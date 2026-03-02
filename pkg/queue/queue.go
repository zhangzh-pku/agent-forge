// Package queue defines the interface for task message queuing.
package queue

import (
	"context"

	"github.com/agentforge/agentforge/pkg/model"
)

// MessageHandler processes a dequeued message. Return nil to ack, error to nack.
type MessageHandler func(ctx context.Context, msg *model.SQSMessage) error

// Queue is the interface for enqueueing and consuming task messages.
type Queue interface {
	// Enqueue publishes a task pointer message.
	Enqueue(ctx context.Context, msg *model.SQSMessage) error
	// StartConsumer begins consuming messages, calling handler for each.
	// It blocks until ctx is cancelled.
	StartConsumer(ctx context.Context, handler MessageHandler) error
}
