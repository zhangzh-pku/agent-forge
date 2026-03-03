package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var (
	envAssignmentPattern = regexp.MustCompile(`^\s*#?\s*([A-Z][A-Z0-9_]+)\s*=`)
	docEnvVarPattern     = regexp.MustCompile("`([A-Z][A-Z0-9_]+)`")
)

func TestEnvExampleAndConfigurationDocStayInSync(t *testing.T) {
	t.Helper()

	envExampleVars := parseEnvExampleVars(t, filepath.Join("..", "..", ".env.example"))
	docVars := parseDocVars(t, filepath.Join("..", "..", "docs", "configuration.md"))

	for envVar := range envExampleVars {
		if _, ok := docVars[envVar]; !ok {
			t.Fatalf("variable %q exists in .env.example but is missing from docs/configuration.md", envVar)
		}
	}
	for docVar := range docVars {
		if _, ok := envExampleVars[docVar]; !ok {
			t.Fatalf("variable %q exists in docs/configuration.md but is missing from .env.example", docVar)
		}
	}
}

func parseEnvExampleVars(t *testing.T, path string) map[string]struct{} {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := make(map[string]struct{})
	lines := regexp.MustCompile(`\r?\n`).Split(string(data), -1)
	for _, line := range lines {
		m := envAssignmentPattern.FindStringSubmatch(line)
		if len(m) == 2 {
			out[m[1]] = struct{}{}
		}
	}
	return out
}

func parseDocVars(t *testing.T, path string) map[string]struct{} {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := make(map[string]struct{})
	matches := docEnvVarPattern.FindAllStringSubmatch(string(data), -1)
	for _, m := range matches {
		if len(m) == 2 {
			out[m[1]] = struct{}{}
		}
	}
	return out
}
