// Package tenant manages per-tenant runtime policy, limits, and metrics.
package tenant

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

const (
	// Default policy values.
	defaultMaxRunning         = 4
	defaultMaxQueued          = 128
	defaultRatePerMinute      = 240
	defaultBreakerErrors      = 5
	defaultBreakerCoolDownSec = 60
)

// BreakerState indicates tenant circuit breaker state.
type BreakerState string

const (
	BreakerClosed   BreakerState = "closed"
	BreakerOpen     BreakerState = "open"
	BreakerHalfOpen BreakerState = "half_open"
)

// AlertType indicates the type of runtime alert.
type AlertType string

const (
	AlertTypeQueueDepth    AlertType = "queue_depth"
	AlertTypeErrorBurst    AlertType = "error_burst"
	AlertTypeBudgetBreach  AlertType = "budget_breach"
	AlertTypeRateLimited   AlertType = "rate_limited"
	AlertTypeBreakerOpened AlertType = "breaker_opened"
	AlertTypeBreakerClosed AlertType = "breaker_closed"
)

// Policy defines per-tenant limits and controls.
type Policy struct {
	MaxRunning int // max concurrent running tasks
	MaxQueued  int // max queued tasks

	// RateLimitPerMinute limits accepted enqueue requests per rolling minute.
	RateLimitPerMinute int

	// MaxConsecutiveErrors trips the breaker when exceeded.
	MaxConsecutiveErrors int
	BreakerCooldown      time.Duration

	// TokenBudget and CostBudgetUSD are soft/hard caps for cumulative usage.
	// 0 means unlimited.
	TokenBudget int64
	CostBudget  float64

	// Alert thresholds.
	AlertQueueDepth int
	AlertErrorBurst int
	AlertBudgetPct  float64 // e.g. 0.9 for 90%
}

// AdmissionInfo captures message-level estimates for budget gating.
type AdmissionInfo struct {
	MessageKey      string
	EstimatedTokens int64
	EstimatedCost   float64
}

// CompletionInfo captures final run usage for budget accounting.
type CompletionInfo struct {
	MessageKey  string
	TokensUsed  int64
	CostUsed    float64
	Failed      bool
	WasAccepted bool
}

// RuntimeSnapshot is the tenant runtime view exposed to operators.
type RuntimeSnapshot struct {
	TenantID string `json:"tenant_id"`

	Running int64 `json:"running"`
	Queued  int64 `json:"queued"`

	Enqueued   int64 `json:"enqueued"`
	Dequeued   int64 `json:"dequeued"`
	Completed  int64 `json:"completed"`
	Failed     int64 `json:"failed"`
	DeadLetter int64 `json:"dead_letter"`

	ConsecutiveErrors int64  `json:"consecutive_errors"`
	BreakerState      string `json:"breaker_state"`
	BreakerOpenedAt   int64  `json:"breaker_opened_at,omitempty"`

	RateLimited int64 `json:"rate_limited"`

	TokensUsed    int64   `json:"tokens_used"`
	CostUsedUSD   float64 `json:"cost_used_usd"`
	TokenBudget   int64   `json:"token_budget"`
	CostBudgetUSD float64 `json:"cost_budget_usd"`

	ReservedTokens int64   `json:"reserved_tokens"`
	ReservedCost   float64 `json:"reserved_cost_usd"`
}

// Alert is an operational alert emitted by runtime policy.
type Alert struct {
	TenantID string    `json:"tenant_id"`
	Type     AlertType `json:"type"`
	Message  string    `json:"message"`
	TS       time.Time `json:"ts"`
}

// Manager keeps per-tenant policy state.
type Manager struct {
	mu            sync.Mutex
	defaultPolicy Policy
	policies      map[string]Policy
	states        map[string]*tenantState
	now           func() time.Time
}

type tenantState struct {
	running int64
	queued  int64

	enqueued   int64
	dequeued   int64
	completed  int64
	failed     int64
	deadLetter int64

	consecutiveErrors int64
	breakerState      BreakerState
	breakerOpenedAt   time.Time
	halfOpenInFlight  bool

	rateLimited int64

	tokensUsed int64
	costUsed   float64

	// Reserved budgets for queued/running messages.
	reservedTokens int64
	reservedCost   float64
	reservations   map[string]AdmissionInfo

	// Accepted enqueue timestamps for rolling per-minute rate limiting.
	enqWindow []time.Time

	alerts []Alert
}

// ErrPolicyViolation indicates a policy limit was hit.
type ErrPolicyViolation struct {
	Reason string
}

func (e *ErrPolicyViolation) Error() string {
	return e.Reason
}

// NewManager constructs a tenant policy manager.
func NewManager(defaultPolicy Policy) *Manager {
	return &Manager{
		defaultPolicy: normalizePolicy(defaultPolicy),
		policies:      make(map[string]Policy),
		states:        make(map[string]*tenantState),
		now:           func() time.Time { return time.Now().UTC() },
	}
}

// SetPolicy sets a tenant-specific policy override.
func (m *Manager) SetPolicy(tenantID string, policy Policy) {
	if tenantID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policies[tenantID] = normalizePolicy(policy)
}

// GetPolicy returns the effective policy for a tenant.
func (m *Manager) GetPolicy(tenantID string) Policy {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.policyLocked(tenantID)
}

// TryEnqueue validates enqueue admission and reserves budget.
func (m *Manager) TryEnqueue(tenantID string, info AdmissionInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pol := m.policyLocked(tenantID)
	st := m.stateLocked(tenantID)
	now := m.now()
	m.maybeRecoverBreakerLocked(st, pol, tenantID, now)

	if st.breakerState == BreakerOpen {
		return &ErrPolicyViolation{Reason: "tenant breaker is open"}
	}
	if pol.MaxQueued > 0 && st.queued >= int64(pol.MaxQueued) {
		m.emitAlertLocked(st, tenantID, AlertTypeQueueDepth, fmt.Sprintf("queue depth=%d exceeds limit=%d", st.queued, pol.MaxQueued))
		return &ErrPolicyViolation{Reason: "tenant queued limit exceeded"}
	}

	st.enqWindow = pruneOld(st.enqWindow, now.Add(-time.Minute))
	if pol.RateLimitPerMinute > 0 && len(st.enqWindow) >= pol.RateLimitPerMinute {
		st.rateLimited++
		m.emitAlertLocked(st, tenantID, AlertTypeRateLimited, fmt.Sprintf("rate limited: %d requests/min", pol.RateLimitPerMinute))
		return &ErrPolicyViolation{Reason: "tenant rate limit exceeded"}
	}

	estTokens := max64(0, info.EstimatedTokens)
	estCost := maxFloat(0, info.EstimatedCost)

	if pol.TokenBudget > 0 && st.tokensUsed+st.reservedTokens+estTokens > pol.TokenBudget {
		m.openBreakerLocked(st, tenantID, fmt.Sprintf("token budget exceeded: used=%d reserved=%d incoming=%d budget=%d",
			st.tokensUsed, st.reservedTokens, estTokens, pol.TokenBudget))
		return &ErrPolicyViolation{Reason: "tenant token budget exceeded"}
	}
	if pol.CostBudget > 0 && st.costUsed+st.reservedCost+estCost > pol.CostBudget {
		m.openBreakerLocked(st, tenantID, fmt.Sprintf("cost budget exceeded: used=%.6f reserved=%.6f incoming=%.6f budget=%.6f",
			st.costUsed, st.reservedCost, estCost, pol.CostBudget))
		return &ErrPolicyViolation{Reason: "tenant cost budget exceeded"}
	}

	st.queued++
	st.enqueued++
	st.enqWindow = append(st.enqWindow, now)

	if info.MessageKey != "" {
		if st.reservations == nil {
			st.reservations = make(map[string]AdmissionInfo)
		}
		// Idempotent reservation update for duplicate enqueue.
		old, exists := st.reservations[info.MessageKey]
		if exists {
			st.reservedTokens -= old.EstimatedTokens
			st.reservedCost -= old.EstimatedCost
		}
		st.reservations[info.MessageKey] = AdmissionInfo{
			MessageKey:      info.MessageKey,
			EstimatedTokens: estTokens,
			EstimatedCost:   estCost,
		}
		st.reservedTokens += estTokens
		st.reservedCost += estCost
	}

	if pol.AlertQueueDepth > 0 && int(st.queued) >= pol.AlertQueueDepth {
		m.emitAlertLocked(st, tenantID, AlertTypeQueueDepth, fmt.Sprintf("queue depth reached %d", st.queued))
	}
	m.emitBudgetAlertsLocked(st, pol, tenantID)
	return nil
}

// TryStartRun checks running concurrency and breaker status, then marks a run as active.
func (m *Manager) TryStartRun(tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pol := m.policyLocked(tenantID)
	st := m.stateLocked(tenantID)
	now := m.now()
	m.maybeRecoverBreakerLocked(st, pol, tenantID, now)

	switch st.breakerState {
	case BreakerOpen:
		return &ErrPolicyViolation{Reason: "tenant breaker is open"}
	case BreakerHalfOpen:
		if st.halfOpenInFlight {
			return &ErrPolicyViolation{Reason: "tenant breaker is half-open and already probing"}
		}
		st.halfOpenInFlight = true
	}

	if pol.MaxRunning > 0 && st.running >= int64(pol.MaxRunning) {
		if st.breakerState == BreakerHalfOpen {
			st.halfOpenInFlight = false
		}
		return &ErrPolicyViolation{Reason: "tenant running limit exceeded"}
	}

	st.running++
	if st.queued > 0 {
		st.queued--
	}
	st.dequeued++
	return nil
}

// CompleteRun records completion metrics and reconciles budget reservation.
func (m *Manager) CompleteRun(tenantID string, info CompletionInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()

	pol := m.policyLocked(tenantID)
	st := m.stateLocked(tenantID)

	if st.running > 0 {
		st.running--
	}

	if info.MessageKey != "" {
		if reserved, ok := st.reservations[info.MessageKey]; ok {
			st.reservedTokens -= reserved.EstimatedTokens
			st.reservedCost -= reserved.EstimatedCost
			delete(st.reservations, info.MessageKey)
		}
	}

	if info.WasAccepted {
		st.tokensUsed += max64(0, info.TokensUsed)
		st.costUsed += maxFloat(0, info.CostUsed)
	}

	if info.Failed {
		st.failed++
		st.consecutiveErrors++
		if pol.MaxConsecutiveErrors > 0 && st.consecutiveErrors >= int64(pol.MaxConsecutiveErrors) {
			m.openBreakerLocked(st, tenantID, fmt.Sprintf("error threshold reached: %d consecutive failures", st.consecutiveErrors))
		}
		if pol.AlertErrorBurst > 0 && int(st.consecutiveErrors) >= pol.AlertErrorBurst {
			m.emitAlertLocked(st, tenantID, AlertTypeErrorBurst, fmt.Sprintf("consecutive failures reached %d", st.consecutiveErrors))
		}
	} else {
		st.completed++
		st.consecutiveErrors = 0
		if st.breakerState == BreakerHalfOpen {
			st.breakerState = BreakerClosed
			st.halfOpenInFlight = false
			st.breakerOpenedAt = time.Time{}
			m.emitAlertLocked(st, tenantID, AlertTypeBreakerClosed, "breaker closed after successful half-open probe")
		}
	}

	if st.breakerState == BreakerHalfOpen && info.Failed {
		// Failed probe re-opens breaker.
		m.openBreakerLocked(st, tenantID, "half-open probe failed")
	}
	st.halfOpenInFlight = false

	m.emitBudgetAlertsLocked(st, pol, tenantID)
}

// CancelEnqueue rolls back queued/reservation counters for an accepted message
// that failed to enter the queue transport.
func (m *Manager) CancelEnqueue(tenantID, messageKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	st := m.stateLocked(tenantID)
	if st.queued > 0 {
		st.queued--
	}
	if messageKey != "" {
		if reserved, ok := st.reservations[messageKey]; ok {
			st.reservedTokens -= reserved.EstimatedTokens
			st.reservedCost -= reserved.EstimatedCost
			delete(st.reservations, messageKey)
		}
	}
}

// MarkDeadLetter increments DLQ counters.
func (m *Manager) MarkDeadLetter(tenantID string, messageKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	st := m.stateLocked(tenantID)
	st.deadLetter++
	if messageKey != "" {
		if reserved, ok := st.reservations[messageKey]; ok {
			st.reservedTokens -= reserved.EstimatedTokens
			st.reservedCost -= reserved.EstimatedCost
			delete(st.reservations, messageKey)
		}
	}
}

// Snapshot returns one tenant runtime snapshot.
func (m *Manager) Snapshot(tenantID string) RuntimeSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	pol := m.policyLocked(tenantID)
	st := m.stateLocked(tenantID)
	return snapshotFromState(tenantID, st, pol)
}

// Snapshots returns all tenant runtime snapshots.
func (m *Manager) Snapshots() []RuntimeSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	tenants := make([]string, 0, len(m.states))
	for tenantID := range m.states {
		tenants = append(tenants, tenantID)
	}
	sort.Strings(tenants)

	out := make([]RuntimeSnapshot, 0, len(tenants))
	for _, tenantID := range tenants {
		out = append(out, snapshotFromState(tenantID, m.states[tenantID], m.policyLocked(tenantID)))
	}
	return out
}

// Alerts returns recent alerts for the tenant.
func (m *Manager) Alerts(tenantID string, limit int) []Alert {
	m.mu.Lock()
	defer m.mu.Unlock()

	st := m.stateLocked(tenantID)
	if limit <= 0 || limit > len(st.alerts) {
		limit = len(st.alerts)
	}
	if limit == 0 {
		return nil
	}
	start := len(st.alerts) - limit
	out := make([]Alert, limit)
	copy(out, st.alerts[start:])
	return out
}

func (m *Manager) policyLocked(tenantID string) Policy {
	if p, ok := m.policies[tenantID]; ok {
		return p
	}
	return m.defaultPolicy
}

func (m *Manager) stateLocked(tenantID string) *tenantState {
	st, ok := m.states[tenantID]
	if !ok {
		st = &tenantState{
			breakerState: BreakerClosed,
			reservations: make(map[string]AdmissionInfo),
		}
		m.states[tenantID] = st
	}
	return st
}

func snapshotFromState(tenantID string, st *tenantState, pol Policy) RuntimeSnapshot {
	out := RuntimeSnapshot{
		TenantID: tenantID,

		Running: st.running,
		Queued:  st.queued,

		Enqueued:   st.enqueued,
		Dequeued:   st.dequeued,
		Completed:  st.completed,
		Failed:     st.failed,
		DeadLetter: st.deadLetter,

		ConsecutiveErrors: st.consecutiveErrors,
		BreakerState:      string(st.breakerState),
		RateLimited:       st.rateLimited,

		TokensUsed:    st.tokensUsed,
		CostUsedUSD:   st.costUsed,
		TokenBudget:   pol.TokenBudget,
		CostBudgetUSD: pol.CostBudget,

		ReservedTokens: st.reservedTokens,
		ReservedCost:   st.reservedCost,
	}
	if !st.breakerOpenedAt.IsZero() {
		out.BreakerOpenedAt = st.breakerOpenedAt.Unix()
	}
	return out
}

func (m *Manager) maybeRecoverBreakerLocked(st *tenantState, pol Policy, tenantID string, now time.Time) {
	if st.breakerState != BreakerOpen {
		return
	}
	if pol.BreakerCooldown <= 0 || st.breakerOpenedAt.IsZero() {
		return
	}
	if now.Sub(st.breakerOpenedAt) >= pol.BreakerCooldown {
		st.breakerState = BreakerHalfOpen
		st.halfOpenInFlight = false
		m.emitAlertLocked(st, tenantID, AlertTypeBreakerClosed, "breaker moved to half-open probe state")
	}
}

func (m *Manager) openBreakerLocked(st *tenantState, tenantID, reason string) {
	if st.breakerState == BreakerOpen {
		return
	}
	st.breakerState = BreakerOpen
	st.breakerOpenedAt = m.now()
	st.halfOpenInFlight = false
	m.emitAlertLocked(st, tenantID, AlertTypeBreakerOpened, reason)
}

func (m *Manager) emitBudgetAlertsLocked(st *tenantState, pol Policy, tenantID string) {
	if pol.AlertBudgetPct <= 0 {
		return
	}
	if pol.TokenBudget > 0 {
		used := float64(st.tokensUsed+st.reservedTokens) / float64(pol.TokenBudget)
		if used >= pol.AlertBudgetPct {
			m.emitAlertLocked(st, tenantID, AlertTypeBudgetBreach,
				fmt.Sprintf("token budget at %.2f%%", used*100))
		}
	}
	if pol.CostBudget > 0 {
		used := (st.costUsed + st.reservedCost) / pol.CostBudget
		if used >= pol.AlertBudgetPct {
			m.emitAlertLocked(st, tenantID, AlertTypeBudgetBreach,
				fmt.Sprintf("cost budget at %.2f%%", used*100))
		}
	}
}

func (m *Manager) emitAlertLocked(st *tenantState, tenantID string, t AlertType, message string) {
	if tenantID == "" || message == "" {
		return
	}
	st.alerts = append(st.alerts, Alert{
		TenantID: tenantID,
		Type:     t,
		Message:  message,
		TS:       m.now(),
	})
	// Keep alert buffer bounded.
	if len(st.alerts) > 200 {
		st.alerts = append([]Alert(nil), st.alerts[len(st.alerts)-200:]...)
	}
}

func normalizePolicy(p Policy) Policy {
	if p.MaxRunning <= 0 {
		p.MaxRunning = defaultMaxRunning
	}
	if p.MaxQueued <= 0 {
		p.MaxQueued = defaultMaxQueued
	}
	if p.RateLimitPerMinute <= 0 {
		p.RateLimitPerMinute = defaultRatePerMinute
	}
	if p.MaxConsecutiveErrors <= 0 {
		p.MaxConsecutiveErrors = defaultBreakerErrors
	}
	if p.BreakerCooldown <= 0 {
		p.BreakerCooldown = time.Duration(defaultBreakerCoolDownSec) * time.Second
	}
	if p.AlertQueueDepth <= 0 {
		p.AlertQueueDepth = int(math.Max(1, float64(p.MaxQueued)/2))
	}
	if p.AlertErrorBurst <= 0 {
		p.AlertErrorBurst = p.MaxConsecutiveErrors
	}
	if p.AlertBudgetPct <= 0 || p.AlertBudgetPct > 1 {
		p.AlertBudgetPct = 0.9
	}
	return p
}

func pruneOld(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	if i == 0 {
		return ts
	}
	out := make([]time.Time, len(ts)-i)
	copy(out, ts[i:])
	return out
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
