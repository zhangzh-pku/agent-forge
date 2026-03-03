package stream

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
)

func TestChunkerFlushByBytes(t *testing.T) {
	var mu sync.Mutex
	var flushed [][]byte

	cfg := ChunkerConfig{
		FlushInterval: 10 * time.Second, // Long interval so it doesn't trigger.
		FlushBytes:    50,               // Small threshold.
	}

	c := NewChunker(cfg, func(data []byte) error {
		mu.Lock()
		flushed = append(flushed, data)
		mu.Unlock()
		return nil
	})

	// Write events until byte threshold is exceeded.
	for i := 0; i < 5; i++ {
		c.Write(&model.StreamEvent{
			TaskID: "task_1",
			RunID:  "run_1",
			Seq:    int64(i),
			TS:     time.Now().Unix(),
			Type:   model.StreamEventTokenChunk,
			Data:   map[string]string{"text": "hello"},
		})
	}

	// Give flush goroutine time to run.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	count := len(flushed)
	mu.Unlock()

	if count == 0 {
		t.Fatal("expected at least one flush by byte threshold")
	}
	c.Stop()
}

func TestChunkerFlushByTime(t *testing.T) {
	var mu sync.Mutex
	var flushed [][]byte

	cfg := ChunkerConfig{
		FlushInterval: 50 * time.Millisecond,
		FlushBytes:    1024 * 1024, // Very large so it won't trigger by bytes.
	}

	c := NewChunker(cfg, func(data []byte) error {
		mu.Lock()
		flushed = append(flushed, data)
		mu.Unlock()
		return nil
	})

	c.Write(&model.StreamEvent{
		TaskID: "task_1",
		RunID:  "run_1",
		Type:   model.StreamEventStepStart,
	})

	// Wait for time-based flush.
	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	count := len(flushed)
	mu.Unlock()

	if count == 0 {
		t.Fatal("expected time-based flush")
	}
	c.Stop()
}

func TestChunkerStop(t *testing.T) {
	var mu sync.Mutex
	var flushed [][]byte

	cfg := ChunkerConfig{
		FlushInterval: 10 * time.Second,
		FlushBytes:    1024 * 1024,
	}

	c := NewChunker(cfg, func(data []byte) error {
		mu.Lock()
		flushed = append(flushed, data)
		mu.Unlock()
		return nil
	})

	c.Write(&model.StreamEvent{
		TaskID: "task_1",
		Type:   model.StreamEventComplete,
	})

	c.Stop()
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	count := len(flushed)
	mu.Unlock()

	if count == 0 {
		t.Fatal("expected flush on stop")
	}
}

func TestChunkerFlushError(t *testing.T) {
	cfg := ChunkerConfig{
		FlushInterval: 10 * time.Second,
		FlushBytes:    10, // Small threshold to trigger flush.
	}

	flushErr := fmt.Errorf("push failed")
	c := NewChunker(cfg, func(data []byte) error {
		return flushErr
	})

	// Write enough events to trigger a byte-threshold flush.
	for i := 0; i < 5; i++ {
		c.Write(&model.StreamEvent{
			TaskID: "task_1",
			RunID:  "run_1",
			Seq:    int64(i),
			TS:     time.Now().Unix(),
			Type:   model.StreamEventTokenChunk,
			Data:   map[string]string{"text": "hello"},
		})
	}

	time.Sleep(50 * time.Millisecond)

	if err := c.LastError(); err == nil {
		t.Fatal("expected non-nil LastError after flush failure")
	}
	c.Stop()
}

func TestMockPusher(t *testing.T) {
	p := NewMockPusher()

	event := &model.StreamEvent{TaskID: "t1", Type: model.StreamEventStepStart}

	alive, err := p.Push(context.Background(), "conn_1", event)
	if err != nil || !alive {
		t.Fatal("expected alive push")
	}

	if len(p.Events()) != 1 {
		t.Fatalf("expected 1 event, got %d", len(p.Events()))
	}

	// Test gone connection.
	p.GoneConnections["conn_2"] = true
	alive, err = p.Push(context.Background(), "conn_2", event)
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatal("expected not alive for gone connection")
	}
}

func TestAWSPusherGoneConnection(t *testing.T) {
	pusher := NewAWSPusher("https://example.com", func(_ context.Context, _ string, _ []byte) error {
		return &GoneError{}
	})

	event := &model.StreamEvent{TaskID: "t1", Type: model.StreamEventStepStart}
	alive, err := pusher.Push(context.Background(), "conn_1", event)
	if err != nil {
		t.Fatalf("expected nil error for gone connection, got %v", err)
	}
	if alive {
		t.Fatal("expected alive=false for gone connection")
	}
}

func TestAWSPusherTransientError(t *testing.T) {
	pusher := NewAWSPusher("https://example.com", func(_ context.Context, _ string, _ []byte) error {
		return fmt.Errorf("network timeout")
	})

	event := &model.StreamEvent{TaskID: "t1", Type: model.StreamEventStepStart}
	alive, err := pusher.Push(context.Background(), "conn_1", event)
	if err == nil {
		t.Fatal("expected error for transient failure")
	}
	if !alive {
		t.Fatal("expected alive=true for transient error (connection should NOT be removed)")
	}
}

func TestAWSPusherSuccess(t *testing.T) {
	pusher := NewAWSPusher("https://example.com", func(_ context.Context, _ string, _ []byte) error {
		return nil
	})

	event := &model.StreamEvent{TaskID: "t1", Type: model.StreamEventStepStart}
	alive, err := pusher.Push(context.Background(), "conn_1", event)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !alive {
		t.Fatal("expected alive=true for successful push")
	}
}

func TestIsGoneErrorPreciseMatch(t *testing.T) {
	if IsGoneError(fmt.Errorf("processed 410 records")) {
		t.Fatal("expected false for unrelated 410 text")
	}
	if !IsGoneError(fmt.Errorf("api error: status code: 410")) {
		t.Fatal("expected true for 410 status error")
	}
}

func TestChunkedAWSPusherFlushByBytes(t *testing.T) {
	var mu sync.Mutex
	var calls int
	var payloads [][]byte

	pusher := NewChunkedAWSPusher("https://example.com", ChunkerConfig{
		FlushInterval: 10 * time.Second,
		FlushBytes:    1, // force immediate flush
	}, func(_ context.Context, _ string, data []byte) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		cp := make([]byte, len(data))
		copy(cp, data)
		payloads = append(payloads, cp)
		return nil
	})

	alive, err := pusher.Push(context.Background(), "conn_1", &model.StreamEvent{
		TaskID: "task_1",
		RunID:  "run_1",
		Type:   model.StreamEventStepStart,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !alive {
		t.Fatal("expected alive=true")
	}

	mu.Lock()
	defer mu.Unlock()
	if calls == 0 {
		t.Fatal("expected at least one post call")
	}
	if len(payloads[0]) == 0 || payloads[0][len(payloads[0])-1] != '\n' {
		t.Fatal("expected newline-delimited chunk payload")
	}
}

func TestChunkedAWSPusherFlushByTime(t *testing.T) {
	flushed := make(chan struct{}, 1)
	pusher := NewChunkedAWSPusher("https://example.com", ChunkerConfig{
		FlushInterval: 30 * time.Millisecond,
		FlushBytes:    1024 * 1024,
	}, func(_ context.Context, _ string, _ []byte) error {
		select {
		case flushed <- struct{}{}:
		default:
		}
		return nil
	})

	alive, err := pusher.Push(context.Background(), "conn_1", &model.StreamEvent{
		TaskID: "task_1",
		RunID:  "run_1",
		Type:   model.StreamEventStepStart,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !alive {
		t.Fatal("expected alive=true")
	}

	select {
	case <-flushed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected time-based flush")
	}
}

func TestChunkedAWSPusherGoneConnection(t *testing.T) {
	var calls int
	pusher := NewChunkedAWSPusher("https://example.com", ChunkerConfig{
		FlushInterval: 10 * time.Second,
		FlushBytes:    1, // flush immediately
	}, func(_ context.Context, _ string, _ []byte) error {
		calls++
		return &GoneError{}
	})

	alive, err := pusher.Push(context.Background(), "conn_1", &model.StreamEvent{
		TaskID: "task_1",
		RunID:  "run_1",
		Type:   model.StreamEventStepStart,
	})
	if err != nil {
		t.Fatalf("expected nil error for gone connection, got %v", err)
	}
	if alive {
		t.Fatal("expected alive=false for gone connection")
	}

	// After being marked dead, the pusher should short-circuit.
	alive, err = pusher.Push(context.Background(), "conn_1", &model.StreamEvent{
		TaskID: "task_1",
		RunID:  "run_1",
		Type:   model.StreamEventStepEnd,
	})
	if err != nil {
		t.Fatalf("expected nil error after dead connection, got %v", err)
	}
	if alive {
		t.Fatal("expected alive=false after dead connection")
	}
	if calls != 1 {
		t.Fatalf("expected one post attempt before dead short-circuit, got %d", calls)
	}
}

func TestIsGoneError(t *testing.T) {
	if IsGoneError(nil) {
		t.Fatal("nil should not be gone")
	}
	if !IsGoneError(&GoneError{}) {
		t.Fatal("GoneError should be gone")
	}
	if !IsGoneError(fmt.Errorf("GoneException: connection no longer exists")) {
		t.Fatal("GoneException string should match")
	}
	if !IsGoneError(fmt.Errorf("HTTP 410 Gone: connection stale")) {
		t.Fatal("410 Gone string should match")
	}
	if IsGoneError(fmt.Errorf("network timeout")) {
		t.Fatal("timeout should not be gone")
	}
}
