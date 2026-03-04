// Package workspace implements the virtual filesystem for agent execution.
package workspace

import (
	"context"
	"io"
)

// Config holds workspace limits and settings.
type Config struct {
	// Root is the base directory for the workspace.
	Root string
	// MaxTotalBytes is the maximum total size of all files (default 50MB).
	MaxTotalBytes int64
	// MaxArchiveBytes is the maximum size of snapshot archives accepted by Restore.
	// This limits compressed input size before extraction.
	MaxArchiveBytes int64
	// MaxFileCount is the maximum number of files (default 2000).
	MaxFileCount int
}

// DefaultConfig returns sensible defaults.
func DefaultConfig(runID string) Config {
	return Config{
		Root:            "/tmp/agentforge/" + runID,
		MaxTotalBytes:   50 * 1024 * 1024,
		MaxArchiveBytes: 60 * 1024 * 1024,
		MaxFileCount:    2000,
	}
}

// Manager provides safe file operations within a workspace root.
type Manager interface {
	// Write creates or overwrites a file. Enforces path safety and quotas.
	Write(ctx context.Context, path string, content []byte) error
	// Read returns file contents.
	Read(ctx context.Context, path string) ([]byte, error)
	// List returns entries in a directory.
	List(ctx context.Context, dir string) ([]FileInfo, error)
	// Stat returns metadata for a file.
	Stat(ctx context.Context, path string) (*FileInfo, error)
	// Delete removes a file.
	Delete(ctx context.Context, path string) error
	// Snapshot creates a tar.gz archive of the entire workspace.
	Snapshot(ctx context.Context) (io.ReadCloser, error)
	// Restore extracts a tar.gz archive into the workspace, replacing contents.
	Restore(ctx context.Context, r io.Reader) error
	// Root returns the workspace root path.
	Root() string
	// Cleanup removes the workspace directory.
	Cleanup() error
	// Usage returns current total bytes and file count.
	Usage() (totalBytes int64, fileCount int)
}

// FileInfo describes a file or directory entry.
type FileInfo struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}
