package ops

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/agentforge/agentforge/pkg/queue"
	"github.com/agentforge/agentforge/pkg/state"
)

// SchedulerConfig controls stale-run recovery cadence and optional consistency checks.
type SchedulerConfig struct {
	Interval          time.Duration
	StaleFor          time.Duration
	Limit             int
	TenantID          string
	ConsistencyCheck  bool
	ConsistencyRepair bool
}

// SchedulerReport is one recovery scheduler execution report.
type SchedulerReport struct {
	Timestamp   time.Time       `json:"timestamp"`
	TenantID    string          `json:"tenant_id,omitempty"`
	DurationMS  int64           `json:"duration_ms"`
	Recovery    *RecoveryReport `json:"recovery,omitempty"`
	Consistency *CheckReport    `json:"consistency,omitempty"`
	Repair      *RepairReport   `json:"repair,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// Scheduler runs stale-run recovery as a periodic operational job.
type Scheduler struct {
	recoverer *Recoverer
	checker   *ConsistencyChecker
	cfg       SchedulerConfig
}

// NewScheduler creates a recovery scheduler with normalized defaults.
func NewScheduler(store state.Store, q queue.Queue, cfg SchedulerConfig) *Scheduler {
	return &Scheduler{
		recoverer: NewRecoverer(store, q),
		checker:   NewConsistencyChecker(store),
		cfg:       normalizeSchedulerConfig(cfg),
	}
}

// RunOnce executes one recovery pass and optional consistency check/repair.
func (s *Scheduler) RunOnce(ctx context.Context) (*SchedulerReport, error) {
	start := time.Now().UTC()
	report := &SchedulerReport{
		Timestamp: start,
		TenantID:  s.cfg.TenantID,
	}

	recovery, err := s.recoverer.RecoverStaleRuns(ctx, s.cfg.TenantID, s.cfg.StaleFor, s.cfg.Limit)
	if err != nil {
		report.DurationMS = time.Since(start).Milliseconds()
		report.Error = err.Error()
		return report, err
	}
	report.Recovery = recovery

	if s.cfg.ConsistencyCheck {
		check, err := s.checker.Check(ctx, s.cfg.TenantID, s.cfg.Limit)
		if err != nil {
			report.DurationMS = time.Since(start).Milliseconds()
			report.Error = err.Error()
			return report, err
		}
		report.Consistency = check

		if s.cfg.ConsistencyRepair && len(check.Issues) > 0 {
			repair, err := s.checker.Repair(ctx, check.Issues)
			if err != nil {
				report.DurationMS = time.Since(start).Milliseconds()
				report.Error = err.Error()
				return report, err
			}
			report.Repair = repair
		}
	}

	report.DurationMS = time.Since(start).Milliseconds()
	return report, nil
}

// Start runs one immediate pass, then continues with the configured interval.
func (s *Scheduler) Start(ctx context.Context) {
	report, err := s.RunOnce(ctx)
	logSchedulerReport(report, err)

	if s.cfg.Interval <= 0 {
		return
	}

	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			report, err := s.RunOnce(ctx)
			logSchedulerReport(report, err)
		}
	}
}

func normalizeSchedulerConfig(cfg SchedulerConfig) SchedulerConfig {
	if cfg.StaleFor <= 0 {
		cfg.StaleFor = 10 * time.Minute
	}
	if cfg.Limit <= 0 {
		cfg.Limit = 200
	}
	return cfg
}

func logSchedulerReport(report *SchedulerReport, runErr error) {
	if report == nil {
		log.Printf("ops: recovery scheduler report unavailable: %v", runErr)
		return
	}
	data, err := json.Marshal(report)
	if err != nil {
		log.Printf("ops: recovery scheduler report marshal failed: %v", err)
		return
	}
	if runErr != nil {
		log.Printf("ops: recovery scheduler run failed: %s", string(data))
		return
	}
	log.Printf("ops: recovery scheduler run completed: %s", string(data))
}
