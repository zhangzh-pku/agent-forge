package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

const (
	llmProviderMock   = "mock"
	llmProviderOpenAI = "openai"
)

var fetchSecretString = func(ctx context.Context, secretID string) (string, error) {
	cfg, err := awscfg.LoadDefaultConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("load aws config: %w", err)
	}
	client := secretsmanager.NewFromConfig(cfg)
	out, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(secretID),
	})
	if err != nil {
		return "", fmt.Errorf("get secret value: %w", err)
	}
	if out.SecretString == nil {
		return "", fmt.Errorf("secret %q has empty SecretString", secretID)
	}
	value := strings.TrimSpace(*out.SecretString)
	if value == "" {
		return "", fmt.Errorf("secret %q has blank SecretString", secretID)
	}
	return value, nil
}

// NewLLMClientFromEnv creates an LLM client from environment variables.
//
// Supported vars:
//   - AGENTFORGE_LLM_PROVIDER: mock (default) | openai
//   - AGENTFORGE_LLM_MOCK_STEPS: int, used for mock provider
//   - OPENAI_API_KEY: optional direct key for openai provider
//   - OPENAI_API_KEY_SECRET_ARN: optional Secrets Manager ARN/name for key bootstrap
//   - OPENAI_API_KEY_SECRET_FIELD: optional JSON field when secret string is JSON
//   - OPENAI_BASE_URL: optional, defaults to https://api.openai.com/v1
//   - AGENTFORGE_LLM_MODEL: optional default model, defaults to gpt-4o-mini
//   - AGENTFORGE_LLM_TIMEOUT_SECONDS: optional, defaults to 60
func NewLLMClientFromEnv() (LLMClient, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("AGENTFORGE_LLM_PROVIDER")))
	if provider == "" {
		provider = llmProviderMock
	}

	primary, err := newLLMClientForProvider(provider)
	if err != nil {
		return nil, err
	}

	clients := map[string]LLMClient{
		provider: primary,
	}

	var fallbackProviders []string
	if raw := strings.TrimSpace(os.Getenv("AGENTFORGE_LLM_FALLBACK_PROVIDERS")); raw != "" {
		parts := strings.Split(raw, ",")
		for _, part := range parts {
			p := strings.ToLower(strings.TrimSpace(part))
			if p == "" || p == provider {
				continue
			}
			if _, exists := clients[p]; exists {
				continue
			}
			client, err := newLLMClientForProvider(p)
			if err != nil {
				return nil, fmt.Errorf("engine: initialize fallback provider %q: %w", p, err)
			}
			clients[p] = client
			fallbackProviders = append(fallbackProviders, p)
		}
	}

	// Always wrap with router to enable task-aware model selection and policy modes.
	router := NewModelRouter(RouterConfig{
		DefaultMode:       strings.TrimSpace(os.Getenv("AGENTFORGE_LLM_ROUTING_DEFAULT_MODE")),
		FallbackProviders: fallbackProviders,
	})
	return NewRoutedLLMClient(provider, clients, router), nil
}

func newLLMClientForProvider(provider string) (LLMClient, error) {
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
		apiKey, err := resolveOpenAIAPIKey(context.Background())
		if err != nil {
			return nil, err
		}
		return NewOpenAICompatibleClient(OpenAICompatibleClientConfig{
			APIKey:       apiKey,
			BaseURL:      os.Getenv("OPENAI_BASE_URL"),
			DefaultModel: os.Getenv("AGENTFORGE_LLM_MODEL"),
			Timeout:      timeout,
		})

	default:
		return nil, fmt.Errorf("engine: unsupported AGENTFORGE_LLM_PROVIDER %q", provider)
	}
}

func resolveOpenAIAPIKey(ctx context.Context) (string, error) {
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		return key, nil
	}

	secretID := strings.TrimSpace(os.Getenv("OPENAI_API_KEY_SECRET_ARN"))
	if secretID == "" {
		return "", nil
	}

	secretValue, err := fetchSecretString(ctx, secretID)
	if err != nil {
		return "", fmt.Errorf("engine: load OPENAI_API_KEY from Secrets Manager: %w", err)
	}
	field := strings.TrimSpace(os.Getenv("OPENAI_API_KEY_SECRET_FIELD"))
	apiKey, err := extractAPIKeyFromSecretValue(secretValue, field)
	if err != nil {
		return "", fmt.Errorf("engine: parse OPENAI_API_KEY secret: %w", err)
	}
	return apiKey, nil
}

func extractAPIKeyFromSecretValue(secretValue, field string) (string, error) {
	trimmed := strings.TrimSpace(secretValue)
	if trimmed == "" {
		return "", fmt.Errorf("secret value is empty")
	}

	if field != "" {
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
			return "", fmt.Errorf("OPENAI_API_KEY_SECRET_FIELD=%q requires JSON secret: %w", field, err)
		}
		val, ok := obj[field]
		if !ok {
			return "", fmt.Errorf("field %q not found in JSON secret", field)
		}
		str, ok := val.(string)
		if !ok || strings.TrimSpace(str) == "" {
			return "", fmt.Errorf("field %q in JSON secret is not a non-empty string", field)
		}
		return strings.TrimSpace(str), nil
	}

	if strings.HasPrefix(trimmed, "{") {
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
			for _, key := range []string{"OPENAI_API_KEY", "api_key", "apiKey", "key"} {
				if val, ok := obj[key]; ok {
					if str, ok := val.(string); ok && strings.TrimSpace(str) != "" {
						return strings.TrimSpace(str), nil
					}
				}
			}
			return "", fmt.Errorf("json secret missing api key field; set OPENAI_API_KEY_SECRET_FIELD to choose one")
		}
	}

	return trimmed, nil
}
