package task

import (
	"testing"

	"github.com/agentforge/agentforge/pkg/model"
)

func TestInferTaskType(t *testing.T) {
	cases := []struct {
		prompt string
		want   string
	}{
		{prompt: "Please fix this golang bug", want: "coding"},
		{prompt: "Generate a SQL metrics report", want: "analysis"},
		{prompt: "Summarize and rewrite this article", want: "writing"},
		{prompt: "Tell me a story", want: "general"},
	}

	for _, tc := range cases {
		if got := InferTaskType(tc.prompt); got != tc.want {
			t.Fatalf("InferTaskType(%q): expected %q, got %q", tc.prompt, tc.want, got)
		}
	}
}

func TestEstimateUsageDefaultsAndMinimum(t *testing.T) {
	tokens, cost := EstimateUsage("short prompt", nil)
	// Defaults: maxTokens=1024, plus input estimate should exceed the 256 floor.
	if tokens <= 1024 {
		t.Fatalf("expected tokens to include input + output allowance, got %d", tokens)
	}
	if cost <= 0 {
		t.Fatalf("expected positive estimated cost, got %.8f", cost)
	}

	// Empty prompt with max_tokens=1 should still be floored to 256.
	tokens, _ = EstimateUsage("", &model.ModelConfig{ModelID: "gpt-4o-mini", MaxTokens: 1})
	if tokens != 256 {
		t.Fatalf("expected token floor 256, got %d", tokens)
	}
}

func TestEstimateUsageUsesModelConfig(t *testing.T) {
	mc := &model.ModelConfig{
		ModelID:   "gpt-4o",
		MaxTokens: 2048,
	}
	tokens, cost := EstimateUsage("this is a medium length prompt for estimation", mc)
	if tokens < 2048 {
		t.Fatalf("expected tokens >= configured max_tokens, got %d", tokens)
	}

	miniCost := estimateModelCostUSD("gpt-4o-mini", tokens)
	if cost <= miniCost {
		t.Fatalf("expected gpt-4o estimated cost %.8f to be higher than mini %.8f", cost, miniCost)
	}
}

func TestEstimateModelCostUSDFallbackAndBounds(t *testing.T) {
	if got := estimateModelCostUSD("gpt-4o-mini", 1000); got != 0.00035 {
		t.Fatalf("expected exact mini price for 1k tokens, got %.8f", got)
	}
	if got := estimateModelCostUSD("unknown-model", 1000); got != 0.00035 {
		t.Fatalf("expected unknown model to fallback to mini rate, got %.8f", got)
	}
	if got := estimateModelCostUSD("gpt-4o-mini", 0); got != 0 {
		t.Fatalf("expected zero cost for zero tokens, got %.8f", got)
	}
	if got := estimateModelCostUSD("gpt-4o-mini", -10); got != 0 {
		t.Fatalf("expected zero cost for negative tokens, got %.8f", got)
	}
}
