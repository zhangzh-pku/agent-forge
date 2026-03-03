package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
)

const defaultOpenAIBaseURL = "https://api.openai.com/v1"

// OpenAICompatibleClientConfig configures an OpenAI-compatible chat completions client.
type OpenAICompatibleClientConfig struct {
	APIKey       string
	BaseURL      string
	DefaultModel string
	Timeout      time.Duration
	HTTPClient   *http.Client
}

// OpenAICompatibleClient implements LLMClient using an OpenAI-compatible /chat/completions API.
type OpenAICompatibleClient struct {
	apiKey       string
	baseURL      string
	defaultModel string
	httpClient   *http.Client
}

// NewOpenAICompatibleClient creates an LLM client backed by an OpenAI-compatible API.
func NewOpenAICompatibleClient(cfg OpenAICompatibleClientConfig) (*OpenAICompatibleClient, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("engine: openai client: api key is required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}
	modelID := strings.TrimSpace(cfg.DefaultModel)
	if modelID == "" {
		modelID = "gpt-4o-mini"
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		httpClient = &http.Client{Timeout: timeout}
	}

	return &OpenAICompatibleClient{
		apiKey:       cfg.APIKey,
		baseURL:      baseURL,
		defaultModel: modelID,
		httpClient:   httpClient,
	}, nil
}

func (c *OpenAICompatibleClient) Chat(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("engine: openai client: request is nil")
	}

	modelID := c.defaultModel
	if req.ModelConfig != nil && strings.TrimSpace(req.ModelConfig.ModelID) != "" {
		modelID = strings.TrimSpace(req.ModelConfig.ModelID)
	}

	requestBody := openAIChatRequest{
		Model:    modelID,
		Messages: make([]openAIMessage, 0, len(req.Messages)),
	}
	for _, m := range req.Messages {
		requestBody.Messages = append(requestBody.Messages, openAIMessage{
			Role:    m.Role,
			Content: m.Content,
		})
	}
	if req.ModelConfig != nil {
		if req.ModelConfig.Temperature > 0 {
			temp := req.ModelConfig.Temperature
			requestBody.Temperature = &temp
		}
		if req.ModelConfig.MaxTokens > 0 {
			maxTokens := req.ModelConfig.MaxTokens
			requestBody.MaxTokens = &maxTokens
		}
	}
	if len(req.Tools) > 0 {
		requestBody.Tools = make([]openAITool, 0, len(req.Tools))
		for _, tool := range req.Tools {
			if strings.TrimSpace(tool.Name) == "" {
				continue
			}
			requestBody.Tools = append(requestBody.Tools, openAITool{
				Type: "function",
				Function: openAIFunctionDef{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters: map[string]interface{}{
						"type":                 "object",
						"additionalProperties": true,
					},
				},
			})
		}
		if len(requestBody.Tools) > 0 {
			requestBody.ToolChoice = "auto"
		}
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("engine: openai client: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("engine: openai client: build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("engine: openai client: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("engine: openai client: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseOpenAIError(resp.StatusCode, respBody)
	}

	var completion openAIChatResponse
	if err := json.Unmarshal(respBody, &completion); err != nil {
		return nil, fmt.Errorf("engine: openai client: decode response: %w", err)
	}
	if len(completion.Choices) == 0 {
		return nil, fmt.Errorf("engine: openai client: empty choices")
	}

	choice := completion.Choices[0]
	content := normalizeContent(choice.Message.Content)
	toolCalls := make([]ToolCall, 0, len(choice.Message.ToolCalls))
	for _, tc := range choice.Message.ToolCalls {
		if strings.TrimSpace(tc.Function.Name) == "" {
			continue
		}
		args := strings.TrimSpace(tc.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		toolCalls = append(toolCalls, ToolCall{
			Name: tc.Function.Name,
			Args: args,
		})
	}

	var usage *model.TokenUsage
	if completion.Usage != nil {
		usage = &model.TokenUsage{
			Input:  completion.Usage.PromptTokens,
			Output: completion.Usage.CompletionTokens,
			Total:  completion.Usage.TotalTokens,
		}
	}

	return &LLMResponse{
		Content:      content,
		ToolCalls:    toolCalls,
		TokenUsage:   usage,
		FinishReason: choice.FinishReason,
	}, nil
}

func normalizeContent(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}

	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" || p.Type == "" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

func parseOpenAIError(statusCode int, body []byte) error {
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error.Message != "" {
		return fmt.Errorf("engine: openai client: status %d: %s", statusCode, payload.Error.Message)
	}
	return fmt.Errorf("engine: openai client: status %d: %s", statusCode, strings.TrimSpace(string(body)))
}

type openAIChatRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	Tools       []openAITool    `json:"tools,omitempty"`
	ToolChoice  string          `json:"tool_choice,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAITool struct {
	Type     string            `json:"type"`
	Function openAIFunctionDef `json:"function"`
}

type openAIFunctionDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

type openAIChatResponse struct {
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage,omitempty"`
}

type openAIChoice struct {
	Message      openAIChoiceMessage `json:"message"`
	FinishReason string              `json:"finish_reason"`
}

type openAIChoiceMessage struct {
	Content   json.RawMessage         `json:"content"`
	ToolCalls []openAIToolCallMessage `json:"tool_calls,omitempty"`
}

type openAIToolCallMessage struct {
	Function openAIToolFunctionCall `json:"function"`
}

type openAIToolFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
