package engine

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/agentforge/agentforge/pkg/workspace"
)

func newFSTestWorkspace(t *testing.T) workspace.Manager {
	t.Helper()
	ws, err := workspace.NewLocalManager(workspace.Config{
		Root:          t.TempDir(),
		MaxTotalBytes: 10 * 1024 * 1024,
		MaxFileCount:  1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Cleanup() })
	return ws
}

func TestFSReadToolTruncatesText(t *testing.T) {
	ws := newFSTestWorkspace(t)
	ctx := context.Background()
	if err := ws.Write(ctx, "large.txt", []byte("abcdefghijklmnop")); err != nil {
		t.Fatal(err)
	}

	tool := &fsReadTool{ws: ws, maxReadBytes: 8}
	res, err := tool.Execute(ctx, `{"path":"large.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected tool error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "abcdefgh") {
		t.Fatalf("expected truncated content prefix in output, got %q", res.Output)
	}
	if !strings.Contains(res.Output, "[TRUNCATED] showing first 8 of 16 bytes") {
		t.Fatalf("expected truncation marker, got %q", res.Output)
	}
}

func TestFSReadToolTruncatesBinaryWithMarker(t *testing.T) {
	ws := newFSTestWorkspace(t)
	ctx := context.Background()
	content := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05}
	if err := ws.Write(ctx, "blob.bin", content); err != nil {
		t.Fatal(err)
	}

	tool := &fsReadTool{ws: ws, maxReadBytes: 4}
	res, err := tool.Execute(ctx, `{"path":"blob.bin"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected tool error: %s", res.Error)
	}
	wantPrefix := base64.StdEncoding.EncodeToString(content[:4])
	if !strings.Contains(res.Output, wantPrefix) {
		t.Fatalf("expected base64 output prefix %q, got %q", wantPrefix, res.Output)
	}
	if !strings.Contains(res.Output, "[TRUNCATED_BASE64] showing first 4 of 6 bytes before base64 encoding") {
		t.Fatalf("expected binary truncation marker, got %q", res.Output)
	}
}

func TestFSReadToolMaxBytesOverride(t *testing.T) {
	ws := newFSTestWorkspace(t)
	ctx := context.Background()
	if err := ws.Write(ctx, "override.txt", []byte("123456")); err != nil {
		t.Fatal(err)
	}

	tool := &fsReadTool{ws: ws, maxReadBytes: 10}
	res, err := tool.Execute(ctx, `{"path":"override.txt","max_bytes":3}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected tool error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "123") {
		t.Fatalf("expected output to include first 3 bytes, got %q", res.Output)
	}
	if !strings.Contains(res.Output, "[TRUNCATED] showing first 3 of 6 bytes") {
		t.Fatalf("expected override truncation marker, got %q", res.Output)
	}
}

func TestFSReadMaxBytesFromEnv(t *testing.T) {
	t.Setenv("AGENTFORGE_FS_READ_MAX_BYTES", "")
	if got := fsReadMaxBytesFromEnv(); got != defaultFSReadMaxBytes {
		t.Fatalf("expected default limit %d, got %d", defaultFSReadMaxBytes, got)
	}

	t.Setenv("AGENTFORGE_FS_READ_MAX_BYTES", "2048")
	if got := fsReadMaxBytesFromEnv(); got != 2048 {
		t.Fatalf("expected env limit 2048, got %d", got)
	}

	t.Setenv("AGENTFORGE_FS_READ_MAX_BYTES", "-1")
	if got := fsReadMaxBytesFromEnv(); got != defaultFSReadMaxBytes {
		t.Fatalf("expected default limit for invalid env, got %d", got)
	}
}
