// Package engine implements the ReAct execution loop.
package engine

import (
	"context"
	"encoding/json"

	"github.com/agentforge/agentforge/pkg/model"
)

// LLMRequest is the input to an LLM call.
type LLMRequest struct {
	Messages    []model.MemoryMessage `json:"messages"`
	ModelConfig *model.ModelConfig    `json:"model_config,omitempty"`
	Tools       []ToolSpec            `json:"tools,omitempty"`
	TaskType    string                `json:"task_type,omitempty"`
}

// LLMResponse is the output of an LLM call.
type LLMResponse struct {
	Content      string            `json:"content"`
	ToolCalls    []ToolCall        `json:"tool_calls,omitempty"`
	TokenUsage   *model.TokenUsage `json:"token_usage,omitempty"`
	FinishReason string            `json:"finish_reason,omitempty"`
	ModelID      string            `json:"model_id,omitempty"`
	Provider     string            `json:"provider,omitempty"`
}

// ToolCall represents a tool invocation requested by the LLM.
type ToolCall struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	Args string `json:"args"` // JSON-encoded arguments
}

// ToolSpec describes an available tool for the LLM.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// LLMClient abstracts the language model backend.
type LLMClient interface {
	// Chat sends a completion request. Must respect context cancellation.
	Chat(ctx context.Context, req *LLMRequest) (*LLMResponse, error)
}

// StreamingLLMClient is an optional extension for token-by-token streaming.
type StreamingLLMClient interface {
	LLMClient
	// ChatStream sends a completion request and invokes onToken for streamed token deltas.
	// Implementations should still return the final aggregated response.
	ChatStream(ctx context.Context, req *LLMRequest, onToken func(token string)) (*LLMResponse, error)
}
