package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
			{Role: "user", Content: "hello"},
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
			{Role: "user", Content: "write file"},
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

func TestNewLLMClientFromEnv(t *testing.T) {
	t.Setenv("AGENTFORGE_LLM_PROVIDER", "mock")
	t.Setenv("AGENTFORGE_LLM_MOCK_STEPS", "4")

	client, err := NewLLMClientFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := client.(*MockLLMClient); !ok {
		t.Fatalf("expected mock client, got %T", client)
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
