package memory

import (
	"context"
	"testing"

	"github.com/agentforge/agentforge/pkg/artifact"
	"github.com/agentforge/agentforge/pkg/model"
)

func TestSaveAndLoad(t *testing.T) {
	store := artifact.NewMemoryStore()
	snapshotter := NewSnapshotter(store)
	ctx := context.Background()

	original := &model.MemorySnapshot{
		RunID:     "run_1",
		StepIndex: 0,
		Messages: []model.MemoryMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi there"},
		},
		Scratchpad: "thinking...",
		ToolState: map[string]interface{}{
			"counter": float64(42),
		},
	}

	ref, err := snapshotter.Save(ctx, "tnt_1", "task_1", original)
	if err != nil {
		t.Fatal(err)
	}

	if ref.S3Key == "" {
		t.Fatal("expected non-empty S3 key")
	}
	if ref.SHA256 == "" {
		t.Fatal("expected non-empty SHA256")
	}
	if ref.Size == 0 {
		t.Fatal("expected non-zero size")
	}

	loaded, err := snapshotter.Load(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.RunID != original.RunID {
		t.Fatalf("RunID: got %q, want %q", loaded.RunID, original.RunID)
	}
	if loaded.StepIndex != original.StepIndex {
		t.Fatalf("StepIndex: got %d, want %d", loaded.StepIndex, original.StepIndex)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(loaded.Messages))
	}
	if loaded.Messages[0].Content != "hello" {
		t.Fatalf("message[0] content: got %q", loaded.Messages[0].Content)
	}
	if loaded.Scratchpad != "thinking..." {
		t.Fatalf("Scratchpad: got %q", loaded.Scratchpad)
	}
}

func TestS3Key(t *testing.T) {
	key := S3Key("tnt_1", "task_1", "run_1", 42)
	expected := "memory/tnt_1/task_1/run_1/step_00000042.json.gz"
	if key != expected {
		t.Fatalf("got %q, want %q", key, expected)
	}
}
