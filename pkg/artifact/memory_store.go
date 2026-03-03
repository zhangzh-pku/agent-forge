package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"sync"
)

// MemoryStore is an in-memory implementation of Store for testing and local dev.
type MemoryStore struct {
	mu    sync.RWMutex
	blobs map[string][]byte
}

// NewMemoryStore creates a new in-memory artifact store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{blobs: make(map[string][]byte)}
}

func (s *MemoryStore) Put(_ context.Context, key string, r io.Reader) (string, int64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", 0, fmt.Errorf("memory store put: %w", err)
	}
	h := sha256.Sum256(data)
	hex := fmt.Sprintf("%x", h)

	s.mu.Lock()
	s.blobs[key] = data
	s.mu.Unlock()

	return hex, int64(len(data)), nil
}

func (s *MemoryStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.RLock()
	data, ok := s.blobs[key]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *MemoryStore) Exists(_ context.Context, key string) (bool, error) {
	s.mu.RLock()
	_, ok := s.blobs[key]
	s.mu.RUnlock()
	return ok, nil
}

func (s *MemoryStore) PresignedURL(_ context.Context, _ string) (string, error) {
	return "", nil // not supported in memory store
}
