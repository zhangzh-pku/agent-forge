package stream

import (
	"context"
	"sync"

	"github.com/agentforge/agentforge/pkg/model"
)

// MockPusher records all pushed events for testing.
type MockPusher struct {
	mu     sync.Mutex
	events []*PushedEvent
	// GoneConnections simulates connections that have disconnected (410 Gone).
	GoneConnections map[string]bool
}

// PushedEvent records a single push.
type PushedEvent struct {
	ConnectionID string
	Event        *model.StreamEvent
}

// NewMockPusher creates a new mock pusher.
func NewMockPusher() *MockPusher {
	return &MockPusher{
		GoneConnections: make(map[string]bool),
	}
}

func (p *MockPusher) Push(_ context.Context, connectionID string, event *model.StreamEvent) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.GoneConnections[connectionID] {
		return false, nil
	}

	p.events = append(p.events, &PushedEvent{
		ConnectionID: connectionID,
		Event:        event,
	})
	return true, nil
}

// Events returns all recorded events.
func (p *MockPusher) Events() []*PushedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*PushedEvent, len(p.events))
	copy(out, p.events)
	return out
}

// Reset clears recorded events.
func (p *MockPusher) Reset() {
	p.mu.Lock()
	p.events = nil
	p.mu.Unlock()
}
