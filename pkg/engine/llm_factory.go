package engine

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	llmProviderMock   = "mock"
	llmProviderOpenAI = "openai"
)

// NewLLMClientFromEnv creates an LLM client from environment variables.
//
// Supported vars:
//   - AGENTFORGE_LLM_PROVIDER: mock (default) | openai
//   - AGENTFORGE_LLM_MOCK_STEPS: int, used for mock provider
//   - OPENAI_API_KEY: required for openai provider
//   - OPENAI_BASE_URL: optional, defaults to https://api.openai.com/v1
//   - AGENTFORGE_LLM_MODEL: optional default model, defaults to gpt-4o-mini
//   - AGENTFORGE_LLM_TIMEOUT_SECONDS: optional, defaults to 60
func NewLLMClientFromEnv() (LLMClient, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("AGENTFORGE_LLM_PROVIDER")))
	if provider == "" {
		provider = llmProviderMock
	}

	switch provider {
	case llmProviderMock:
		maxSteps := 3
		if raw := strings.TrimSpace(os.Getenv("AGENTFORGE_LLM_MOCK_STEPS")); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				return nil, fmt.Errorf("engine: invalid AGENTFORGE_LLM_MOCK_STEPS: %w", err)
			}
			maxSteps = n
		}
		return NewMockLLMClient(maxSteps), nil

	case llmProviderOpenAI:
		timeout := 60 * time.Second
		if raw := strings.TrimSpace(os.Getenv("AGENTFORGE_LLM_TIMEOUT_SECONDS")); raw != "" {
			sec, err := strconv.Atoi(raw)
			if err != nil {
				return nil, fmt.Errorf("engine: invalid AGENTFORGE_LLM_TIMEOUT_SECONDS: %w", err)
			}
			if sec > 0 {
				timeout = time.Duration(sec) * time.Second
			}
		}
		return NewOpenAICompatibleClient(OpenAICompatibleClientConfig{
			APIKey:       os.Getenv("OPENAI_API_KEY"),
			BaseURL:      os.Getenv("OPENAI_BASE_URL"),
			DefaultModel: os.Getenv("AGENTFORGE_LLM_MODEL"),
			Timeout:      timeout,
		})

	default:
		return nil, fmt.Errorf("engine: unsupported AGENTFORGE_LLM_PROVIDER %q", provider)
	}
}
