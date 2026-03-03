package queue

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/tenant"
)

func TestMemoryQueueFairSchedulingAcrossTenants(t *testing.T) {
	mgr := tenant.NewManager(tenant.Policy{
		MaxRunning:         2,
		MaxQueued:          10,
		RateLimitPerMinute: 100,
	})
	q := NewMemoryQueueWithConfig(MemoryQueueConfig{
		BufferSize:    20,
		MaxWorkers:    1,
		MaxAttempts:   1,
		RetryBackoff:  10 * time.Millisecond,
		DedupeWindow:  time.Second,
		TenantManager: mgr,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msgs := []*model.SQSMessage{
		{TenantID: "t1", TaskID: "a1", RunID: "r1"},
		{TenantID: "t1", TaskID: "a2", RunID: "r2"},
		{TenantID: "t2", TaskID: "b1", RunID: "r3"},
		{TenantID: "t2", TaskID: "b2", RunID: "r4"},
	}
	for _, msg := range msgs {
		if err := q.Enqueue(ctx, msg); err != nil {
			t.Fatalf("enqueue %s: %v", msg.TaskID, err)
		}
	}

	var mu sync.Mutex
	var order []string
	go q.StartConsumer(ctx, func(_ context.Context, msg *model.SQSMessage) error {
		mu.Lock()
		order = append(order, fmt.Sprintf("%s:%s", msg.TenantID, msg.TaskID))
		done := len(order) == 4
		mu.Unlock()
		if done {
			cancel()
		}
		return nil
	})

	<-ctx.Done()
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 4 {
		t.Fatalf("expected 4 deliveries, got %d (%v)", len(order), order)
	}

	want := []string{"t1:a1", "t2:b1", "t1:a2", "t2:b2"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("fair scheduling mismatch at %d: want=%s got=%s (full=%v)", i, want[i], order[i], order)
		}
	}
}

func TestMemoryQueuePerTenantRunningLimit(t *testing.T) {
	mgr := tenant.NewManager(tenant.Policy{
		MaxRunning:         1,
		MaxQueued:          10,
		RateLimitPerMinute: 100,
	})
	q := NewMemoryQueueWithConfig(MemoryQueueConfig{
		BufferSize:    10,
		MaxWorkers:    2,
		MaxAttempts:   1,
		RetryBackoff:  10 * time.Millisecond,
		DedupeWindow:  time.Second,
		TenantManager: mgr,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_ = q.Enqueue(ctx, &model.SQSMessage{TenantID: "same", TaskID: "a", RunID: "r1"})
	_ = q.Enqueue(ctx, &model.SQSMessage{TenantID: "same", TaskID: "b", RunID: "r2"})

	var concurrent atomic.Int64
	var maxSeen atomic.Int64
	var handled atomic.Int64

	go q.StartConsumer(ctx, func(_ context.Context, _ *model.SQSMessage) error {
		n := concurrent.Add(1)
		for {
			old := maxSeen.Load()
			if n <= old || maxSeen.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		concurrent.Add(-1)
		if handled.Add(1) >= 2 {
			cancel()
		}
		return nil
	})

	<-ctx.Done()
	if maxSeen.Load() > 1 {
		t.Fatalf("expected max concurrency 1 for tenant, got %d", maxSeen.Load())
	}
}

func TestMemoryQueueTenantQueuedLimit(t *testing.T) {
	mgr := tenant.NewManager(tenant.Policy{
		MaxRunning:         2,
		MaxQueued:          1,
		RateLimitPerMinute: 100,
	})
	q := NewMemoryQueueWithConfig(MemoryQueueConfig{
		BufferSize:    10,
		MaxWorkers:    1,
		MaxAttempts:   1,
		RetryBackoff:  10 * time.Millisecond,
		DedupeWindow:  time.Second,
		TenantManager: mgr,
	})

	ctx := context.Background()
	if err := q.Enqueue(ctx, &model.SQSMessage{TenantID: "quota", TaskID: "a", RunID: "r1"}); err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}
	if err := q.Enqueue(ctx, &model.SQSMessage{TenantID: "quota", TaskID: "b", RunID: "r2"}); err == nil {
		t.Fatal("expected queued limit violation")
	}
}

func TestMemoryQueueRetryAndDeadLetter(t *testing.T) {
	q := NewMemoryQueueWithConfig(MemoryQueueConfig{
		BufferSize:    10,
		MaxWorkers:    1,
		MaxAttempts:   2,
		RetryBackoff:  20 * time.Millisecond,
		DedupeWindow:  time.Second,
		TenantManager: tenant.NewManager(tenant.Policy{MaxRunning: 2, MaxQueued: 10, RateLimitPerMinute: 100}),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := q.Enqueue(ctx, &model.SQSMessage{TenantID: "t", TaskID: "task", RunID: "run"}); err != nil {
		t.Fatal(err)
	}

	var attempts atomic.Int64
	go q.StartConsumer(ctx, func(_ context.Context, _ *model.SQSMessage) error {
		attempts.Add(1)
		return fmt.Errorf("boom")
	})

	deadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(q.DeadLetters()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	if attempts.Load() < 2 {
		t.Fatalf("expected at least 2 attempts, got %d", attempts.Load())
	}
	dlq := q.DeadLetters()
	if len(dlq) != 1 {
		t.Fatalf("expected 1 dead-letter message, got %d", len(dlq))
	}
	if dlq[0].Attempt != 2 {
		t.Fatalf("expected dead-letter attempt=2, got %d", dlq[0].Attempt)
	}
}
