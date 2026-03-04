package engine

import (
	"os"
	"strconv"
	"strings"
)

const modelPricingOverridesEnv = "AGENTFORGE_MODEL_PRICING_OVERRIDES"

var defaultRequestModelPricingUSDPer1K = map[string]float64{
	"gpt-4o-mini": 0.00035,
	"gpt-4o":      0.00500,
	"gpt-4.1":     0.00600,
}

var defaultUsageModelPricingUSDPer1K = map[string]float64{
	"gpt-4o-mini":     0.00035,
	"gpt-4o":          0.00500,
	"gpt-4.1":         0.00600,
	"claude-3-haiku":  0.00080,
	"claude-3-sonnet": 0.00300,
	"claude-3-opus":   0.01500,
}

func resolveModelRateUSDPer1K(modelID string, defaults map[string]float64) float64 {
	rates := mergePricingOverrides(defaults, os.Getenv(modelPricingOverridesEnv))
	model := strings.ToLower(strings.TrimSpace(modelID))
	if model != "" {
		if rate, ok := rates[model]; ok {
			return rate
		}
	}
	return defaults["gpt-4o-mini"]
}

func mergePricingOverrides(defaults map[string]float64, raw string) map[string]float64 {
	merged := make(map[string]float64, len(defaults))
	for model, rate := range defaults {
		merged[strings.ToLower(strings.TrimSpace(model))] = rate
	}
	if strings.TrimSpace(raw) == "" {
		return merged
	}
	for _, entry := range strings.Split(raw, ",") {
		parts := strings.SplitN(strings.TrimSpace(entry), "=", 2)
		if len(parts) != 2 {
			continue
		}
		model := strings.ToLower(strings.TrimSpace(parts[0]))
		if model == "" {
			continue
		}
		rate, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil || rate <= 0 {
			continue
		}
		merged[model] = rate
	}
	return merged
}
