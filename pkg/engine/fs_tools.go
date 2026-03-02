package engine

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/agentforge/agentforge/pkg/workspace"
)

// NewFSTools creates the built-in file system tools bound to a workspace.
func NewFSTools(ws workspace.Manager) []Tool {
	return NewFSToolsWithArtifacts(ws, nil)
}

// NewFSToolsWithArtifacts creates file system tools with optional artifact store for fs.export.
func NewFSToolsWithArtifacts(ws workspace.Manager, artifacts artifactStore) []Tool {
	tools := []Tool{
		&fsWriteTool{ws: ws},
		&fsReadTool{ws: ws},
		&fsListTool{ws: ws},
		&fsStatTool{ws: ws},
		&fsDeleteTool{ws: ws},
	}
	if artifacts != nil {
		tools = append(tools, &fsExportTool{ws: ws, artifacts: artifacts})
	}
	return tools
}

// artifactStore is the subset of artifact.Store needed by fs.export.
type artifactStore interface {
	Put(ctx context.Context, key string, r io.Reader) (sha256hex string, size int64, err error)
}

// --- fs.write ---

type fsWriteTool struct{ ws workspace.Manager }

func (t *fsWriteTool) Name() string        { return "fs.write" }
func (t *fsWriteTool) Description() string { return "Write a file to the workspace" }

func (t *fsWriteTool) Execute(ctx context.Context, args string) (*ToolResult, error) {
	var params struct {
		Path          string `json:"path"`
		Content       string `json:"content"`
		ContentBase64 string `json:"content_base64"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return &ToolResult{Error: fmt.Sprintf("invalid args: %v", err)}, nil
	}

	var data []byte
	if params.ContentBase64 != "" {
		var err error
		data, err = base64.StdEncoding.DecodeString(params.ContentBase64)
		if err != nil {
			return &ToolResult{Error: fmt.Sprintf("base64 decode: %v", err)}, nil
		}
	} else {
		data = []byte(params.Content)
	}

	if err := t.ws.Write(ctx, params.Path, data); err != nil {
		return &ToolResult{Error: err.Error()}, nil
	}
	return &ToolResult{Output: fmt.Sprintf("wrote %d bytes to %s", len(data), params.Path)}, nil
}

// --- fs.read ---

type fsReadTool struct{ ws workspace.Manager }

func (t *fsReadTool) Name() string        { return "fs.read" }
func (t *fsReadTool) Description() string { return "Read a file from the workspace" }

func (t *fsReadTool) Execute(ctx context.Context, args string) (*ToolResult, error) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return &ToolResult{Error: fmt.Sprintf("invalid args: %v", err)}, nil
	}

	data, err := t.ws.Read(ctx, params.Path)
	if err != nil {
		return &ToolResult{Error: err.Error()}, nil
	}
	// Return as text if printable, else base64.
	if isPrintable(data) {
		return &ToolResult{Output: string(data)}, nil
	}
	return &ToolResult{Output: base64.StdEncoding.EncodeToString(data)}, nil
}

// --- fs.list ---

type fsListTool struct{ ws workspace.Manager }

func (t *fsListTool) Name() string        { return "fs.list" }
func (t *fsListTool) Description() string { return "List files in a workspace directory" }

func (t *fsListTool) Execute(ctx context.Context, args string) (*ToolResult, error) {
	var params struct {
		Dir string `json:"dir"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return &ToolResult{Error: fmt.Sprintf("invalid args: %v", err)}, nil
	}
	if params.Dir == "" {
		params.Dir = "."
	}

	entries, err := t.ws.List(ctx, params.Dir)
	if err != nil {
		return &ToolResult{Error: err.Error()}, nil
	}

	var lines []string
	for _, e := range entries {
		suffix := ""
		if e.IsDir {
			suffix = "/"
		}
		lines = append(lines, fmt.Sprintf("%s%s (%d bytes)", e.Name, suffix, e.Size))
	}
	return &ToolResult{Output: strings.Join(lines, "\n")}, nil
}

// --- fs.stat ---

type fsStatTool struct{ ws workspace.Manager }

func (t *fsStatTool) Name() string        { return "fs.stat" }
func (t *fsStatTool) Description() string { return "Get file metadata" }

func (t *fsStatTool) Execute(ctx context.Context, args string) (*ToolResult, error) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return &ToolResult{Error: fmt.Sprintf("invalid args: %v", err)}, nil
	}

	info, err := t.ws.Stat(ctx, params.Path)
	if err != nil {
		return &ToolResult{Error: err.Error()}, nil
	}

	data, _ := json.Marshal(info)
	return &ToolResult{Output: string(data)}, nil
}

// --- fs.delete ---

type fsDeleteTool struct{ ws workspace.Manager }

func (t *fsDeleteTool) Name() string        { return "fs.delete" }
func (t *fsDeleteTool) Description() string { return "Delete a file from the workspace" }

func (t *fsDeleteTool) Execute(ctx context.Context, args string) (*ToolResult, error) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return &ToolResult{Error: fmt.Sprintf("invalid args: %v", err)}, nil
	}

	if err := t.ws.Delete(ctx, params.Path); err != nil {
		return &ToolResult{Error: err.Error()}, nil
	}
	return &ToolResult{Output: fmt.Sprintf("deleted %s", params.Path)}, nil
}

// --- fs.export ---

type fsExportTool struct {
	ws        workspace.Manager
	artifacts artifactStore
}

func (t *fsExportTool) Name() string        { return "fs.export" }
func (t *fsExportTool) Description() string { return "Export workspace files as an artifact archive" }

func (t *fsExportTool) Execute(ctx context.Context, args string) (*ToolResult, error) {
	var params struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return &ToolResult{Error: fmt.Sprintf("invalid args: %v", err)}, nil
	}
	if len(params.Paths) == 0 {
		return &ToolResult{Error: "paths required"}, nil
	}

	pr, pw := io.Pipe()
	go func() {
		gw := gzip.NewWriter(pw)
		tw := tar.NewWriter(gw)
		var writeErr error
		for _, p := range params.Paths {
			data, err := t.ws.Read(ctx, p)
			if err != nil {
				writeErr = fmt.Errorf("read %s: %w", p, err)
				break
			}
			hdr := &tar.Header{
				Name: p,
				Mode: 0o644,
				Size: int64(len(data)),
			}
			if err := tw.WriteHeader(hdr); err != nil {
				writeErr = err
				break
			}
			if _, err := tw.Write(data); err != nil {
				writeErr = err
				break
			}
		}
		tw.Close()
		gw.Close()
		pw.CloseWithError(writeErr)
	}()

	key := fmt.Sprintf("exports/artifact_%s.tar.gz", randomHex())
	sha, size, err := t.artifacts.Put(ctx, key, pr)
	if err != nil {
		return &ToolResult{Error: fmt.Sprintf("export failed: %v", err)}, nil
	}

	result := fmt.Sprintf(`{"s3_key":%q,"sha256":%q,"size":%d}`, key, sha, size)
	return &ToolResult{Output: result}, nil
}

func randomHex() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand.Read failed: %v", err))
	}
	return fmt.Sprintf("%x", b)
}

func isPrintable(data []byte) bool {
	for _, b := range data {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
			return false
		}
	}
	return true
}
