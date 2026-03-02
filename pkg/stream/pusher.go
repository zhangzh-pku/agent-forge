// Package stream defines the interface for pushing events to WebSocket clients.
package stream

import (
	"context"

	"github.com/agentforge/agentforge/pkg/model"
)

// Pusher sends stream events to connected WebSocket clients.
type Pusher interface {
	// Push sends an event to a specific connection. Returns true if connection is alive.
	Push(ctx context.Context, connectionID string, event *model.StreamEvent) (alive bool, err error)
}
