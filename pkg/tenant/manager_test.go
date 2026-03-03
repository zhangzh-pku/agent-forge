package tenant

import (
	"testing"
	"time"
)

func TestManagerEnqueueStartAndComplete(t *testing.T) {
	m := NewManager(Policy{
		MaxRunning:           1,
		MaxQueued:            2,
		RateLimitPerMinute:   10,
		MaxConsecutiveErrors: 3,
		BreakerCooldown:      2 * time.Second,
		TokenBudget:          1000,
		CostBudget:           1.0,
	})

	if err := m.TryEnqueue("t1", AdmissionInfo{MessageKey: "m1", EstimatedTokens: 100, EstimatedCost: 0.1}); err != nil {
		t.Fatalf("enqueue m1: %v", err)
	}
	if err := m.TryEnqueue("t1", AdmissionInfo{MessageKey: "m2", EstimatedTokens: 100, EstimatedCost: 0.1}); err != nil {
		t.Fatalf("enqueue m2: %v", err)
	}
	if err := m.TryStartRun("t1"); err != nil {
		t.Fatalf("start run: %v", err)
	}
	// MaxRunning=1 should reject second start.
	if err := m.TryStartRun("t1"); err == nil {
		t.Fatal("expected running limit violation")
	}

	m.CompleteRun("t1", CompletionInfo{
		MessageKey:  "m1",
		TokensUsed:  120,
		CostUsed:    0.11,
		Failed:      false,
		WasAccepted: true,
	})

	s := m.Snapshot("t1")
	if s.Running != 0 {
		t.Fatalf("expected running=0, got %d", s.Running)
	}
	if s.Queued != 1 {
		t.Fatalf("expected queued=1, got %d", s.Queued)
	}
	if s.Completed != 1 {
		t.Fatalf("expected completed=1, got %d", s.Completed)
	}
	if s.TokensUsed != 120 {
		t.Fatalf("expected tokens_used=120, got %d", s.TokensUsed)
	}
}

func TestManagerQueueLimit(t *testing.T) {
	m := NewManager(Policy{
		MaxRunning:         2,
		MaxQueued:          1,
		RateLimitPerMinute: 100,
	})

	if err := m.TryEnqueue("t2", AdmissionInfo{MessageKey: "a"}); err != nil {
		t.Fatalf("enqueue a: %v", err)
	}
	if err := m.TryEnqueue("t2", AdmissionInfo{MessageKey: "b"}); err == nil {
		t.Fatal("expected queue limit violation")
	}
}

func TestManagerBudgetBreaker(t *testing.T) {
	m := NewManager(Policy{
		MaxRunning:         2,
		MaxQueued:          10,
		RateLimitPerMinute: 100,
		TokenBudget:        100,
		CostBudget:         10,
	})
	if err := m.TryEnqueue("budget", AdmissionInfo{MessageKey: "m1", EstimatedTokens: 60}); err != nil {
		t.Fatalf("enqueue m1: %v", err)
	}
	// Exceeds token budget and opens breaker.
	if err := m.TryEnqueue("budget", AdmissionInfo{MessageKey: "m2", EstimatedTokens: 60}); err == nil {
		t.Fatal("expected token budget violation")
	}
	if err := m.TryEnqueue("budget", AdmissionInfo{MessageKey: "m3", EstimatedTokens: 1}); err == nil {
		t.Fatal("expected breaker open after budget breach")
	}
}

func TestManagerRateLimit(t *testing.T) {
	m := NewManager(Policy{
		MaxRunning:         2,
		MaxQueued:          10,
		RateLimitPerMinute: 2,
	})
	now := time.Now().UTC()
	m.now = func() time.Time { return now }

	if err := m.TryEnqueue("rate", AdmissionInfo{MessageKey: "r1"}); err != nil {
		t.Fatalf("enqueue r1: %v", err)
	}
	if err := m.TryEnqueue("rate", AdmissionInfo{MessageKey: "r2"}); err != nil {
		t.Fatalf("enqueue r2: %v", err)
	}
	if err := m.TryEnqueue("rate", AdmissionInfo{MessageKey: "r3"}); err == nil {
		t.Fatal("expected rate limit violation")
	}
}

func TestManagerBreakerHalfOpenRecovery(t *testing.T) {
	m := NewManager(Policy{
		MaxRunning:           1,
		MaxQueued:            10,
		RateLimitPerMinute:   100,
		MaxConsecutiveErrors: 1,
		BreakerCooldown:      time.Second,
	})
	now := time.Now().UTC()
	m.now = func() time.Time { return now }

	if err := m.TryEnqueue("r", AdmissionInfo{MessageKey: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := m.TryStartRun("r"); err != nil {
		t.Fatal(err)
	}
	m.CompleteRun("r", CompletionInfo{MessageKey: "x", Failed: true})

	// Breaker open now.
	if err := m.TryEnqueue("r", AdmissionInfo{MessageKey: "y"}); err == nil {
		t.Fatal("expected open breaker rejection")
	}

	// Advance time beyond cooldown; should transition to half-open and allow one probe.
	now = now.Add(2 * time.Second)
	if err := m.TryEnqueue("r", AdmissionInfo{MessageKey: "probe"}); err != nil {
		t.Fatalf("enqueue probe after cooldown: %v", err)
	}
	if err := m.TryStartRun("r"); err != nil {
		t.Fatalf("start probe: %v", err)
	}
	m.CompleteRun("r", CompletionInfo{MessageKey: "probe", Failed: false})

	s := m.Snapshot("r")
	if s.BreakerState != string(BreakerClosed) {
		t.Fatalf("expected breaker closed, got %s", s.BreakerState)
	}
}
