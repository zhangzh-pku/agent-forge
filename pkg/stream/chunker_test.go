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

func TestChunkerClearsLastErrorAfterSuccessfulFlush(t *testing.T) {
	cfg := ChunkerConfig{
		FlushInterval: 10 * time.Second,
		FlushBytes:    1, // force immediate flush on each write
	}

	flushErr := fmt.Errorf("push failed once")
	flushCalls := 0
	c := NewChunker(cfg, func(_ []byte) error {
		flushCalls++
		if flushCalls == 1 {
			return flushErr
		}
		return nil
	})

	if err := c.Write(&model.StreamEvent{
		TaskID: "task_1",
		RunID:  "run_1",
		Type:   model.StreamEventTokenChunk,
		Data:   map[string]string{"text": "first"},
	}); err != nil {
		t.Fatalf("first write failed: %v", err)
	}
	if err := c.LastError(); err == nil {
		t.Fatal("expected non-nil LastError after first flush failure")
	}

	if err := c.Write(&model.StreamEvent{
		TaskID: "task_1",
		RunID:  "run_1",
		Type:   model.StreamEventTokenChunk,
		Data:   map[string]string{"text": "second"},
	}); err != nil {
		t.Fatalf("second write failed: %v", err)
	}
	if err := c.LastError(); err != nil {
		t.Fatalf("expected LastError cleared after successful flush, got %v", err)
	}
	c.Stop()
}

func TestChunkerErrorStatsTracksCountAndHistory(t *testing.T) {
	cfg := ChunkerConfig{
		FlushInterval: 10 * time.Second,
		FlushBytes:    1, // force immediate flush
	}

	flushCalls := 0
	c := NewChunker(cfg, func(_ []byte) error {
		flushCalls++
		if flushCalls <= 3 {
			return fmt.Errorf("push failed %d", flushCalls)
		}
		return nil
	})
	defer c.Stop()

	for i := 0; i < 4; i++ {
		if err := c.Write(&model.StreamEvent{
			TaskID: "task_1",
			RunID:  "run_1",
			Type:   model.StreamEventTokenChunk,
			Data:   map[string]string{"text": "chunk"},
		}); err != nil {
			t.Fatalf("write %d failed: %v", i, err)
		}
	}

	stats := c.ErrorStats()
	if stats.ErrorCount != 3 {
		t.Fatalf("expected 3 flush errors, got %d", stats.ErrorCount)
	}
	if len(stats.RecentErrors) != 3 {
		t.Fatalf("expected 3 recent errors, got %d", len(stats.RecentErrors))
	}
	if stats.LastError != "" {
		t.Fatalf("expected empty LastError after successful flush, got %q", stats.LastError)
	}
}

func TestChunkerErrorStatsHistoryCap(t *testing.T) {
	cfg := ChunkerConfig{
		FlushInterval: 10 * time.Second,
		FlushBytes:    1,
	}

	c := NewChunker(cfg, func(_ []byte) error {
		return fmt.Errorf("flush failed")
	})
	defer c.Stop()

	for i := 0; i < maxChunkerErrorHistory+3; i++ {
		if err := c.Write(&model.StreamEvent{
			TaskID: "task_1",
			RunID:  "run_1",
			Type:   model.StreamEventTokenChunk,
			Data:   map[string]string{"text": "chunk"},
		}); err != nil {
			t.Fatalf("write %d failed: %v", i, err)
		}
	}

	stats := c.ErrorStats()
	if len(stats.RecentErrors) != maxChunkerErrorHistory {
		t.Fatalf("expected capped error history size %d, got %d", maxChunkerErrorHistory, len(stats.RecentErrors))
	}
	if stats.ErrorCount != int64(maxChunkerErrorHistory+3) {
		t.Fatalf("expected error count %d, got %d", maxChunkerErrorHistory+3, stats.ErrorCount)
	}
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

func TestChunkedAWSPusherDeadConnectionExpires(t *testing.T) {
	var calls int
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

	pusher := NewChunkedAWSPusher("https://example.com", ChunkerConfig{
		FlushInterval: 10 * time.Second,
		FlushBytes:    1, // flush immediately
	}, func(_ context.Context, _ string, _ []byte) error {
		calls++
		if calls == 1 {
			return &GoneError{}
		}
		return nil
	})
	pusher.deadTTL = 5 * time.Second
	pusher.now = func() time.Time { return now }

	alive, err := pusher.Push(context.Background(), "conn_1", &model.StreamEvent{
		TaskID: "task_1",
		RunID:  "run_1",
		Type:   model.StreamEventStepStart,
	})
	if err != nil {
		t.Fatalf("expected nil error for gone connection, got %v", err)
	}
	if alive {
		t.Fatal("expected alive=false for first gone push")
	}

	alive, err = pusher.Push(context.Background(), "conn_1", &model.StreamEvent{
		TaskID: "task_1",
		RunID:  "run_1",
		Type:   model.StreamEventStepEnd,
	})
	if err != nil {
		t.Fatalf("expected nil error while dead entry still valid, got %v", err)
	}
	if alive {
		t.Fatal("expected alive=false before dead entry TTL expires")
	}
	if calls != 1 {
		t.Fatalf("expected no additional post while dead entry is active, got %d", calls)
	}

	now = now.Add(6 * time.Second)
	alive, err = pusher.Push(context.Background(), "conn_1", &model.StreamEvent{
		TaskID: "task_1",
		RunID:  "run_1",
		Type:   model.StreamEventStepEnd,
	})
	if err != nil {
		t.Fatalf("expected push retry after dead entry expiry, got %v", err)
	}
	if !alive {
		t.Fatal("expected alive=true after dead entry expires and push succeeds")
	}
	if calls != 2 {
		t.Fatalf("expected post retried after expiry, got %d calls", calls)
	}
}

func TestChunkedAWSPusherDeadConnectionLimitPrunesOldest(t *testing.T) {
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	pusher := NewChunkedAWSPusher("https://example.com", ChunkerConfig{
		FlushInterval: 10 * time.Second,
		FlushBytes:    1024,
	}, func(_ context.Context, _ string, _ []byte) error {
		return nil
	})
	pusher.deadTTL = time.Hour
	pusher.deadLimit = 2
	pusher.now = func() time.Time { return now }

	pusher.markDead("conn_1")
	now = now.Add(time.Second)
	pusher.markDead("conn_2")
	now = now.Add(time.Second)
	pusher.markDead("conn_3")

	if pusher.isDead("conn_1") {
		t.Fatal("expected oldest dead connection to be pruned when limit exceeded")
	}
	if !pusher.isDead("conn_2") || !pusher.isDead("conn_3") {
		t.Fatal("expected newest dead connections to remain tracked")
	}
}

func TestChunkedAWSPusherTransientErrorRecovers(t *testing.T) {
	var calls int
	pusher := NewChunkedAWSPusher("https://example.com", ChunkerConfig{
		FlushInterval: 10 * time.Second,
		FlushBytes:    1, // flush immediately
	}, func(_ context.Context, _ string, _ []byte) error {
		calls++
		if calls == 1 {
			return fmt.Errorf("temporary network failure")
		}
		return nil
	})

	alive, err := pusher.Push(context.Background(), "conn_1", &model.StreamEvent{
		TaskID: "task_1",
		RunID:  "run_1",
		Type:   model.StreamEventStepStart,
	})
	if err == nil {
		t.Fatal("expected transient error on first push")
	}
	if !alive {
		t.Fatal("expected alive=true on transient error")
	}

	alive, err = pusher.Push(context.Background(), "conn_1", &model.StreamEvent{
		TaskID: "task_1",
		RunID:  "run_1",
		Type:   model.StreamEventStepEnd,
	})
	if err != nil {
		t.Fatalf("expected recovery after transient error, got %v", err)
	}
	if !alive {
		t.Fatal("expected alive=true after recovery")
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
