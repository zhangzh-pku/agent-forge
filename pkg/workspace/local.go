package workspace

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	ErrPathTraversal = errors.New("workspace: path traversal detected")
	ErrQuotaBytes    = errors.New("workspace: total bytes quota exceeded")
	ErrQuotaFiles    = errors.New("workspace: file count quota exceeded")
)

// LocalManager implements Manager using the local filesystem.
type LocalManager struct {
	cfg        Config
	mu         sync.RWMutex
	totalBytes int64
	fileCount  int
}

// NewLocalManager creates a workspace rooted at cfg.Root.
func NewLocalManager(cfg Config) (*LocalManager, error) {
	absRoot, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("workspace: resolve root: %w", err)
	}
	cfg.Root = absRoot
	if err := os.MkdirAll(cfg.Root, 0o755); err != nil {
		return nil, fmt.Errorf("workspace: create root: %w", err)
	}
	m := &LocalManager{cfg: cfg}
	// Scan existing files for quota tracking.
	_ = m.recount()
	return m, nil
}

func (m *LocalManager) Root() string { return m.cfg.Root }

// safePath resolves and validates that path stays within the workspace root.
func (m *LocalManager) safePath(rel string) (string, error) {
	cleaned := filepath.Clean(rel)
	if filepath.IsAbs(cleaned) {
		// Allow if it's already under root.
		if strings.HasPrefix(cleaned, m.cfg.Root+string(os.PathSeparator)) || cleaned == m.cfg.Root {
			return cleaned, nil
		}
		return "", ErrPathTraversal
	}
	abs := filepath.Join(m.cfg.Root, cleaned)
	// Double-check after join.
	if !strings.HasPrefix(abs, m.cfg.Root+string(os.PathSeparator)) && abs != m.cfg.Root {
		return "", ErrPathTraversal
	}
	return abs, nil
}

func (m *LocalManager) Write(_ context.Context, path string, content []byte) error {
	abs, err := m.safePath(path)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if file exists (update vs create).
	var oldSize int64
	isNew := true
	if info, err := os.Stat(abs); err == nil {
		oldSize = info.Size()
		isNew = false
	}

	newTotal := m.totalBytes - oldSize + int64(len(content))
	if newTotal > m.cfg.MaxTotalBytes {
		return ErrQuotaBytes
	}
	if isNew && m.fileCount+1 > m.cfg.MaxFileCount {
		return ErrQuotaFiles
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("workspace: mkdir: %w", err)
	}
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		return fmt.Errorf("workspace: write: %w", err)
	}

	m.totalBytes = newTotal
	if isNew {
		m.fileCount++
	}
	return nil
}

func (m *LocalManager) Read(_ context.Context, path string) ([]byte, error) {
	abs, err := m.safePath(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("workspace: read: %w", err)
	}
	return data, nil
}

func (m *LocalManager) List(_ context.Context, dir string) ([]FileInfo, error) {
	abs, err := m.safePath(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("workspace: list: %w", err)
	}
	var result []FileInfo
	for _, e := range entries {
		info, _ := e.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}
		result = append(result, FileInfo{
			Name:  e.Name(),
			Path:  filepath.Join(dir, e.Name()),
			IsDir: e.IsDir(),
			Size:  size,
		})
	}
	return result, nil
}

func (m *LocalManager) Stat(_ context.Context, path string) (*FileInfo, error) {
	abs, err := m.safePath(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("workspace: stat: %w", err)
	}
	return &FileInfo{
		Name:  info.Name(),
		Path:  path,
		IsDir: info.IsDir(),
		Size:  info.Size(),
	}, nil
}

func (m *LocalManager) Delete(_ context.Context, path string) error {
	abs, err := m.safePath(path)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("workspace: delete: %w", err)
	}
	if err := os.Remove(abs); err != nil {
		return fmt.Errorf("workspace: delete: %w", err)
	}
	if !info.IsDir() {
		m.totalBytes -= info.Size()
		m.fileCount--
	}
	return nil
}

// Snapshot creates a tar.gz of the workspace.
func (m *LocalManager) Snapshot(_ context.Context) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	go func() {
		gw := gzip.NewWriter(pw)
		tw := tar.NewWriter(gw)
		err := filepath.Walk(m.cfg.Root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(m.cfg.Root, path)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}
			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			header.Name = rel
			if err := tw.WriteHeader(header); err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		})
		tw.Close()
		gw.Close()
		pw.CloseWithError(err)
	}()
	return pr, nil
}

// Restore extracts a tar.gz into the workspace, with path traversal protection.
func (m *LocalManager) Restore(_ context.Context, r io.Reader) error {
	// Clear workspace first.
	if err := os.RemoveAll(m.cfg.Root); err != nil {
		return fmt.Errorf("workspace: clear for restore: %w", err)
	}
	if err := os.MkdirAll(m.cfg.Root, 0o755); err != nil {
		return fmt.Errorf("workspace: recreate root: %w", err)
	}

	gr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("workspace: gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	var totalBytes int64
	var fileCount int
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("workspace: tar next: %w", err)
		}

		// Path traversal protection: ensure entry stays within root.
		cleanName := filepath.Clean(header.Name)
		if filepath.IsAbs(cleanName) {
			return fmt.Errorf("%w: %s", ErrPathTraversal, header.Name)
		}
		// Reject any path containing ".." components (e.g., "foo/../../etc/passwd").
		for _, part := range strings.Split(cleanName, string(os.PathSeparator)) {
			if part == ".." {
				return fmt.Errorf("%w: %s", ErrPathTraversal, header.Name)
			}
		}
		target := filepath.Join(m.cfg.Root, cleanName)
		if !strings.HasPrefix(target, m.cfg.Root+string(os.PathSeparator)) && target != m.cfg.Root {
			return fmt.Errorf("%w: resolved %s", ErrPathTraversal, target)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("workspace: mkdir: %w", err)
			}
		case tar.TypeReg:
			// Enforce quotas during extraction.
			fileCount++
			if fileCount > m.cfg.MaxFileCount {
				return fmt.Errorf("%w: archive contains more than %d files", ErrQuotaFiles, m.cfg.MaxFileCount)
			}
			totalBytes += header.Size
			if totalBytes > m.cfg.MaxTotalBytes {
				return fmt.Errorf("%w: archive exceeds %d bytes", ErrQuotaBytes, m.cfg.MaxTotalBytes)
			}

			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("workspace: create file: %w", err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("workspace: extract file: %w", err)
			}
			f.Close()
		default:
			// Skip symlinks and other types for security.
			continue
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recount()
}

func (m *LocalManager) Cleanup() error {
	return os.RemoveAll(m.cfg.Root)
}

func (m *LocalManager) Usage() (int64, int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.totalBytes, m.fileCount
}

// recount walks the workspace to recalculate quota tracking. Must hold mu or be called at init.
func (m *LocalManager) recount() error {
	var total int64
	var count int
	err := filepath.Walk(m.cfg.Root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
			count++
		}
		return nil
	})
	m.totalBytes = total
	m.fileCount = count
	return err
}
