package promptpolicy

import "testing"

func TestLoadFromEnvDefaults(t *testing.T) {
	t.Setenv(EnvMaxChars, "")
	t.Setenv(EnvDenyList, "")

	p := LoadFromEnv()
	if p.MaxChars != DefaultMaxChars {
		t.Fatalf("expected default max chars %d, got %d", DefaultMaxChars, p.MaxChars)
	}
	if len(p.DenyList) != 0 {
		t.Fatalf("expected empty deny list, got %v", p.DenyList)
	}
}

func TestLoadFromEnvParsesValues(t *testing.T) {
	t.Setenv(EnvMaxChars, "42")
	t.Setenv(EnvDenyList, "token, secret ,token")

	p := LoadFromEnv()
	if p.MaxChars != 42 {
		t.Fatalf("expected max chars 42, got %d", p.MaxChars)
	}
	if len(p.DenyList) != 2 {
		t.Fatalf("expected deduped deny list size 2, got %d (%v)", len(p.DenyList), p.DenyList)
	}
}

func TestValidate(t *testing.T) {
	p := Policy{
		MaxChars: 64,
		DenyList: []string{"forbidden"},
	}

	if err := Validate("short", p); err != nil {
		t.Fatalf("expected short prompt to pass, got %v", err)
	}
	if err := Validate("this prompt is definitely longer than eight chars", Policy{MaxChars: 8}); err != ErrPromptTooLong {
		t.Fatalf("expected ErrPromptTooLong, got %v", err)
	}
	if err := Validate("contains forbidden marker", p); err != ErrPromptDenied {
		t.Fatalf("expected ErrPromptDenied, got %v", err)
	}
}
