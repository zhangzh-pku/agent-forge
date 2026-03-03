package queue

import (
	"context"
	"fmt"
	"log"

	"github.com/agentforge/agentforge/pkg/model"
)

// MemoryQueue is an in-memory implementation of Queue for testing and local dev.
type MemoryQueue struct {
	ch chan *model.SQSMessage
}

// NewMemoryQueue creates a new in-memory queue.
func NewMemoryQueue(bufSize int) *MemoryQueue {
	if bufSize <= 0 {
		bufSize = 100
	}
	return &MemoryQueue{
		ch: make(chan *model.SQSMessage, bufSize),
	}
}

func (q *MemoryQueue) Enqueue(ctx context.Context, msg *model.SQSMessage) error {
	select {
	case q.ch <- msg:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("queue: enqueue: %w", ctx.Err())
	}
}

func (q *MemoryQueue) StartConsumer(ctx context.Context, handler MessageHandler) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg := <-q.ch:
			if err := handler(ctx, msg); err != nil {
				// In a real SQS consumer, this would nack. Here we just log/skip.
				log.Printf("queue: handler error for task=%s run=%s: %v", msg.TaskID, msg.RunID, err)
				continue
			}
		}
	}
}
