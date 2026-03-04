package queue

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/tenant"
)

const defaultRetryBackoff = 250 * time.Millisecond

// MemoryQueueConfig controls MemoryQueue behavior.
type MemoryQueueConfig struct {
	BufferSize   int
	MaxWorkers   int
	MaxAttempts  int
	RetryBackoff time.Duration
	DedupeWindow time.Duration

	// TenantManager controls per-tenant limits, budgets, and alerts.
	// If nil, a default manager is used.
	TenantManager *tenant.Manager
}

// DefaultMemoryQueueConfig returns defaults tuned for local dev/testing.
func DefaultMemoryQueueConfig() MemoryQueueConfig {
	return MemoryQueueConfig{
		BufferSize:   100,
		MaxWorkers:   1,
		MaxAttempts:  3,
		RetryBackoff: defaultRetryBackoff,
		DedupeWindow: 2 * time.Minute,
		TenantManager: tenant.NewManager(tenant.Policy{
			MaxRunning:           8,
			MaxQueued:            2048,
			RateLimitPerMinute:   1200,
			MaxConsecutiveErrors: 8,
			BreakerCooldown:      30 * time.Second,
		}),
	}
}

// MemoryQueue is an in-memory queue with fair multi-tenant scheduling.
type MemoryQueue struct {
	// ch remains exposed for compatibility with existing tests.
	ch chan *model.SQSMessage

	cfg MemoryQueueConfig

	mu sync.Mutex
	// pending is a per-tenant FIFO buffer.
	pending map[string][]*model.SQSMessage
	// tenantOrder is a ring used for round-robin fairness.
	tenantOrder []string
	nextTenant  int

	// inQueue tracks keys already accepted to prevent duplicates.
	inQueue map[string]struct{}
	// inFlight tracks currently executing message keys.
	inFlight map[string]struct{}
	// recent deduplicates recently completed messages.
	recent map[string]time.Time

	dlq []*model.SQSMessage
}

// HealthCheck always succeeds for in-memory local queue.
func (q *MemoryQueue) HealthCheck(_ context.Context) error {
	return nil
}

// NewMemoryQueue creates a queue with default config and the provided buffer size.
func NewMemoryQueue(bufSize int) *MemoryQueue {
	cfg := DefaultMemoryQueueConfig()
	if bufSize > 0 {
		cfg.BufferSize = bufSize
	}
	return NewMemoryQueueWithConfig(cfg)
}

// NewMemoryQueueWithConfig creates a queue with explicit config.
func NewMemoryQueueWithConfig(cfg MemoryQueueConfig) *MemoryQueue {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 100
	}
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 1
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = defaultRetryBackoff
	}
	if cfg.DedupeWindow <= 0 {
		cfg.DedupeWindow = 2 * time.Minute
	}
	if cfg.TenantManager == nil {
		def := DefaultMemoryQueueConfig()
		cfg.TenantManager = def.TenantManager
	}

	return &MemoryQueue{
		ch:       make(chan *model.SQSMessage, cfg.BufferSize),
		cfg:      cfg,
		pending:  make(map[string][]*model.SQSMessage),
		inQueue:  make(map[string]struct{}),
		inFlight: make(map[string]struct{}),
		recent:   make(map[string]time.Time),
	}
}

func (q *MemoryQueue) Enqueue(ctx context.Context, msg *model.SQSMessage) error {
	if msg == nil {
		return fmt.Errorf("queue: enqueue: nil message")
	}
	cp := cloneMessage(msg)
	if cp.Attempt <= 0 {
		cp.Attempt = 1
	}
	tenantID := normalizeTenant(cp.TenantID)
	cp.TenantID = tenantID
	key := messageKey(cp)
	now := time.Now().UTC()

	q.mu.Lock()
	q.pruneRecentLocked(now)
	if _, exists := q.inQueue[key]; exists {
		q.mu.Unlock()
		return nil
	}
	if _, exists := q.inFlight[key]; exists {
		q.mu.Unlock()
		return nil
	}
	if ts, exists := q.recent[key]; exists && now.Sub(ts) <= q.cfg.DedupeWindow {
		q.mu.Unlock()
		return nil
	}
	q.inQueue[key] = struct{}{}
	q.mu.Unlock()

	if q.cfg.TenantManager != nil {
		if err := q.cfg.TenantManager.TryEnqueue(tenantID, tenant.AdmissionInfo{
			MessageKey:      key,
			EstimatedTokens: cp.EstimatedTokens,
			EstimatedCost:   cp.EstimatedCost,
		}); err != nil {
			q.mu.Lock()
			delete(q.inQueue, key)
			q.mu.Unlock()
			return err
		}
	}

	select {
	case q.ch <- cp:
		return nil
	case <-ctx.Done():
		q.mu.Lock()
		delete(q.inQueue, key)
		q.mu.Unlock()
		if q.cfg.TenantManager != nil {
			q.cfg.TenantManager.CancelEnqueue(tenantID, key)
		}
		return fmt.Errorf("queue: enqueue: %w", ctx.Err())
	}
}

func (q *MemoryQueue) StartConsumer(ctx context.Context, handler MessageHandler) error {
	if handler == nil {
		return fmt.Errorf("queue: nil handler")
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		q.ingressLoop(ctx)
	}()

	for i := 0; i < q.cfg.MaxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q.workerLoop(ctx, handler)
		}()
	}

	<-ctx.Done()
	wg.Wait()
	return ctx.Err()
}

// DeadLetters returns a snapshot of the dead-letter queue.
func (q *MemoryQueue) DeadLetters() []*model.SQSMessage {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]*model.SQSMessage, len(q.dlq))
	for i, msg := range q.dlq {
		out[i] = cloneMessage(msg)
	}
	return out
}

// RedriveDeadLetters re-enqueues dead-letter messages and returns the count.
func (q *MemoryQueue) RedriveDeadLetters(ctx context.Context, limit int) (int, error) {
	q.mu.Lock()
	if limit <= 0 || limit > len(q.dlq) {
		limit = len(q.dlq)
	}
	batch := make([]*model.SQSMessage, limit)
	for i := 0; i < limit; i++ {
		batch[i] = cloneMessage(q.dlq[i])
	}
	q.dlq = append([]*model.SQSMessage(nil), q.dlq[limit:]...)
	q.mu.Unlock()

	var ok int
	for _, msg := range batch {
		msg.Attempt = 1
		if err := q.Enqueue(ctx, msg); err != nil {
			// Put failed messages back to DLQ tail.
			q.mu.Lock()
			q.dlq = append(q.dlq, cloneMessage(msg))
			q.mu.Unlock()
			return ok, err
		}
		ok++
	}
	return ok, nil
}

// TenantSnapshots returns runtime snapshots for all tenants.
func (q *MemoryQueue) TenantSnapshots() []tenant.RuntimeSnapshot {
	if q.cfg.TenantManager == nil {
		return nil
	}
	return q.cfg.TenantManager.Snapshots()
}

// TenantSnapshot returns one tenant runtime snapshot.
func (q *MemoryQueue) TenantSnapshot(tenantID string) tenant.RuntimeSnapshot {
	if q.cfg.TenantManager == nil {
		return tenant.RuntimeSnapshot{}
	}
	return q.cfg.TenantManager.Snapshot(normalizeTenant(tenantID))
}

// TenantAlerts returns recent alerts for one tenant.
func (q *MemoryQueue) TenantAlerts(tenantID string, limit int) []tenant.Alert {
	if q.cfg.TenantManager == nil {
		return nil
	}
	return q.cfg.TenantManager.Alerts(normalizeTenant(tenantID), limit)
}

func (q *MemoryQueue) ingressLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-q.ch:
			q.mu.Lock()
			if msg != nil {
				q.addPendingLocked(msg)
			}
			// Drain buffered messages as one batch so workers observe a stable
			// pending set and fairness is less sensitive to goroutine timing.
			for {
				select {
				case next := <-q.ch:
					if next != nil {
						q.addPendingLocked(next)
					}
				default:
					q.mu.Unlock()
					goto drained
				}
			}
		drained:
		}
	}
}

func (q *MemoryQueue) workerLoop(ctx context.Context, handler MessageHandler) {
	for {
		msg, ok := q.nextDispatchable(ctx)
		if !ok {
			return
		}
		err := handler(ctx, cloneMessage(msg))
		q.finishAttempt(ctx, msg, err)
	}
}

func (q *MemoryQueue) addPendingLocked(msg *model.SQSMessage) {
	tenantID := normalizeTenant(msg.TenantID)
	if _, ok := q.pending[tenantID]; !ok {
		q.tenantOrder = append(q.tenantOrder, tenantID)
	}
	q.pending[tenantID] = append(q.pending[tenantID], cloneMessage(msg))
}

func (q *MemoryQueue) nextDispatchable(ctx context.Context) (*model.SQSMessage, bool) {
	for {
		if ctx.Err() != nil {
			return nil, false
		}

		q.mu.Lock()
		msg, hadPending := q.popEligibleLocked()
		q.mu.Unlock()
		if msg != nil {
			return msg, true
		}

		// If there are pending messages but all are currently blocked by tenant
		// limits/breakers, wait a bit and retry.
		if hadPending {
			select {
			case <-ctx.Done():
				return nil, false
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}

		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (q *MemoryQueue) popEligibleLocked() (*model.SQSMessage, bool) {
	if len(q.tenantOrder) == 0 {
		return nil, false
	}

	checked := 0
	for checked < len(q.tenantOrder) && len(q.tenantOrder) > 0 {
		idx := q.nextTenant % len(q.tenantOrder)
		tenantID := q.tenantOrder[idx]
		tenantQueue := q.pending[tenantID]
		if len(tenantQueue) == 0 {
			delete(q.pending, tenantID)
			q.removeTenantLocked(idx)
			continue
		}

		msg := tenantQueue[0]
		if q.cfg.TenantManager != nil {
			if err := q.cfg.TenantManager.TryStartRun(tenantID); err != nil {
				q.nextTenant = (idx + 1) % len(q.tenantOrder)
				checked++
				continue
			}
		}

		q.pending[tenantID] = tenantQueue[1:]
		if len(q.pending[tenantID]) == 0 {
			delete(q.pending, tenantID)
			q.removeTenantLocked(idx)
		} else {
			q.nextTenant = (idx + 1) % len(q.tenantOrder)
		}

		key := messageKey(msg)
		delete(q.inQueue, key)
		q.inFlight[key] = struct{}{}
		return msg, true
	}

	return nil, true
}

func (q *MemoryQueue) removeTenantLocked(idx int) {
	if idx < 0 || idx >= len(q.tenantOrder) {
		return
	}
	q.tenantOrder = append(q.tenantOrder[:idx], q.tenantOrder[idx+1:]...)
	if len(q.tenantOrder) == 0 {
		q.nextTenant = 0
		return
	}
	if q.nextTenant >= len(q.tenantOrder) {
		q.nextTenant = 0
	}
}

func (q *MemoryQueue) finishAttempt(ctx context.Context, msg *model.SQSMessage, err error) {
	key := messageKey(msg)
	finalFailure := err != nil && msg.Attempt >= q.cfg.MaxAttempts

	if q.cfg.TenantManager != nil {
		q.cfg.TenantManager.CompleteRun(msg.TenantID, tenant.CompletionInfo{
			MessageKey:  key,
			TokensUsed:  msg.EstimatedTokens,
			CostUsed:    msg.EstimatedCost,
			Failed:      err != nil,
			WasAccepted: err == nil || finalFailure,
		})
		if finalFailure {
			q.cfg.TenantManager.MarkDeadLetter(msg.TenantID, key)
		}
	}

	q.mu.Lock()
	delete(q.inFlight, key)
	now := time.Now().UTC()
	if err == nil || finalFailure {
		q.recent[key] = now
	}
	q.pruneRecentLocked(now)
	if finalFailure {
		q.dlq = append(q.dlq, cloneMessage(msg))
	}
	q.mu.Unlock()

	if err == nil {
		return
	}

	if finalFailure {
		log.Printf("queue: moved message to DLQ after %d attempts: task=%s run=%s err=%v", msg.Attempt, msg.TaskID, msg.RunID, err)
		return
	}

	retry := cloneMessage(msg)
	retry.Attempt++
	delay := q.cfg.RetryBackoff * time.Duration(retry.Attempt-1)
	if delay <= 0 {
		delay = q.cfg.RetryBackoff
	}

	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if enqueueErr := q.Enqueue(ctx, retry); enqueueErr != nil {
				log.Printf("queue: retry enqueue failed task=%s run=%s attempt=%d: %v", retry.TaskID, retry.RunID, retry.Attempt, enqueueErr)
			}
		}
	}()
}

func (q *MemoryQueue) pruneRecentLocked(now time.Time) {
	if len(q.recent) == 0 {
		return
	}
	for k, ts := range q.recent {
		if now.Sub(ts) > q.cfg.DedupeWindow {
			delete(q.recent, k)
		}
	}
}

func normalizeTenant(tenantID string) string {
	if tenantID == "" {
		return "default"
	}
	return tenantID
}

func messageKey(msg *model.SQSMessage) string {
	if msg == nil {
		return ""
	}
	tenantID := normalizeTenant(msg.TenantID)
	if msg.DedupeKey != "" {
		return tenantID + "#" + msg.DedupeKey
	}
	return tenantID + "#" + msg.TaskID + "#" + msg.RunID
}

func cloneMessage(msg *model.SQSMessage) *model.SQSMessage {
	if msg == nil {
		return nil
	}
	cp := *msg
	return &cp
}
