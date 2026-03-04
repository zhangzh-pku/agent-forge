package engine

import (
	"context"
	"errors"
	"testing"

	artstore "github.com/agentforge/agentforge/pkg/artifact"
	"github.com/agentforge/agentforge/pkg/workspace"
)

func TestRestoreWorkspaceRejectsSHA256Mismatch(t *testing.T) {
	ctx := context.Background()
	store := artstore.NewMemoryStore()

	src, err := workspace.NewLocalManager(workspace.Config{
		Root:            t.TempDir(),
		MaxTotalBytes:   1024 * 1024,
		MaxArchiveBytes: 2 * 1024 * 1024,
		MaxFileCount:    100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Write(ctx, "a.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}

	ref, err := SnapshotWorkspace(ctx, src, store, "t1", "task1", "run1", 0)
	if err != nil {
		t.Fatal(err)
	}

	dst, err := workspace.NewLocalManager(workspace.Config{
		Root:            t.TempDir(),
		MaxTotalBytes:   1024 * 1024,
		MaxArchiveBytes: 2 * 1024 * 1024,
		MaxFileCount:    100,
	})
	if err != nil {
		t.Fatal(err)
	}

	bad := *ref
	bad.SHA256 = "deadbeef"
	err = RestoreWorkspace(ctx, dst, store, &bad)
	if !errors.Is(err, ErrWorkspaceSnapshotIntegrity) {
		t.Fatalf("expected ErrWorkspaceSnapshotIntegrity, got %v", err)
	}
}

func TestRestoreWorkspaceRejectsSizeMismatch(t *testing.T) {
	ctx := context.Background()
	store := artstore.NewMemoryStore()

	src, err := workspace.NewLocalManager(workspace.Config{
		Root:            t.TempDir(),
		MaxTotalBytes:   1024 * 1024,
		MaxArchiveBytes: 2 * 1024 * 1024,
		MaxFileCount:    100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Write(ctx, "b.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}

	ref, err := SnapshotWorkspace(ctx, src, store, "t1", "task1", "run1", 1)
	if err != nil {
		t.Fatal(err)
	}

	dst, err := workspace.NewLocalManager(workspace.Config{
		Root:            t.TempDir(),
		MaxTotalBytes:   1024 * 1024,
		MaxArchiveBytes: 2 * 1024 * 1024,
		MaxFileCount:    100,
	})
	if err != nil {
		t.Fatal(err)
	}

	bad := *ref
	bad.Size = ref.Size + 1
	err = RestoreWorkspace(ctx, dst, store, &bad)
	if !errors.Is(err, ErrWorkspaceSnapshotIntegrity) {
		t.Fatalf("expected ErrWorkspaceSnapshotIntegrity, got %v", err)
	}
}
