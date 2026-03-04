package workspace

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
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
	if cfg.MaxArchiveBytes <= 0 {
		// Keep a bounded compressed-input limit for restore to mitigate tar bombs.
		cfg.MaxArchiveBytes = cfg.MaxTotalBytes + 10*1024*1024
	}
	if err := os.MkdirAll(cfg.Root, 0o700); err != nil {
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
		return "", ErrPathTraversal
	}
	abs := filepath.Join(m.cfg.Root, cleaned)
	// Double-check after join.
	if !strings.HasPrefix(abs, m.cfg.Root+string(os.PathSeparator)) && abs != m.cfg.Root {
		return "", ErrPathTraversal
	}
	// Defense in depth: reject any existing symlink path components.
	if err := m.ensureNoSymlink(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// ensureNoSymlink rejects paths that traverse any symlink component currently
// present under the workspace root.
func (m *LocalManager) ensureNoSymlink(abs string) error {
	if abs == m.cfg.Root {
		return nil
	}
	rel, err := filepath.Rel(m.cfg.Root, abs)
	if err != nil {
		return ErrPathTraversal
	}
	cur := m.cfg.Root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		info, statErr := os.Lstat(cur)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("workspace: lstat: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrPathTraversal
		}
	}
	return nil
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

	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return fmt.Errorf("workspace: mkdir: %w", err)
	}
	if err := os.WriteFile(abs, content, 0o600); err != nil {
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, err := os.ReadFile(abs) // #nosec G304 -- abs is validated by safePath and symlink checks.
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
	m.mu.RLock()
	defer m.mu.RUnlock()
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
	m.mu.RLock()
	defer m.mu.RUnlock()
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
	m.mu.RLock()
	pr, pw := io.Pipe()
	go func() {
		defer m.mu.RUnlock()
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
			f, err := os.Open(path) // #nosec G304 -- path is emitted by filepath.Walk under m.cfg.Root.
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			return nil
		})
		if closeErr := tw.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
		if closeErr := gw.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
		pw.CloseWithError(err)
	}()
	return pr, nil
}

// Restore extracts a tar.gz into the workspace, with path traversal protection.
func (m *LocalManager) Restore(_ context.Context, r io.Reader) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	tmpRoot, err := os.MkdirTemp(filepath.Dir(m.cfg.Root), ".agentforge-restore-*")
	if err != nil {
		return fmt.Errorf("workspace: create restore temp root: %w", err)
	}
	// #nosec G302 -- directories need execute bit; 0700 is least-privileged usable mode.
	if err := os.Chmod(tmpRoot, 0o700); err != nil {
		_ = os.RemoveAll(tmpRoot)
		return fmt.Errorf("workspace: chmod restore temp root: %w", err)
	}
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			_ = os.RemoveAll(tmpRoot)
		}
	}()

	limited := &io.LimitedReader{R: r, N: m.cfg.MaxArchiveBytes + 1}
	if err := m.extractTarGzIntoRoot(limited, tmpRoot); err != nil {
		if limited.N <= 0 {
			return fmt.Errorf("%w: archive exceeds %d bytes", ErrQuotaBytes, m.cfg.MaxArchiveBytes)
		}
		return err
	}
	if limited.N <= 0 {
		return fmt.Errorf("%w: archive exceeds %d bytes", ErrQuotaBytes, m.cfg.MaxArchiveBytes)
	}

	backupRoot := m.cfg.Root + ".restore-backup"
	_ = os.RemoveAll(backupRoot)

	hadRoot := true
	if _, err := os.Stat(m.cfg.Root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			hadRoot = false
		} else {
			return fmt.Errorf("workspace: stat root: %w", err)
		}
	}
	if hadRoot {
		if err := os.Rename(m.cfg.Root, backupRoot); err != nil {
			return fmt.Errorf("workspace: stage previous root: %w", err)
		}
	}
	if err := os.Rename(tmpRoot, m.cfg.Root); err != nil {
		if hadRoot {
			if rollbackErr := os.Rename(backupRoot, m.cfg.Root); rollbackErr != nil {
				return fmt.Errorf("workspace: activate restore: %w (rollback failed: %v)", err, rollbackErr)
			}
		}
		return fmt.Errorf("workspace: activate restore: %w", err)
	}
	cleanupTemp = false
	if hadRoot {
		_ = os.RemoveAll(backupRoot)
	}

	return m.recount()
}

func (m *LocalManager) extractTarGzIntoRoot(r io.Reader, root string) error {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("workspace: gzip reader: %w", err)
	}
	defer func() { _ = gr.Close() }()

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
		target := filepath.Join(root, cleanName)
		if !strings.HasPrefix(target, root+string(os.PathSeparator)) && target != root {
			return fmt.Errorf("%w: resolved %s", ErrPathTraversal, target)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return fmt.Errorf("workspace: mkdir: %w", err)
			}
		case tar.TypeReg:
			if header.Size < 0 {
				return fmt.Errorf("workspace: invalid negative file size in tar header: %d", header.Size)
			}
			// Enforce quotas during extraction.
			fileCount++
			if fileCount > m.cfg.MaxFileCount {
				return fmt.Errorf("%w: archive contains more than %d files", ErrQuotaFiles, m.cfg.MaxFileCount)
			}
			if header.Size > m.cfg.MaxTotalBytes-totalBytes {
				return fmt.Errorf("%w: archive exceeds %d bytes", ErrQuotaBytes, m.cfg.MaxTotalBytes)
			}
			totalBytes += header.Size

			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			fileMode := sanitizeArchiveFileMode(header.Mode)
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fileMode) // #nosec G304 -- target is validated to stay inside restore root.
			if err != nil {
				return fmt.Errorf("workspace: create file: %w", err)
			}
			written, err := io.Copy(f, io.LimitReader(tr, header.Size+1))
			if err != nil {
				if closeErr := f.Close(); closeErr != nil {
					return fmt.Errorf("workspace: extract file: %w (close: %v)", err, closeErr)
				}
				return fmt.Errorf("workspace: extract file: %w", err)
			}
			if written != header.Size {
				if closeErr := f.Close(); closeErr != nil {
					return fmt.Errorf("workspace: extract file size mismatch (%d != %d) (close: %v)", written, header.Size, closeErr)
				}
				return fmt.Errorf("workspace: extract file size mismatch (%d != %d)", written, header.Size)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("workspace: close file: %w", err)
			}
		default:
			// Skip symlinks and other types for security.
			continue
		}
	}
	return nil
}

func (m *LocalManager) Cleanup() error {
	return os.RemoveAll(m.cfg.Root)
}

func sanitizeArchiveFileMode(mode int64) os.FileMode {
	if mode < 0 {
		mode = 0
	}
	if mode > int64(math.MaxUint32) {
		mode = int64(math.MaxUint32)
	}
	// Workspace files should remain private to the runtime identity.
	return os.FileMode(uint32(mode)) & 0o600 // #nosec G115 -- mode is clamped to uint32 bounds above.
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
