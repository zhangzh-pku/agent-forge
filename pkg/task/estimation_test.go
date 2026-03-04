package task

import (
	"testing"

	"github.com/agentforge/agentforge/pkg/model"
)

func TestInferTaskType(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{name: "coding by language", prompt: "Write golang code to fix bug", want: "coding"},
		{name: "analysis by keyword", prompt: "Analyze metrics and build report", want: "analysis"},
		{name: "writing by keyword", prompt: "Summarize and rewrite this text", want: "writing"},
		{name: "default general", prompt: "Plan a weekend trip", want: "general"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := InferTaskType(tc.prompt)
			if got != tc.want {
				t.Fatalf("InferTaskType(%q) = %q, want %q", tc.prompt, got, tc.want)
			}
		})
	}
}

func TestEstimateUsageDefaults(t *testing.T) {
	tokens, cost := EstimateUsage("hello world", nil)
	if tokens < 256 {
		t.Fatalf("expected token floor 256, got %d", tokens)
	}
	if cost <= 0 {
		t.Fatalf("expected positive cost, got %f", cost)
	}
}

func TestEstimateUsageUsesModelConfig(t *testing.T) {
	tokens, cost := EstimateUsage("build release notes", &model.ModelConfig{
		ModelID:   "gpt-4o",
		MaxTokens: 4096,
	})
	if tokens < 4096 {
		t.Fatalf("expected tokens to include max_tokens, got %d", tokens)
	}
	if cost <= 0 {
		t.Fatalf("expected positive cost, got %f", cost)
	}
}

func TestEstimateModelCostUSDFallbackAndZero(t *testing.T) {
	if got := estimateModelCostUSD("unknown-model", 1000); got <= 0 {
		t.Fatalf("expected fallback model cost > 0, got %f", got)
	}
	if got := estimateModelCostUSD("gpt-4o-mini", 0); got != 0 {
		t.Fatalf("expected 0 cost for non-positive tokens, got %f", got)
	}
}
