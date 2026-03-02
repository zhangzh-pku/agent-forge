// Package artifact defines the interface for storing large objects (S3).
package artifact

import (
	"context"
	"io"
)

// Store is the interface for persisting and retrieving binary artifacts.
type Store interface {
	// Put writes data to the given key. Returns the SHA256 hex digest and size.
	Put(ctx context.Context, key string, r io.Reader) (sha256hex string, size int64, err error)
	// Get retrieves data from the given key.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Exists checks whether a key exists.
	Exists(ctx context.Context, key string) (bool, error)
	// PresignedURL generates a time-limited download URL (for S3). Returns empty string for in-memory.
	PresignedURL(ctx context.Context, key string) (string, error)
}
