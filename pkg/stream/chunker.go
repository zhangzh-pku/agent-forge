package stream

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
)

// ChunkerConfig controls the flush strategy.
type ChunkerConfig struct {
	// FlushInterval is the maximum time before flushing buffered data.
	FlushInterval time.Duration
	// FlushBytes is the maximum byte size before flushing.
	FlushBytes int
}

// DefaultChunkerConfig returns sensible defaults: 100ms or 2KB.
func DefaultChunkerConfig() ChunkerConfig {
	return ChunkerConfig{
		FlushInterval: 100 * time.Millisecond,
		FlushBytes:    2048,
	}
}

// FlushFunc is called when the chunker flushes accumulated data.
type FlushFunc func(data []byte) error

// Chunker buffers stream data and flushes by time or byte threshold.
type Chunker struct {
	cfg     ChunkerConfig
	flush   FlushFunc
	mu      sync.Mutex
	buf     []byte
	timer   *time.Timer
	stopped bool
	lastErr error // last flush error, for observability
}

// NewChunker creates a new chunker with the given flush function.
func NewChunker(cfg ChunkerConfig, flush FlushFunc) *Chunker {
	c := &Chunker{
		cfg:   cfg,
		flush: flush,
	}
	return c
}

// Write appends data to the buffer. Flushes if byte threshold is reached.
func (c *Chunker) Write(event *model.StreamEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stopped {
		return nil
	}

	c.buf = append(c.buf, data...)

	// Start timer on first write.
	if c.timer == nil {
		c.timer = time.AfterFunc(c.cfg.FlushInterval, func() {
			c.doFlush()
		})
	}

	// Flush if byte threshold met.
	if len(c.buf) >= c.cfg.FlushBytes {
		c.drainLocked()
	}

	return nil
}

// Flush forces a flush of buffered data.
func (c *Chunker) Flush() {
	c.doFlush()
}

func (c *Chunker) doFlush() {
	c.mu.Lock()
	c.drainLocked()
	c.mu.Unlock()
}

// drainLocked extracts buffered data, releases the lock, calls flush, then
// re-acquires the lock. The caller must hold c.mu and must NOT use defer
// Unlock — it should explicitly unlock after this returns (or call this as
// the last operation before its own explicit unlock).
func (c *Chunker) drainLocked() {
	if len(c.buf) == 0 {
		return
	}
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	data := make([]byte, len(c.buf))
	copy(data, c.buf)
	c.buf = c.buf[:0]
	// Release lock before calling flush to avoid blocking writes.
	c.mu.Unlock()
	if err := c.flush(data); err != nil {
		c.mu.Lock()
		c.lastErr = err
		return
	}
	c.mu.Lock()
}

// LastError returns the last flush error, if any.
func (c *Chunker) LastError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// Stop flushes remaining data and stops the chunker.
func (c *Chunker) Stop() {
	c.mu.Lock()
	c.stopped = true
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.drainLocked()
	c.mu.Unlock()
}
