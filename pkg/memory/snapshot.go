// Package memory handles agent memory serialization and checkpointing.
package memory

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/agentforge/agentforge/pkg/artifact"
	"github.com/agentforge/agentforge/pkg/model"
)

// Snapshotter saves and loads memory snapshots via an artifact store.
type Snapshotter struct {
	store artifact.Store
}

// NewSnapshotter creates a new memory snapshotter.
func NewSnapshotter(store artifact.Store) *Snapshotter {
	return &Snapshotter{store: store}
}

// S3Key returns the canonical S3 key for a memory snapshot.
func S3Key(tenantID, taskID, runID string, stepIndex int) string {
	return fmt.Sprintf("memory/%s/%s/%s/step_%08d.json.gz", tenantID, taskID, runID, stepIndex)
}

// Save serializes a MemorySnapshot to gzipped JSON and writes it to the artifact store.
func (s *Snapshotter) Save(ctx context.Context, tenantID, taskID string, snap *model.MemorySnapshot) (*model.ArtifactRef, error) {
	data, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("memory: marshal: %w", err)
	}

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		return nil, fmt.Errorf("memory: gzip write: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("memory: gzip close: %w", err)
	}

	key := S3Key(tenantID, taskID, snap.RunID, snap.StepIndex)
	sha, size, err := s.store.Put(ctx, key, &buf)
	if err != nil {
		return nil, fmt.Errorf("memory: put: %w", err)
	}

	return &model.ArtifactRef{
		S3Key:  key,
		SHA256: sha,
		Size:   size,
	}, nil
}

// Load retrieves and deserializes a memory snapshot.
func (s *Snapshotter) Load(ctx context.Context, ref *model.ArtifactRef) (*model.MemorySnapshot, error) {
	rc, err := s.store.Get(ctx, ref.S3Key)
	if err != nil {
		return nil, fmt.Errorf("memory: get: %w", err)
	}
	defer rc.Close()

	gr, err := gzip.NewReader(rc)
	if err != nil {
		return nil, fmt.Errorf("memory: gzip reader: %w", err)
	}
	defer gr.Close()

	data, err := io.ReadAll(gr)
	if err != nil {
		return nil, fmt.Errorf("memory: read: %w", err)
	}

	var snap model.MemorySnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("memory: unmarshal: %w", err)
	}
	return &snap, nil
}
