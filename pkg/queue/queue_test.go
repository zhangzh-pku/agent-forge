package queue

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
)

func TestMemoryQueueEnqueueAndConsume(t *testing.T) {
	q := NewMemoryQueue(10)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msg := &model.SQSMessage{TenantID: "tnt_1", TaskID: "task_1", RunID: "run_1", SubmittedAt: time.Now().Unix(), Attempt: 1}
	if err := q.Enqueue(ctx, msg); err != nil {
		t.Fatal(err)
	}

	var received atomic.Int64
	go q.StartConsumer(ctx, func(_ context.Context, m *model.SQSMessage) error {
		if m.TaskID != "task_1" {
			t.Errorf("expected task_1, got %s", m.TaskID)
		}
		received.Add(1)
		cancel()
		return nil
	})

	<-ctx.Done()
	if received.Load() != 1 {
		t.Fatalf("expected 1 message consumed, got %d", received.Load())
	}
}

func TestMemoryQueueMultipleMessages(t *testing.T) {
	q := NewMemoryQueue(10)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for i := 0; i < 3; i++ {
		q.Enqueue(ctx, &model.SQSMessage{TaskID: "task_" + string(rune('a'+i)), RunID: "run_1", Attempt: 1})
	}

	var count atomic.Int64
	go q.StartConsumer(ctx, func(_ context.Context, m *model.SQSMessage) error {
		if count.Add(1) >= 3 {
			cancel()
		}
		return nil
	})

	<-ctx.Done()
	if count.Load() != 3 {
		t.Fatalf("expected 3 messages, got %d", count.Load())
	}
}

func TestMemoryQueueContextCancel(t *testing.T) {
	q := NewMemoryQueue(10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	err := q.StartConsumer(ctx, func(_ context.Context, m *model.SQSMessage) error {
		t.Fatal("handler should not be called")
		return nil
	})

	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestMemoryQueueDefaultBufSize(t *testing.T) {
	q := NewMemoryQueue(0) // Should default to 100.
	if cap(q.ch) != 100 {
		t.Fatalf("expected buffer size 100, got %d", cap(q.ch))
	}
}

func TestMemoryQueueEnqueueContextCancel(t *testing.T) {
	q := NewMemoryQueue(1)
	ctx := context.Background()

	// Fill the buffer.
	q.Enqueue(ctx, &model.SQSMessage{TaskID: "task_1", RunID: "run_1", Attempt: 1})

	// Now enqueue with a cancelled context should return error.
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := q.Enqueue(cancelCtx, &model.SQSMessage{TaskID: "task_2", RunID: "run_2", Attempt: 1})
	if err == nil {
		t.Fatal("expected error when enqueuing with cancelled context on full queue")
	}
}
