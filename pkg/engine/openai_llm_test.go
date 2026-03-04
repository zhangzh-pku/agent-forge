package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
)

func TestOpenAICompatibleClientChatFinalAnswer(t *testing.T) {
	var seenAuth string
	var seenRequest openAIChatRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&seenRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[
				{
					"message":{"content":"done"},
					"finish_reason":"stop"
				}
			],
			"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}
		}`))
	}))
	defer srv.Close()

	client, err := NewOpenAICompatibleClient(OpenAICompatibleClientConfig{
		APIKey:       "test-key",
		BaseURL:      srv.URL,
		DefaultModel: "gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Chat(context.Background(), &LLMRequest{
		Messages: []model.MemoryMessage{
			{Role: model.MessageRoleUser, Content: "hello"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if seenAuth != "Bearer test-key" {
		t.Fatalf("unexpected auth header: %q", seenAuth)
	}
	if seenRequest.Model != "gpt-4o-mini" {
		t.Fatalf("unexpected model: %q", seenRequest.Model)
	}
	if len(seenRequest.Messages) != 1 || seenRequest.Messages[0].Content != "hello" {
		t.Fatalf("unexpected messages: %+v", seenRequest.Messages)
	}
	if resp.Content != "done" {
		t.Fatalf("unexpected response content: %q", resp.Content)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("unexpected finish reason: %q", resp.FinishReason)
	}
	if resp.TokenUsage == nil || resp.TokenUsage.Total != 20 {
		t.Fatalf("unexpected token usage: %+v", resp.TokenUsage)
	}
}

func TestOpenAICompatibleClientChatToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[
				{
					"message":{
						"content":null,
						"tool_calls":[
							{
								"function":{
									"name":"fs.write",
									"arguments":"{\"path\":\"a.txt\",\"content\":\"ok\"}"
								}
							},
							{
								"function":{
									"name":"fs.read",
									"arguments":""
								}
							}
						]
					},
					"finish_reason":"tool_calls"
				}
			]
		}`))
	}))
	defer srv.Close()

	client, err := NewOpenAICompatibleClient(OpenAICompatibleClientConfig{
		APIKey:       "test-key",
		BaseURL:      srv.URL,
		DefaultModel: "gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Chat(context.Background(), &LLMRequest{
		Messages: []model.MemoryMessage{
			{Role: model.MessageRoleUser, Content: "write file"},
		},
		Tools: []ToolSpec{
			{Name: "fs.write", Description: "Write a file"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.FinishReason != "tool_calls" {
		t.Fatalf("unexpected finish reason: %q", resp.FinishReason)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "fs.write" {
		t.Fatalf("unexpected tool name: %q", resp.ToolCalls[0].Name)
	}
	if resp.ToolCalls[1].Args != "{}" {
		t.Fatalf("expected empty args to normalize to {}, got %q", resp.ToolCalls[1].Args)
	}
}

func TestOpenAICompatibleClientChatToolMessageProtocol(t *testing.T) {
	var seenRequest openAIChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&seenRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	client, err := NewOpenAICompatibleClient(OpenAICompatibleClientConfig{
		APIKey:       "test-key",
		BaseURL:      srv.URL,
		DefaultModel: "gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Chat(context.Background(), &LLMRequest{
		Messages: []model.MemoryMessage{
			{
				Role:    model.MessageRoleAssistant,
				Content: "",
				ToolCalls: []model.MemoryToolCall{
					{ID: "call_1", Name: "fs.write", Args: `{"path":"a.txt","content":"x"}`},
				},
			},
			{
				Role:       model.MessageRoleTool,
				ToolCallID: "call_1",
				Content:    "wrote 1 byte",
			},
		},
		Tools: []ToolSpec{
			{
				Name:        "fs.write",
				Description: "Write file",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(seenRequest.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(seenRequest.Messages))
	}
	if len(seenRequest.Messages[0].ToolCalls) != 1 || seenRequest.Messages[0].ToolCalls[0].ID != "call_1" {
		t.Fatalf("assistant tool_calls not mapped correctly: %+v", seenRequest.Messages[0].ToolCalls)
	}
	if seenRequest.Messages[1].ToolCallID != "call_1" {
		t.Fatalf("expected tool_call_id=call_1, got %q", seenRequest.Messages[1].ToolCallID)
	}
	if len(seenRequest.Tools) != 1 {
		t.Fatalf("expected 1 tool definition, got %d", len(seenRequest.Tools))
	}
	if seenRequest.Tools[0].Function.Parameters == nil {
		t.Fatal("expected tool parameters schema to be set")
	}
}

func TestOpenAICompatibleClientChatRetries429(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	client, err := NewOpenAICompatibleClient(OpenAICompatibleClientConfig{
		APIKey:       "test-key",
		BaseURL:      srv.URL,
		DefaultModel: "gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Chat(context.Background(), &LLMRequest{
		Messages: []model.MemoryMessage{{Role: model.MessageRoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 attempts, got %d", calls.Load())
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	got := parseRetryAfter("2", time.Unix(0, 0).UTC())
	if got != 2*time.Second {
		t.Fatalf("expected 2s, got %s", got)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	header := now.Add(5 * time.Second).Format(http.TimeFormat)
	got := parseRetryAfter(header, now)
	if got != 5*time.Second {
		t.Fatalf("expected 5s, got %s", got)
	}
}

func TestParseRetryAfterInvalidOrPast(t *testing.T) {
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("abc", now); got != 0 {
		t.Fatalf("expected 0 for invalid header, got %s", got)
	}
	if got := parseRetryAfter(now.Add(-1*time.Second).Format(http.TimeFormat), now); got != 0 {
		t.Fatalf("expected 0 for past date, got %s", got)
	}
}

func TestOpenAICompatibleClientChatResponseTooLarge(t *testing.T) {
	huge := strings.Repeat("a", openAIMaxResponseBytes+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, huge)))
	}))
	defer srv.Close()

	client, err := NewOpenAICompatibleClient(OpenAICompatibleClientConfig{
		APIKey:       "test-key",
		BaseURL:      srv.URL,
		DefaultModel: "gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Chat(context.Background(), &LLMRequest{
		Messages: []model.MemoryMessage{{Role: model.MessageRoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected response size error")
	}
	if !strings.Contains(err.Error(), "response too large") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOpenAICompatibleClientChatStreamContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"},\"finish_reason\":\"\"}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	client, err := NewOpenAICompatibleClient(OpenAICompatibleClientConfig{
		APIKey:       "test-key",
		BaseURL:      srv.URL,
		DefaultModel: "gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}

	var tokens []string
	resp, err := client.ChatStream(context.Background(), &LLMRequest{
		Messages: []model.MemoryMessage{{Role: model.MessageRoleUser, Content: "hello"}},
	}, func(token string) {
		tokens = append(tokens, token)
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Hello" {
		t.Fatalf("expected aggregated content Hello, got %q", resp.Content)
	}
	if !reflect.DeepEqual(tokens, []string{"Hel", "lo"}) {
		t.Fatalf("unexpected tokens: %#v", tokens)
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("unexpected finish reason: %q", resp.FinishReason)
	}
	if resp.TokenUsage == nil || resp.TokenUsage.Total != 3 {
		t.Fatalf("unexpected usage: %+v", resp.TokenUsage)
	}
}

func TestOpenAICompatibleClientChatStreamToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"fs.write\",\"arguments\":\"{\\\"path\\\":\\\"a.txt\\\"\"}}]},\"finish_reason\":\"\"}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\",\\\"content\\\":\\\"x\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	client, err := NewOpenAICompatibleClient(OpenAICompatibleClientConfig{
		APIKey:       "test-key",
		BaseURL:      srv.URL,
		DefaultModel: "gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.ChatStream(context.Background(), &LLMRequest{
		Messages: []model.MemoryMessage{{Role: model.MessageRoleUser, Content: "write"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "call_1" || resp.ToolCalls[0].Name != "fs.write" {
		t.Fatalf("unexpected tool call metadata: %+v", resp.ToolCalls[0])
	}
	if resp.ToolCalls[0].Args != "{\"path\":\"a.txt\",\"content\":\"x\"}" {
		t.Fatalf("unexpected aggregated args: %q", resp.ToolCalls[0].Args)
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("unexpected finish reason: %q", resp.FinishReason)
	}
}

func TestNewLLMClientFromEnv(t *testing.T) {
	t.Setenv("AGENTFORGE_LLM_PROVIDER", "mock")
	t.Setenv("AGENTFORGE_LLM_MOCK_STEPS", "4")

	client, err := NewLLMClientFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	routed, ok := client.(*RoutedLLMClient)
	if !ok {
		t.Fatalf("expected routed client, got %T", client)
	}
	if _, ok := routed.clients[llmProviderMock].(*MockLLMClient); !ok {
		t.Fatalf("expected routed mock provider client, got %T", routed.clients[llmProviderMock])
	}
}

func TestNewLLMClientFromEnvOpenAIMissingKey(t *testing.T) {
	t.Setenv("AGENTFORGE_LLM_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "")

	_, err := NewLLMClientFromEnv()
	if err == nil {
		t.Fatal("expected error for missing OPENAI_API_KEY")
	}
}

func TestResolveOpenAIAPIKeyPrefersEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-env")
	t.Setenv("OPENAI_API_KEY_SECRET_ARN", "arn:aws:secretsmanager:us-east-1:123:secret:test")

	orig := fetchSecretString
	defer func() { fetchSecretString = orig }()
	fetchSecretString = func(_ context.Context, _ string) (string, error) {
		t.Fatal("fetchSecretString should not be called when OPENAI_API_KEY is set")
		return "", nil
	}

	key, err := resolveOpenAIAPIKey(context.Background())
	if err != nil {
		t.Fatalf("resolveOpenAIAPIKey error: %v", err)
	}
	if key != "sk-env" {
		t.Fatalf("expected sk-env, got %q", key)
	}
}

func TestResolveOpenAIAPIKeyFromSecretJSON(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY_SECRET_ARN", "arn:aws:secretsmanager:us-east-1:123:secret:test")
	t.Setenv("OPENAI_API_KEY_SECRET_FIELD", "")

	orig := fetchSecretString
	defer func() { fetchSecretString = orig }()
	fetchSecretString = func(_ context.Context, secretID string) (string, error) {
		if secretID == "" {
			t.Fatal("expected non-empty secret id")
		}
		return `{"api_key":"sk-json"}`, nil
	}

	key, err := resolveOpenAIAPIKey(context.Background())
	if err != nil {
		t.Fatalf("resolveOpenAIAPIKey error: %v", err)
	}
	if key != "sk-json" {
		t.Fatalf("expected sk-json, got %q", key)
	}
}

func TestResolveOpenAIAPIKeyFromSecretJSONWithField(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY_SECRET_ARN", "arn:aws:secretsmanager:us-east-1:123:secret:test")
	t.Setenv("OPENAI_API_KEY_SECRET_FIELD", "openai_key")

	orig := fetchSecretString
	defer func() { fetchSecretString = orig }()
	fetchSecretString = func(_ context.Context, _ string) (string, error) {
		return `{"openai_key":"sk-field"}`, nil
	}

	key, err := resolveOpenAIAPIKey(context.Background())
	if err != nil {
		t.Fatalf("resolveOpenAIAPIKey error: %v", err)
	}
	if key != "sk-field" {
		t.Fatalf("expected sk-field, got %q", key)
	}
}

func TestExtractAPIKeyFromSecretValueMissingJSONField(t *testing.T) {
	_, err := extractAPIKeyFromSecretValue(`{"foo":"bar"}`, "")
	if err == nil {
		t.Fatal("expected error when json secret has no recognizable api key fields")
	}
}
