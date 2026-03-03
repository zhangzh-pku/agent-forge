package task

import (
	"math"
	"strings"

	"github.com/agentforge/agentforge/pkg/model"
)

const defaultModelID = "gpt-4o-mini"

// InferTaskType classifies task prompts for routing/scheduling.
func InferTaskType(prompt string) string {
	p := strings.ToLower(strings.TrimSpace(prompt))
	switch {
	case strings.Contains(p, "code"), strings.Contains(p, "golang"), strings.Contains(p, "python"), strings.Contains(p, "typescript"), strings.Contains(p, "bug"), strings.Contains(p, "test"):
		return "coding"
	case strings.Contains(p, "analy"), strings.Contains(p, "report"), strings.Contains(p, "metrics"), strings.Contains(p, "sql"):
		return "analysis"
	case strings.Contains(p, "summar"), strings.Contains(p, "translate"), strings.Contains(p, "rewrite"):
		return "writing"
	default:
		return "general"
	}
}

// EstimateUsage returns rough token/cost usage estimates for queue admission.
func EstimateUsage(prompt string, mc *model.ModelConfig) (tokens int64, costUSD float64) {
	modelID := defaultModelID
	maxTokens := 1024
	if mc != nil {
		if strings.TrimSpace(mc.ModelID) != "" {
			modelID = mc.ModelID
		}
		if mc.MaxTokens > 0 {
			maxTokens = mc.MaxTokens
		}
	}

	// Approximation: 1 word ~= 1.3 tokens. Add output cap allowance.
	words := len(strings.Fields(prompt))
	estInput := int64(math.Ceil(float64(words) * 1.3))
	tokens = estInput + int64(maxTokens)
	if tokens < 256 {
		tokens = 256
	}

	costUSD = estimateModelCostUSD(modelID, tokens)
	return tokens, costUSD
}

func estimateModelCostUSD(modelID string, totalTokens int64) float64 {
	if totalTokens <= 0 {
		return 0
	}
	// Coarse cost table in USD / 1K tokens.
	pricing := map[string]float64{
		"gpt-4o-mini":     0.00035,
		"gpt-4o":          0.00500,
		"gpt-4.1":         0.00600,
		"claude-3-haiku":  0.00080,
		"claude-3-sonnet": 0.00300,
		"claude-3-opus":   0.01500,
	}
	modelKey := strings.ToLower(strings.TrimSpace(modelID))
	rate, ok := pricing[modelKey]
	if !ok {
		rate = pricing["gpt-4o-mini"]
	}
	return (float64(totalTokens) / 1000.0) * rate
}
