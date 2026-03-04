package util

import (
	"strings"
	"testing"
)

func TestNewIDPrefix(t *testing.T) {
	id := NewID("task_")
	if !strings.HasPrefix(id, "task_") {
		t.Fatalf("expected prefix task_, got %s", id)
	}
}

func TestNewIDLength(t *testing.T) {
	id := NewID("t_")
	// prefix "t_" + 24 hex chars (12 bytes * 2) = 26 total
	if len(id) != 26 {
		t.Fatalf("expected length 26, got %d for %q", len(id), id)
	}
}

func TestNewIDUnique(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		id := NewID("x_")
		if seen[id] {
			t.Fatalf("duplicate ID generated: %s", id)
		}
		seen[id] = true
	}
}
