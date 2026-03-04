package stream

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
)

// ErrConnectionGone is a sentinel error indicating the WebSocket connection
// no longer exists (HTTP 410 Gone from API Gateway).
var ErrConnectionGone = &GoneError{}

// GoneError indicates a connection has been removed.
type GoneError struct{}

func (e *GoneError) Error() string { return "connection gone (410)" }

// IsGoneError checks if an error indicates a 410 Gone connection.
// It matches the sentinel GoneError and common AWS SDK error patterns.
func IsGoneError(err error) bool {
	if err == nil {
		return false
	}
	// Match our sentinel type.
	if _, ok := err.(*GoneError); ok {
		return true
	}
	// Match common AWS SDK error patterns for GoneException / 410 Gone status.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "goneexception") ||
		strings.Contains(msg, "status code: 410") ||
		strings.Contains(msg, "410 gone")
}

// AWSPusher sends events via API Gateway Management API (PostToConnection).
// In production, this would use the AWS SDK v2 apigatewaymanagementapi client.
// This implementation provides the structure; actual AWS calls require deployment.
type AWSPusher struct {
	// endpoint is the API Gateway WebSocket management endpoint.
	endpoint string
	// postFunc is the actual PostToConnection implementation.
	// In production: apigatewaymanagementapi.Client.PostToConnection
	// For testing: can be replaced with a mock.
	postFunc func(ctx context.Context, connectionID string, data []byte) error

	// chunkCfg enables time/byte chunked pushes when non-nil.
	chunkCfg *ChunkerConfig

	mu              sync.Mutex
	chunkers        map[string]*connectionChunker
	deadConnections map[string]deadConnectionEntry
	deadTTL         time.Duration
	deadLimit       int
	now             func() time.Time
}

type deadConnectionEntry struct {
	markedAt  time.Time
	expiresAt time.Time
}

const (
	defaultDeadConnectionTTL   = 15 * time.Minute
	defaultDeadConnectionLimit = 10000
)

// NewAWSPusher creates a new AWS API Gateway pusher.
// postFunc should wrap apigatewaymanagementapi.Client.PostToConnection.
func NewAWSPusher(endpoint string, postFunc func(ctx context.Context, connectionID string, data []byte) error) *AWSPusher {
	return &AWSPusher{
		endpoint: endpoint,
		postFunc: postFunc,
	}
}

// NewChunkedAWSPusher creates an AWS pusher that batches events per connection
// and flushes by time or byte threshold.
func NewChunkedAWSPusher(endpoint string, cfg ChunkerConfig, postFunc func(ctx context.Context, connectionID string, data []byte) error) *AWSPusher {
	return &AWSPusher{
		endpoint:        endpoint,
		postFunc:        postFunc,
		chunkCfg:        &cfg,
		chunkers:        make(map[string]*connectionChunker),
		deadConnections: make(map[string]deadConnectionEntry),
		deadTTL:         defaultDeadConnectionTTL,
		deadLimit:       defaultDeadConnectionLimit,
		now:             func() time.Time { return time.Now().UTC() },
	}
}

func (p *AWSPusher) Push(ctx context.Context, connectionID string, event *model.StreamEvent) (bool, error) {
	if p.chunkCfg != nil {
		return p.pushChunked(ctx, connectionID, event)
	}

	data, err := json.Marshal(event)
	if err != nil {
		return false, err
	}

	if err := p.postFunc(ctx, connectionID, data); err != nil {
		if IsGoneError(err) {
			// Connection stale — return alive=false so caller can clean up.
			return false, nil
		}
		// Transient error — return alive=true so connection is NOT removed.
		return true, err
	}
	return true, nil
}

func (p *AWSPusher) pushChunked(ctx context.Context, connectionID string, event *model.StreamEvent) (bool, error) {
	cc := p.getOrCreateChunker(connectionID)
	if cc == nil {
		return false, nil
	}
	cc.setContext(ctx)

	if err := cc.chunker.Write(event); err != nil {
		return true, err
	}

	// Flush terminal events immediately so clients receive completion quickly.
	if event.Type == model.StreamEventComplete || event.Type == model.StreamEventError {
		cc.chunker.Flush()
	}

	if err := cc.chunker.LastError(); err != nil {
		if IsGoneError(err) {
			p.markDead(connectionID)
			return false, nil
		}
		return true, err
	}
	if p.isDead(connectionID) {
		return false, nil
	}
	return true, nil
}

func (p *AWSPusher) getOrCreateChunker(connectionID string) *connectionChunker {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.nowUTC()
	if p.isDeadLocked(connectionID, now) {
		return nil
	}
	if c, ok := p.chunkers[connectionID]; ok {
		return c
	}

	cc := &connectionChunker{}
	cfg := *p.chunkCfg
	cc.chunker = NewChunker(cfg, func(data []byte) error {
		pushCtx := cc.context()
		if pushCtx == nil {
			pushCtx = context.Background()
		}
		err := p.postFunc(pushCtx, connectionID, data)
		if err != nil && IsGoneError(err) {
			p.markDead(connectionID)
		}
		return err
	})
	p.chunkers[connectionID] = cc
	return cc
}

func (p *AWSPusher) isDead(connectionID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.isDeadLocked(connectionID, p.nowUTC())
}

func (p *AWSPusher) markDead(connectionID string) {
	var cc *connectionChunker
	p.mu.Lock()
	now := p.nowUTC()
	if p.isDeadLocked(connectionID, now) {
		p.mu.Unlock()
		return
	}
	p.deadConnections[connectionID] = deadConnectionEntry{
		markedAt:  now,
		expiresAt: now.Add(p.deadTTL),
	}
	p.pruneDeadConnectionsLocked(now)
	cc = p.chunkers[connectionID]
	delete(p.chunkers, connectionID)
	p.mu.Unlock()

	if cc != nil {
		cc.chunker.Stop()
	}
}

func (p *AWSPusher) isDeadLocked(connectionID string, now time.Time) bool {
	entry, ok := p.deadConnections[connectionID]
	if !ok {
		return false
	}
	if !entry.expiresAt.After(now) {
		delete(p.deadConnections, connectionID)
		return false
	}
	return true
}

func (p *AWSPusher) pruneDeadConnectionsLocked(now time.Time) {
	for id, entry := range p.deadConnections {
		if !entry.expiresAt.After(now) {
			delete(p.deadConnections, id)
		}
	}
	if len(p.deadConnections) <= p.deadLimit {
		return
	}
	for len(p.deadConnections) > p.deadLimit {
		var oldestID string
		var oldestAt time.Time
		first := true
		for id, entry := range p.deadConnections {
			if first || entry.markedAt.Before(oldestAt) {
				oldestID = id
				oldestAt = entry.markedAt
				first = false
			}
		}
		if first {
			return
		}
		delete(p.deadConnections, oldestID)
	}
}

func (p *AWSPusher) nowUTC() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now().UTC()
}

type connectionChunker struct {
	chunker *Chunker
	ctxMu   sync.RWMutex
	ctx     context.Context
}

func (c *connectionChunker) setContext(ctx context.Context) {
	c.ctxMu.Lock()
	c.ctx = ctx
	c.ctxMu.Unlock()
}

func (c *connectionChunker) context() context.Context {
	c.ctxMu.RLock()
	defer c.ctxMu.RUnlock()
	return c.ctx
}
