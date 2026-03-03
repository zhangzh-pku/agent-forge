package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/agentforge/agentforge/pkg/model"
)

const (
	// Policy modes for routing tradeoffs.
	PolicyLatencyFirst = "latency-first"
	PolicyQualityFirst = "quality-first"
	PolicyCostCap      = "cost-cap"
)

// RouteCandidate is one model/provider target in the fallback chain.
type RouteCandidate struct {
	Provider string
	ModelID  string
}

// RouterConfig defines global routing behavior.
type RouterConfig struct {
	DefaultMode       string
	FallbackProviders []string
}

// ModelRouter builds a task-aware, budget-aware route plan.
type ModelRouter struct {
	cfg RouterConfig
}

// NewModelRouter creates a model router.
func NewModelRouter(cfg RouterConfig) *ModelRouter {
	if strings.TrimSpace(cfg.DefaultMode) == "" {
		cfg.DefaultMode = PolicyLatencyFirst
	}
	return &ModelRouter{cfg: cfg}
}

// Resolve returns an ordered fallback chain.
func (r *ModelRouter) Resolve(req *LLMRequest, primaryProvider string) []RouteCandidate {
	mode := r.cfg.DefaultMode
	taskType := inferTaskType(req)
	if req != nil && req.ModelConfig != nil && strings.TrimSpace(req.ModelConfig.PolicyMode) != "" {
		mode = strings.TrimSpace(req.ModelConfig.PolicyMode)
	}

	baseModel := chooseModel(taskType, mode)
	if req != nil && req.ModelConfig != nil && strings.TrimSpace(req.ModelConfig.ModelID) != "" {
		baseModel = strings.TrimSpace(req.ModelConfig.ModelID)
	}

	candidates := make([]RouteCandidate, 0, 8)

	primary := RouteCandidate{
		Provider: normalizeProvider(primaryProvider),
		ModelID:  baseModel,
	}

	// Budget-aware override for cost-cap policy.
	if req != nil && req.ModelConfig != nil && req.ModelConfig.CostCapUSD > 0 {
		cost := estimateRequestCostUSD(req, baseModel)
		if cost > req.ModelConfig.CostCapUSD {
			primary.ModelID = "gpt-4o-mini"
		}
	}
	candidates = append(candidates, primary)

	if req != nil && req.ModelConfig != nil {
		for _, m := range req.ModelConfig.FallbackModelIDs {
			if strings.TrimSpace(m) == "" {
				continue
			}
			candidates = append(candidates, RouteCandidate{
				Provider: primary.Provider,
				ModelID:  strings.TrimSpace(m),
			})
		}
	}

	for _, p := range r.cfg.FallbackProviders {
		prov := normalizeProvider(p)
		if prov == "" {
			continue
		}
		candidates = append(candidates, RouteCandidate{
			Provider: prov,
			ModelID:  chooseModel(taskType, mode),
		})
	}
	return dedupeRouteCandidates(candidates)
}

func chooseModel(taskType, mode string) string {
	taskType = strings.ToLower(strings.TrimSpace(taskType))
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case PolicyQualityFirst:
		if taskType == "coding" || taskType == "analysis" {
			return "gpt-4o"
		}
		return "gpt-4o"
	case PolicyCostCap, PolicyLatencyFirst:
		return "gpt-4o-mini"
	default:
		return "gpt-4o-mini"
	}
}

func normalizeProvider(provider string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" {
		return llmProviderMock
	}
	return p
}

func dedupeRouteCandidates(in []RouteCandidate) []RouteCandidate {
	seen := make(map[string]struct{}, len(in))
	out := make([]RouteCandidate, 0, len(in))
	for _, c := range in {
		key := c.Provider + "#" + c.ModelID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	return out
}

func inferTaskType(req *LLMRequest) string {
	if req == nil {
		return "general"
	}
	if strings.TrimSpace(req.TaskType) != "" {
		return strings.TrimSpace(req.TaskType)
	}
	// Fall back to coarse prompt/message heuristics.
	var content string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == model.MessageRoleUser {
			content = req.Messages[i].Content
			break
		}
	}
	s := strings.ToLower(content)
	switch {
	case strings.Contains(s, "code"), strings.Contains(s, "test"), strings.Contains(s, "refactor"), strings.Contains(s, "bug"):
		return "coding"
	case strings.Contains(s, "analy"), strings.Contains(s, "report"), strings.Contains(s, "sql"), strings.Contains(s, "metric"):
		return "analysis"
	default:
		return "general"
	}
}

func estimateRequestCostUSD(req *LLMRequest, modelID string) float64 {
	if req == nil {
		return 0
	}
	approxTokens := 256
	if req.ModelConfig != nil && req.ModelConfig.MaxTokens > 0 {
		approxTokens = req.ModelConfig.MaxTokens + 256
	}
	rate := map[string]float64{
		"gpt-4o-mini": 0.00035,
		"gpt-4o":      0.00500,
		"gpt-4.1":     0.00600,
	}
	r, ok := rate[strings.ToLower(strings.TrimSpace(modelID))]
	if !ok {
		r = rate["gpt-4o-mini"]
	}
	return (float64(approxTokens) / 1000.0) * r
}

// EstimateUsageCostUSD estimates cost from actual token usage.
func EstimateUsageCostUSD(modelID string, usage *model.TokenUsage) float64 {
	if usage == nil || usage.Total <= 0 {
		return 0
	}
	rate := map[string]float64{
		"gpt-4o-mini":     0.00035,
		"gpt-4o":          0.00500,
		"gpt-4.1":         0.00600,
		"claude-3-haiku":  0.00080,
		"claude-3-sonnet": 0.00300,
		"claude-3-opus":   0.01500,
	}
	r, ok := rate[strings.ToLower(strings.TrimSpace(modelID))]
	if !ok {
		r = rate["gpt-4o-mini"]
	}
	return (float64(usage.Total) / 1000.0) * r
}

// RoutedLLMClient adds task-aware routing and fallback behavior.
type RoutedLLMClient struct {
	router          *ModelRouter
	primaryProvider string
	clients         map[string]LLMClient
}

// NewRoutedLLMClient creates a routed client wrapper.
func NewRoutedLLMClient(primaryProvider string, clients map[string]LLMClient, router *ModelRouter) *RoutedLLMClient {
	if router == nil {
		router = NewModelRouter(RouterConfig{})
	}
	if clients == nil {
		clients = make(map[string]LLMClient)
	}
	return &RoutedLLMClient{
		router:          router,
		primaryProvider: normalizeProvider(primaryProvider),
		clients:         clients,
	}
}

func (c *RoutedLLMClient) Chat(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	candidates := c.router.Resolve(req, c.primaryProvider)
	var lastErr error
	for i, cand := range candidates {
		client := c.clients[cand.Provider]
		if client == nil {
			continue
		}
		resp, err := client.Chat(ctx, withModel(req, cand.ModelID))
		if err == nil {
			if resp.ModelID == "" {
				resp.ModelID = cand.ModelID
			}
			if resp.Provider == "" {
				resp.Provider = cand.Provider
			}
			return resp, nil
		}
		lastErr = err
		if !shouldFallbackOnError(err) || i == len(candidates)-1 {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no available routed providers")
	}
	return nil, fmt.Errorf("routing failed: %w", lastErr)
}

func (c *RoutedLLMClient) ChatStream(ctx context.Context, req *LLMRequest, onToken func(token string)) (*LLMResponse, error) {
	candidates := c.router.Resolve(req, c.primaryProvider)
	var lastErr error
	for i, cand := range candidates {
		client := c.clients[cand.Provider]
		if client == nil {
			continue
		}
		adapted := withModel(req, cand.ModelID)
		if sclient, ok := client.(StreamingLLMClient); ok {
			resp, err := sclient.ChatStream(ctx, adapted, onToken)
			if err == nil {
				if resp.ModelID == "" {
					resp.ModelID = cand.ModelID
				}
				if resp.Provider == "" {
					resp.Provider = cand.Provider
				}
				return resp, nil
			}
			lastErr = err
		} else {
			resp, err := client.Chat(ctx, adapted)
			if err == nil {
				if onToken != nil && resp.Content != "" {
					onToken(resp.Content)
				}
				if resp.ModelID == "" {
					resp.ModelID = cand.ModelID
				}
				if resp.Provider == "" {
					resp.Provider = cand.Provider
				}
				return resp, nil
			}
			lastErr = err
		}
		if !shouldFallbackOnError(lastErr) || i == len(candidates)-1 {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no available routed providers")
	}
	return nil, fmt.Errorf("routing stream failed: %w", lastErr)
}

func withModel(req *LLMRequest, modelID string) *LLMRequest {
	if req == nil {
		return &LLMRequest{
			ModelConfig: &model.ModelConfig{ModelID: modelID},
		}
	}
	cp := *req
	if req.ModelConfig != nil {
		mc := *req.ModelConfig
		mc.ModelID = modelID
		if len(req.ModelConfig.FallbackModelIDs) > 0 {
			mc.FallbackModelIDs = append([]string(nil), req.ModelConfig.FallbackModelIDs...)
		}
		cp.ModelConfig = &mc
	} else {
		cp.ModelConfig = &model.ModelConfig{ModelID: modelID}
	}
	return &cp
}

func shouldFallbackOnError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "context canceled") || strings.Contains(msg, "deadline exceeded") {
		return false
	}
	nonRetryable := []string{"invalid", "bad request", "401", "403", "permission", "unauthorized"}
	for _, marker := range nonRetryable {
		if strings.Contains(msg, marker) {
			return false
		}
	}
	retryable := []string{
		"timeout", "temporary", "temporarily", "429", "500", "502", "503", "504",
		"unavailable", "connection reset", "refused", "eof", "rate limit",
	}
	for _, marker := range retryable {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	// Default to conservative fallback to improve availability.
	return true
}
