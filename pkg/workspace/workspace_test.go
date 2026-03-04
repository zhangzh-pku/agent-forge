package workspace

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func newTestWorkspace(t *testing.T) *LocalManager {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{Root: dir, MaxTotalBytes: 1024 * 1024, MaxFileCount: 100}
	m, err := NewLocalManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Cleanup() })
	return m
}

func TestWriteReadDelete(t *testing.T) {
	m := newTestWorkspace(t)
	ctx := context.Background()

	if err := m.Write(ctx, "hello.txt", []byte("world")); err != nil {
		t.Fatal(err)
	}
	data, err := m.Read(ctx, "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "world" {
		t.Fatalf("got %q, want %q", data, "world")
	}

	if err := m.Delete(ctx, "hello.txt"); err != nil {
		t.Fatal(err)
	}
	_, err = m.Read(ctx, "hello.txt")
	if err == nil {
		t.Fatal("expected error reading deleted file")
	}
}

func TestNewLocalManagerCreatesPrivateRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace-root")
	cfg := Config{Root: root, MaxTotalBytes: 1024 * 1024, MaxFileCount: 100}
	m, err := NewLocalManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Cleanup() })

	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("expected root mode 0700, got %04o", got)
	}
}

func TestPathTraversal(t *testing.T) {
	m := newTestWorkspace(t)
	ctx := context.Background()

	err := m.Write(ctx, "../escape.txt", []byte("evil"))
	if err != ErrPathTraversal {
		t.Fatalf("expected ErrPathTraversal, got %v", err)
	}

	err = m.Write(ctx, "foo/../../escape.txt", []byte("evil"))
	if err != ErrPathTraversal {
		t.Fatalf("expected ErrPathTraversal, got %v", err)
	}
}

func TestSymlinkTraversalBlocked(t *testing.T) {
	m := newTestWorkspace(t)
	ctx := context.Background()

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}

	linkPath := filepath.Join(m.Root(), "link.txt")
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}

	if _, err := m.Read(ctx, "link.txt"); err != ErrPathTraversal {
		t.Fatalf("expected ErrPathTraversal on read via symlink, got %v", err)
	}
	if err := m.Write(ctx, "link.txt", []byte("blocked")); err != ErrPathTraversal {
		t.Fatalf("expected ErrPathTraversal on write via symlink, got %v", err)
	}

	linkDir := filepath.Join(m.Root(), "linked_dir")
	if err := os.Symlink(outsideDir, linkDir); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}
	if err := m.Write(ctx, "linked_dir/new.txt", []byte("blocked")); err != ErrPathTraversal {
		t.Fatalf("expected ErrPathTraversal on write through symlink dir, got %v", err)
	}
}

func TestQuotaBytes(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Root: dir, MaxTotalBytes: 10, MaxFileCount: 100}
	m, err := NewLocalManager(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := m.Write(ctx, "a.txt", []byte("12345")); err != nil {
		t.Fatal(err)
	}
	err = m.Write(ctx, "b.txt", []byte("123456"))
	if err != ErrQuotaBytes {
		t.Fatalf("expected ErrQuotaBytes, got %v", err)
	}
}

func TestQuotaFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Root: dir, MaxTotalBytes: 1024 * 1024, MaxFileCount: 2}
	m, err := NewLocalManager(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	m.Write(ctx, "a.txt", []byte("a"))
	m.Write(ctx, "b.txt", []byte("b"))
	err = m.Write(ctx, "c.txt", []byte("c"))
	if err != ErrQuotaFiles {
		t.Fatalf("expected ErrQuotaFiles, got %v", err)
	}
}

func TestSnapshotRestore(t *testing.T) {
	m1 := newTestWorkspace(t)
	ctx := context.Background()

	m1.Write(ctx, "a.txt", []byte("alpha"))
	m1.Write(ctx, "sub/b.txt", []byte("beta"))

	rc, err := m1.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	io.Copy(&buf, rc)
	rc.Close()

	dir2 := t.TempDir()
	cfg2 := Config{Root: dir2, MaxTotalBytes: 1024 * 1024, MaxFileCount: 100}
	m2, err := NewLocalManager(cfg2)
	if err != nil {
		t.Fatal(err)
	}

	if err := m2.Restore(ctx, &buf); err != nil {
		t.Fatal(err)
	}

	data, err := m2.Read(ctx, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "alpha" {
		t.Fatalf("got %q, want %q", data, "alpha")
	}
	data, err = m2.Read(ctx, "sub/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "beta" {
		t.Fatalf("got %q, want %q", data, "beta")
	}

	// Verify quota tracking after restore.
	totalBytes, fileCount := m2.Usage()
	if fileCount != 2 {
		t.Fatalf("expected 2 files after restore, got %d", fileCount)
	}
	if totalBytes != int64(len("alpha")+len("beta")) {
		t.Fatalf("expected %d bytes, got %d", len("alpha")+len("beta"), totalBytes)
	}
}

func TestRestorePathTraversal(t *testing.T) {
	m := newTestWorkspace(t)
	ctx := context.Background()

	buf := createMaliciousTarGz(t)
	err := m.Restore(ctx, buf)
	if err == nil {
		t.Fatal("expected error for tar path traversal")
	}
}

func createMaliciousTarGz(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	// Malicious entry with path traversal.
	hdr := &tar.Header{
		Name: "../../../etc/evil.txt",
		Mode: 0o644,
		Size: 4,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte("evil"))
	tw.Close()
	gw.Close()
	return &buf
}

func TestRestorePathTraversalMiddleComponent(t *testing.T) {
	m := newTestWorkspace(t)
	ctx := context.Background()

	// Create tar.gz with ".." in middle of path (e.g., "foo/../../etc/passwd").
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{
		Name: "foo/../../etc/passwd",
		Mode: 0o644,
		Size: 4,
	}
	tw.WriteHeader(hdr)
	tw.Write([]byte("evil"))
	tw.Close()
	gw.Close()

	err := m.Restore(ctx, &buf)
	if err == nil {
		t.Fatal("expected error for path traversal with .. in middle component")
	}
}

func TestRestoreSkipsSymlinks(t *testing.T) {
	m := newTestWorkspace(t)
	ctx := context.Background()

	// Create tar.gz with a symlink entry - should be skipped.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	// Regular file first.
	tw.WriteHeader(&tar.Header{Name: "good.txt", Mode: 0o644, Size: 4})
	tw.Write([]byte("good"))

	// Symlink entry should be skipped.
	tw.WriteHeader(&tar.Header{
		Name:     "evil_link",
		Typeflag: tar.TypeSymlink,
		Linkname: "/etc/passwd",
	})

	tw.Close()
	gw.Close()

	err := m.Restore(ctx, &buf)
	if err != nil {
		t.Fatalf("restore should succeed (skipping symlinks): %v", err)
	}

	// Good file should exist.
	data, err := m.Read(ctx, "good.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "good" {
		t.Fatalf("expected 'good', got %q", data)
	}

	// Symlink should NOT exist.
	_, err = m.Read(ctx, "evil_link")
	if err == nil {
		t.Fatal("symlink should not have been extracted")
	}
}

func TestRestoreMasksFilePermissions(t *testing.T) {
	m := newTestWorkspace(t)
	ctx := context.Background()

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	content := []byte("secret")
	if err := tw.WriteHeader(&tar.Header{
		Name: "private.txt",
		Mode: 0o777,
		Size: int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := m.Restore(ctx, &buf); err != nil {
		t.Fatalf("restore failed: %v", err)
	}

	info, err := os.Stat(filepath.Join(m.Root(), "private.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("expected restored file mode 0644, got %04o", got)
	}
}

func TestListAndStat(t *testing.T) {
	m := newTestWorkspace(t)
	ctx := context.Background()

	m.Write(ctx, "x.txt", []byte("xxx"))
	m.Write(ctx, "y.txt", []byte("yy"))

	entries, err := m.List(ctx, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	info, err := m.Stat(ctx, "x.txt")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 3 {
		t.Fatalf("expected size 3, got %d", info.Size)
	}
}

func TestNestedDirectories(t *testing.T) {
	m := newTestWorkspace(t)
	ctx := context.Background()

	if err := m.Write(ctx, "a/b/c.txt", []byte("deep")); err != nil {
		t.Fatal(err)
	}
	data, err := m.Read(ctx, "a/b/c.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "deep" {
		t.Fatalf("got %q", data)
	}
}

func TestSnapshotRestoreEmptyWorkspace(t *testing.T) {
	m := newTestWorkspace(t)
	ctx := context.Background()

	// Snapshot an empty workspace.
	rc, err := m.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	io.Copy(&buf, rc)
	rc.Close()

	// Restore into a new workspace.
	dir2 := t.TempDir()
	cfg2 := Config{Root: dir2, MaxTotalBytes: 1024 * 1024, MaxFileCount: 100}
	m2, err := NewLocalManager(cfg2)
	if err != nil {
		t.Fatal(err)
	}

	if err := m2.Restore(ctx, &buf); err != nil {
		t.Fatal(err)
	}

	totalBytes, fileCount := m2.Usage()
	if totalBytes != 0 || fileCount != 0 {
		t.Fatalf("expected 0/0 for empty workspace, got %d/%d", totalBytes, fileCount)
	}
}

func TestRestoreQuotaFileCount(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Root: dir, MaxTotalBytes: 1024 * 1024, MaxFileCount: 2}
	m, err := NewLocalManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Create tar.gz with 3 files, exceeding MaxFileCount=2.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for i := 0; i < 3; i++ {
		name := "file" + string(rune('a'+i)) + ".txt"
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 4})
		tw.Write([]byte("data"))
	}
	tw.Close()
	gw.Close()

	err = m.Restore(ctx, &buf)
	if err == nil {
		t.Fatal("expected quota error for too many files")
	}
}

func TestRestoreQuotaBytes(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Root: dir, MaxTotalBytes: 10, MaxFileCount: 100}
	m, err := NewLocalManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Create tar.gz with a file exceeding MaxTotalBytes=10.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	content := "this is more than ten bytes of data"
	tw.WriteHeader(&tar.Header{Name: "big.txt", Mode: 0o644, Size: int64(len(content))})
	tw.Write([]byte(content))
	tw.Close()
	gw.Close()

	err = m.Restore(ctx, &buf)
	if err == nil {
		t.Fatal("expected quota error for exceeding total bytes")
	}
}

func TestUsageTracking(t *testing.T) {
	m := newTestWorkspace(t)
	ctx := context.Background()

	m.Write(ctx, "a.txt", []byte("hello"))
	totalBytes, fileCount := m.Usage()
	if totalBytes != 5 || fileCount != 1 {
		t.Fatalf("expected 5 bytes/1 file, got %d/%d", totalBytes, fileCount)
	}

	// Overwrite with larger content.
	m.Write(ctx, "a.txt", []byte("hello world"))
	totalBytes, fileCount = m.Usage()
	if totalBytes != 11 || fileCount != 1 {
		t.Fatalf("expected 11 bytes/1 file, got %d/%d", totalBytes, fileCount)
	}

	m.Delete(ctx, "a.txt")
	totalBytes, fileCount = m.Usage()
	if totalBytes != 0 || fileCount != 0 {
		t.Fatalf("expected 0/0, got %d/%d", totalBytes, fileCount)
	}
}
