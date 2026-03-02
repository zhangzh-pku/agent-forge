package artifact

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestMemoryStorePutAndGet(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	data := []byte("hello world")
	sha, size, err := s.Put(ctx, "test/key.txt", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(data)) {
		t.Fatalf("expected size %d, got %d", len(data), size)
	}
	if sha == "" {
		t.Fatal("expected non-empty sha256")
	}

	rc, err := s.Get(ctx, "test/key.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("expected %q, got %q", data, got)
	}
}

func TestMemoryStoreGetNotFound(t *testing.T) {
	s := NewMemoryStore()
	_, err := s.Get(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent key")
	}
}

func TestMemoryStoreExists(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	exists, err := s.Exists(ctx, "key1")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("expected false for nonexistent key")
	}

	s.Put(ctx, "key1", bytes.NewReader([]byte("data")))

	exists, err = s.Exists(ctx, "key1")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("expected true after put")
	}
}

func TestMemoryStorePresignedURL(t *testing.T) {
	s := NewMemoryStore()
	url, err := s.PresignedURL(context.Background(), "any")
	if err != nil {
		t.Fatal(err)
	}
	if url != "" {
		t.Fatalf("expected empty presigned URL for memory store, got %q", url)
	}
}

func TestMemoryStorePutOverwrite(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	s.Put(ctx, "key1", bytes.NewReader([]byte("v1")))
	s.Put(ctx, "key1", bytes.NewReader([]byte("v2")))

	rc, _ := s.Get(ctx, "key1")
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "v2" {
		t.Fatalf("expected v2 after overwrite, got %q", got)
	}
}

func TestMemoryStoreSHA256Deterministic(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	sha1, _, _ := s.Put(ctx, "key1", bytes.NewReader([]byte("same data")))
	sha2, _, _ := s.Put(ctx, "key2", bytes.NewReader([]byte("same data")))

	if sha1 != sha2 {
		t.Fatalf("expected same SHA256 for same data: %s != %s", sha1, sha2)
	}
}
