package engine

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/agentforge/agentforge/pkg/model"
)

// MockLLMClient is a deterministic LLM client for testing.
// It produces a sequence of responses: first a tool call, then observations, then a final answer.
type MockLLMClient struct {
	callCount atomic.Int64
	maxSteps  int
}

// NewMockLLMClient creates a mock that produces maxSteps tool calls before a final answer.
func NewMockLLMClient(maxSteps int) *MockLLMClient {
	if maxSteps <= 0 {
		maxSteps = 2
	}
	return &MockLLMClient{maxSteps: maxSteps}
}

func (m *MockLLMClient) Chat(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	count := int(m.callCount.Add(1))

	if count <= m.maxSteps {
		// Return a tool call.
		return &LLMResponse{
			Content: fmt.Sprintf("I'll use a tool (step %d)", count),
			ToolCalls: []ToolCall{
				{
					Name: "fs.write",
					Args: fmt.Sprintf(`{"path":"step_%d.txt","content":"output from step %d"}`, count, count),
				},
			},
			TokenUsage:   &model.TokenUsage{Input: 100, Output: 50, Total: 150},
			FinishReason: "tool_calls",
		}, nil
	}

	// Final answer.
	return &LLMResponse{
		Content:      fmt.Sprintf("Task complete after %d steps.", count-1),
		TokenUsage:   &model.TokenUsage{Input: 80, Output: 30, Total: 110},
		FinishReason: "stop",
	}, nil
}

// ChatStream streams response content in small chunks while returning the same final response.
func (m *MockLLMClient) ChatStream(ctx context.Context, req *LLMRequest, onToken func(token string)) (*LLMResponse, error) {
	resp, err := m.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	if onToken != nil && resp.Content != "" {
		parts := strings.Fields(resp.Content)
		for i, p := range parts {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			if i == 0 {
				onToken(p)
			} else {
				onToken(" " + p)
			}
		}
	}
	return resp, nil
}

// Reset allows reusing the mock.
func (m *MockLLMClient) Reset() {
	m.callCount.Store(0)
}
