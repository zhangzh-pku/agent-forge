package promptpolicy

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	EnvMaxChars = "AGENTFORGE_PROMPT_MAX_CHARS"
	EnvDenyList = "AGENTFORGE_PROMPT_DENYLIST"

	DefaultMaxChars = 16000
)

var (
	ErrPromptTooLong = errors.New("prompt exceeds max length")
	ErrPromptDenied  = errors.New("prompt contains denied term")
)

type Policy struct {
	MaxChars int
	DenyList []string
}

func LoadFromEnv() Policy {
	maxChars := DefaultMaxChars
	if raw := strings.TrimSpace(os.Getenv(EnvMaxChars)); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			maxChars = parsed
		}
	}

	var denyList []string
	if raw := os.Getenv(EnvDenyList); raw != "" {
		seen := make(map[string]struct{})
		for _, token := range strings.Split(raw, ",") {
			normalized := strings.ToLower(strings.TrimSpace(token))
			if normalized == "" {
				continue
			}
			if _, exists := seen[normalized]; exists {
				continue
			}
			seen[normalized] = struct{}{}
			denyList = append(denyList, normalized)
		}
	}

	return Policy{
		MaxChars: maxChars,
		DenyList: denyList,
	}
}

func Validate(prompt string, p Policy) error {
	if p.MaxChars > 0 && utf8.RuneCountInString(prompt) > p.MaxChars {
		return ErrPromptTooLong
	}
	if len(p.DenyList) == 0 {
		return nil
	}

	normalizedPrompt := strings.ToLower(prompt)
	for _, token := range p.DenyList {
		if strings.Contains(normalizedPrompt, token) {
			return ErrPromptDenied
		}
	}
	return nil
}
