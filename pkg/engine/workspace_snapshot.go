package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"github.com/agentforge/agentforge/pkg/artifact"
	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/workspace"
)

var ErrWorkspaceSnapshotIntegrity = errors.New("engine: workspace snapshot integrity check failed")

// WorkspaceS3Key returns the canonical S3 key for a workspace snapshot.
func WorkspaceS3Key(tenantID, taskID, runID string, stepIndex int) string {
	return fmt.Sprintf("workspaces/%s/%s/%s/step_%08d.tar.gz", tenantID, taskID, runID, stepIndex)
}

// SnapshotWorkspace creates a tar.gz of the workspace and stores it.
func SnapshotWorkspace(ctx context.Context, ws workspace.Manager, store artifact.Store, tenantID, taskID, runID string, stepIndex int) (*model.ArtifactRef, error) {
	rc, err := ws.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot workspace: %w", err)
	}
	defer func() { _ = rc.Close() }()

	key := WorkspaceS3Key(tenantID, taskID, runID, stepIndex)
	sha, size, err := store.Put(ctx, key, rc)
	if err != nil {
		return nil, fmt.Errorf("put workspace snapshot: %w", err)
	}

	return &model.ArtifactRef{
		S3Key:  key,
		SHA256: sha,
		Size:   size,
	}, nil
}

// RestoreWorkspace downloads and extracts a workspace snapshot.
func RestoreWorkspace(ctx context.Context, ws workspace.Manager, store artifact.Store, ref *model.ArtifactRef) error {
	rc, err := store.Get(ctx, ref.S3Key)
	if err != nil {
		return fmt.Errorf("get workspace snapshot: %w", err)
	}
	defer func() { _ = rc.Close() }()

	raw, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("read workspace snapshot: %w", err)
	}
	if ref.Size > 0 && int64(len(raw)) != ref.Size {
		return fmt.Errorf(
			"%w: workspace snapshot size mismatch (expected=%d got=%d)",
			ErrWorkspaceSnapshotIntegrity,
			ref.Size,
			len(raw),
		)
	}
	if ref.SHA256 != "" {
		sum := sha256.Sum256(raw)
		actual := fmt.Sprintf("%x", sum)
		if actual != ref.SHA256 {
			return fmt.Errorf(
				"%w: workspace snapshot sha256 mismatch (expected=%s got=%s)",
				ErrWorkspaceSnapshotIntegrity,
				ref.SHA256,
				actual,
			)
		}
	}

	if err := ws.Restore(ctx, bytes.NewReader(raw)); err != nil {
		return fmt.Errorf("restore workspace: %w", err)
	}
	return nil
}
