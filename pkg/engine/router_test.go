package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/agentforge/agentforge/pkg/model"
)

type alwaysErrClient struct {
	err error
}

func (c *alwaysErrClient) Chat(_ context.Context, _ *LLMRequest) (*LLMResponse, error) {
	return nil, c.err
}

type staticClient struct {
	resp *LLMResponse
}

func (c *staticClient) Chat(_ context.Context, _ *LLMRequest) (*LLMResponse, error) {
	return c.resp, nil
}

func TestModelRouterResolve(t *testing.T) {
	router := NewModelRouter(RouterConfig{
		DefaultMode:       PolicyQualityFirst,
		FallbackProviders: []string{"mock"},
	})

	req := &LLMRequest{
		TaskType: "coding",
		ModelConfig: &model.ModelConfig{
			PolicyMode:       PolicyQualityFirst,
			FallbackModelIDs: []string{"gpt-4o-mini"},
		},
	}

	route := router.Resolve(req, llmProviderOpenAI)
	if len(route) < 2 {
		t.Fatalf("expected at least 2 route candidates, got %v", route)
	}
	if route[0].Provider != llmProviderOpenAI || route[0].ModelID != "gpt-4o" {
		t.Fatalf("unexpected primary route: %+v", route[0])
	}
}

func TestModelRouterCostCap(t *testing.T) {
	router := NewModelRouter(RouterConfig{
		DefaultMode: PolicyQualityFirst,
	})
	req := &LLMRequest{
		TaskType: "analysis",
		ModelConfig: &model.ModelConfig{
			PolicyMode: PolicyCostCap,
			CostCapUSD: 0.01,
			MaxTokens:  8000,
			ModelID:    "gpt-4o",
		},
	}
	route := router.Resolve(req, llmProviderOpenAI)
	if len(route) == 0 {
		t.Fatal("expected non-empty route")
	}
	if route[0].ModelID != "gpt-4o-mini" {
		t.Fatalf("expected cost-cap to force mini model, got %s", route[0].ModelID)
	}
}

func TestRoutedLLMFallback(t *testing.T) {
	router := NewModelRouter(RouterConfig{
		DefaultMode:       PolicyLatencyFirst,
		FallbackProviders: []string{"mock"},
	})
	client := NewRoutedLLMClient(llmProviderOpenAI, map[string]LLMClient{
		llmProviderOpenAI: &alwaysErrClient{err: errors.New("status 503 unavailable")},
		llmProviderMock:   &staticClient{resp: &LLMResponse{Content: "ok"}},
	}, router)

	resp, err := client.Chat(context.Background(), &LLMRequest{
		Messages: []model.MemoryMessage{
			{Role: model.MessageRoleUser, Content: "hello"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != llmProviderMock {
		t.Fatalf("expected fallback provider mock, got %s", resp.Provider)
	}
}

func TestShouldFallbackOnErrorDefaultFalseForUnknown(t *testing.T) {
	if shouldFallbackOnError(errors.New("unexpected upstream parse failure")) {
		t.Fatal("expected unknown errors to not trigger fallback by default")
	}
}

func TestShouldFallbackOnErrorRetryableStillTrue(t *testing.T) {
	if !shouldFallbackOnError(errors.New("status 503 unavailable")) {
		t.Fatal("expected retryable errors to trigger fallback")
	}
}
