package engine

import (
	"bufio"
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

const (
	openAIMaxResponseBytes = 2 << 20 // 2 MiB
	openAIMaxStreamBytes   = 8 << 20 // 8 MiB
	openAIMaxSSELineBytes  = 1 << 20 // 1 MiB
	openAIMaxAttempts      = 3
)

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

	requestBody := c.buildChatRequest(req, false)

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("engine: openai client: marshal request: %w", err)
	}

	respBody, err := c.doJSONRequest(ctx, bodyBytes)
	if err != nil {
		return nil, err
	}

	var completion openAIChatResponse
	if err := json.Unmarshal(respBody, &completion); err != nil {
		return nil, fmt.Errorf("engine: openai client: decode response: %w", err)
	}
	return completionToLLMResponse(&completion)
}

// ChatStream sends a streaming completion request and pushes token deltas via callback.
func (c *OpenAICompatibleClient) ChatStream(ctx context.Context, req *LLMRequest, onToken func(token string)) (*LLMResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("engine: openai client: request is nil")
	}
	requestBody := c.buildChatRequest(req, true)
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("engine: openai client: marshal stream request: %w", err)
	}

	resp, err := c.doStreamRequest(ctx, bodyBytes)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	streamReader := io.LimitReader(resp.Body, openAIMaxStreamBytes)
	scanner := bufio.NewScanner(streamReader)
	scanner.Buffer(make([]byte, 64*1024), openAIMaxSSELineBytes)

	var contentBuilder strings.Builder
	partialToolCalls := make(map[int]*ToolCall)
	var finishReason string
	var usage *model.TokenUsage

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			break
		}

		var chunk openAIChatStreamResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return nil, fmt.Errorf("engine: openai client: decode stream chunk: %w", err)
		}
		if chunk.Usage != nil {
			usage = &model.TokenUsage{
				Input:  chunk.Usage.PromptTokens,
				Output: chunk.Usage.CompletionTokens,
				Total:  chunk.Usage.TotalTokens,
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}

		token := normalizeContent(choice.Delta.Content)
		if token != "" {
			contentBuilder.WriteString(token)
			if onToken != nil {
				onToken(token)
			}
		}

		for _, tcd := range choice.Delta.ToolCalls {
			call, ok := partialToolCalls[tcd.Index]
			if !ok {
				call = &ToolCall{}
				partialToolCalls[tcd.Index] = call
			}
			if tcd.ID != "" {
				call.ID = tcd.ID
			}
			if tcd.Function.Name != "" {
				call.Name = tcd.Function.Name
			}
			if tcd.Function.Arguments != "" {
				call.Args += tcd.Function.Arguments
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("engine: openai client: read stream: %w", err)
	}

	var toolCalls []ToolCall
	if len(partialToolCalls) > 0 {
		idxs := make([]int, 0, len(partialToolCalls))
		for idx := range partialToolCalls {
			idxs = append(idxs, idx)
		}
		// Stable ordering by stream index.
		for i := 0; i < len(idxs); i++ {
			for j := i + 1; j < len(idxs); j++ {
				if idxs[j] < idxs[i] {
					idxs[i], idxs[j] = idxs[j], idxs[i]
				}
			}
		}
		toolCalls = make([]ToolCall, 0, len(partialToolCalls))
		for _, idx := range idxs {
			tc := partialToolCalls[idx]
			if strings.TrimSpace(tc.Name) == "" {
				continue
			}
			args := strings.TrimSpace(tc.Args)
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:   tc.ID,
				Name: tc.Name,
				Args: args,
			})
		}
	}

	return &LLMResponse{
		Content:      contentBuilder.String(),
		ToolCalls:    toolCalls,
		TokenUsage:   usage,
		FinishReason: finishReason,
	}, nil
}

func (c *OpenAICompatibleClient) buildChatRequest(req *LLMRequest, stream bool) openAIChatRequest {
	modelID := c.defaultModel
	if req.ModelConfig != nil && strings.TrimSpace(req.ModelConfig.ModelID) != "" {
		modelID = strings.TrimSpace(req.ModelConfig.ModelID)
	}
	requestBody := openAIChatRequest{
		Model:    modelID,
		Messages: make([]openAIMessage, 0, len(req.Messages)),
	}
	for _, m := range req.Messages {
		msg := openAIMessage{Role: string(m.Role)}
		switch m.Role {
		case model.MessageRoleAssistant:
			msg.Content = m.Content
			if len(m.ToolCalls) > 0 {
				msg.ToolCalls = make([]openAIToolCallMessage, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					msg.ToolCalls = append(msg.ToolCalls, openAIToolCallMessage{
						ID:   tc.ID,
						Type: "function",
						Function: openAIToolFunctionCall{
							Name:      tc.Name,
							Arguments: tc.Args,
						},
					})
				}
			}
		case model.MessageRoleTool:
			msg.Content = m.Content
			msg.ToolCallID = m.ToolCallID
		default:
			msg.Content = m.Content
		}
		requestBody.Messages = append(requestBody.Messages, msg)
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
					Parameters:  map[string]interface{}{"type": "object", "additionalProperties": true},
				},
			})
			if len(tool.Parameters) > 0 && json.Valid(tool.Parameters) {
				var parsed interface{}
				if err := json.Unmarshal(tool.Parameters, &parsed); err == nil {
					requestBody.Tools[len(requestBody.Tools)-1].Function.Parameters = parsed
				}
			}
		}
		if len(requestBody.Tools) > 0 {
			requestBody.ToolChoice = "auto"
		}
	}
	if stream {
		requestBody.Stream = true
		requestBody.StreamOptions = &openAIStreamOptions{IncludeUsage: true}
	}
	return requestBody
}

func (c *OpenAICompatibleClient) doJSONRequest(ctx context.Context, bodyBytes []byte) ([]byte, error) {
	var respBody []byte
	var lastErr error
	for attempt := 1; attempt <= openAIMaxAttempts; attempt++ {
		httpReq, err := c.newRequest(ctx, bodyBytes)
		if err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("engine: openai client: request failed: %w", err)
			if attempt < openAIMaxAttempts && ctx.Err() == nil {
				sleepWithBackoff(ctx, attempt)
				continue
			}
			return nil, lastErr
		}

		respBody, err = io.ReadAll(io.LimitReader(resp.Body, openAIMaxResponseBytes+1))
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("engine: openai client: read response: %w", err)
			if attempt < openAIMaxAttempts && ctx.Err() == nil {
				sleepWithBackoff(ctx, attempt)
				continue
			}
			return nil, lastErr
		}
		if len(respBody) > openAIMaxResponseBytes {
			return nil, fmt.Errorf("engine: openai client: response too large (> %d bytes)", openAIMaxResponseBytes)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return respBody, nil
		}
		lastErr = parseOpenAIError(resp.StatusCode, respBody)
		if attempt < openAIMaxAttempts && shouldRetryStatus(resp.StatusCode) && ctx.Err() == nil {
			sleepWithBackoff(ctx, attempt)
			continue
		}
		return nil, lastErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("engine: openai client: empty response")
}

func (c *OpenAICompatibleClient) doStreamRequest(ctx context.Context, bodyBytes []byte) (*http.Response, error) {
	var lastErr error
	for attempt := 1; attempt <= openAIMaxAttempts; attempt++ {
		httpReq, err := c.newRequest(ctx, bodyBytes)
		if err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("engine: openai client: stream request failed: %w", err)
			if attempt < openAIMaxAttempts && ctx.Err() == nil {
				sleepWithBackoff(ctx, attempt)
				continue
			}
			return nil, lastErr
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}

		// Drain a bounded error body for diagnostics before retry/return.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, openAIMaxResponseBytes))
		resp.Body.Close()
		lastErr = parseOpenAIError(resp.StatusCode, errBody)
		if attempt < openAIMaxAttempts && shouldRetryStatus(resp.StatusCode) && ctx.Err() == nil {
			sleepWithBackoff(ctx, attempt)
			continue
		}
		return nil, lastErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("engine: openai client: stream request failed")
}

func (c *OpenAICompatibleClient) newRequest(ctx context.Context, bodyBytes []byte) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("engine: openai client: build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	return httpReq, nil
}

func completionToLLMResponse(completion *openAIChatResponse) (*LLMResponse, error) {
	if completion == nil || len(completion.Choices) == 0 {
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
			ID:   tc.ID,
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

func shouldRetryStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusBadGateway ||
		statusCode == http.StatusServiceUnavailable ||
		statusCode == http.StatusGatewayTimeout
}

func sleepWithBackoff(ctx context.Context, attempt int) {
	if attempt <= 0 {
		return
	}
	backoff := 200 * time.Millisecond
	for i := 1; i < attempt; i++ {
		backoff *= 2
	}
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

type openAIChatRequest struct {
	Model         string               `json:"model"`
	Messages      []openAIMessage      `json:"messages"`
	Temperature   *float64             `json:"temperature,omitempty"`
	MaxTokens     *int                 `json:"max_tokens,omitempty"`
	Tools         []openAITool         `json:"tools,omitempty"`
	ToolChoice    string               `json:"tool_choice,omitempty"`
	Stream        bool                 `json:"stream,omitempty"`
	StreamOptions *openAIStreamOptions `json:"stream_options,omitempty"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type openAIMessage struct {
	Role       string                  `json:"role"`
	Content    interface{}             `json:"content,omitempty"`
	ToolCallID string                  `json:"tool_call_id,omitempty"`
	ToolCalls  []openAIToolCallMessage `json:"tool_calls,omitempty"`
}

type openAITool struct {
	Type     string            `json:"type"`
	Function openAIFunctionDef `json:"function"`
}

type openAIFunctionDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
}

type openAIChatResponse struct {
	Choices []openAIChoice `json:"choices"`
	Usage   *openAIUsage   `json:"usage,omitempty"`
}

type openAIChatStreamResponse struct {
	Choices []openAIStreamChoice `json:"choices"`
	Usage   *openAIUsage         `json:"usage,omitempty"`
}

type openAIChoice struct {
	Message      openAIChoiceMessage `json:"message"`
	FinishReason string              `json:"finish_reason"`
}

type openAIStreamChoice struct {
	Delta        openAIStreamDelta `json:"delta"`
	FinishReason string            `json:"finish_reason"`
}

type openAIChoiceMessage struct {
	Content   json.RawMessage         `json:"content"`
	ToolCalls []openAIToolCallMessage `json:"tool_calls,omitempty"`
}

type openAIStreamDelta struct {
	Content   json.RawMessage        `json:"content,omitempty"`
	ToolCalls []openAIStreamToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCallMessage struct {
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type,omitempty"`
	Function openAIToolFunctionCall `json:"function"`
}

type openAIStreamToolCall struct {
	Index    int                    `json:"index"`
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type,omitempty"`
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
