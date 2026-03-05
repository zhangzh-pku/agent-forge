package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/queue"
	"github.com/agentforge/agentforge/pkg/state"
)

// SchedulerConfig controls stale-run recovery cadence and optional consistency checks.
type SchedulerConfig struct {
	Interval               time.Duration
	StaleFor               time.Duration
	Limit                  int
	TenantID               string
	ConsistencyCheck       bool
	ConsistencyRepair      bool
	EventCompactionEnabled bool
	EventCompactionWindow  time.Duration
}

// EventCompactionReport summarizes one compaction pass over retained events.
type EventCompactionReport struct {
	BeforeTS      int64    `json:"before_ts"`
	RunsScanned   int      `json:"runs_scanned"`
	RunsCompacted int      `json:"runs_compacted"`
	EventsRemoved int      `json:"events_removed"`
	Errors        []string `json:"errors,omitempty"`
}

// SchedulerReport is one recovery scheduler execution report.
type SchedulerReport struct {
	Timestamp       time.Time              `json:"timestamp"`
	TenantID        string                 `json:"tenant_id,omitempty"`
	DurationMS      int64                  `json:"duration_ms"`
	Recovery        *RecoveryReport        `json:"recovery,omitempty"`
	Consistency     *CheckReport           `json:"consistency,omitempty"`
	Repair          *RepairReport          `json:"repair,omitempty"`
	EventCompaction *EventCompactionReport `json:"event_compaction,omitempty"`
	Error           string                 `json:"error,omitempty"`
}

// Scheduler runs stale-run recovery as a periodic operational job.
type Scheduler struct {
	recoverer *Recoverer
	checker   *ConsistencyChecker
	store     state.Store
	cfg       SchedulerConfig
}

// NewScheduler creates a recovery scheduler with normalized defaults.
func NewScheduler(store state.Store, q queue.Queue, cfg SchedulerConfig) *Scheduler {
	return &Scheduler{
		recoverer: NewRecoverer(store, q),
		checker:   NewConsistencyChecker(store),
		store:     store,
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

	if s.cfg.EventCompactionEnabled {
		compaction, err := s.compactRunEvents(ctx)
		if err != nil {
			report.DurationMS = time.Since(start).Milliseconds()
			report.Error = err.Error()
			return report, err
		}
		report.EventCompaction = compaction
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
	if cfg.EventCompactionWindow <= 0 {
		cfg.EventCompactionWindow = 24 * time.Hour
	}
	return cfg
}

func (s *Scheduler) compactRunEvents(ctx context.Context) (*EventCompactionReport, error) {
	cutoff := time.Now().UTC().Add(-s.cfg.EventCompactionWindow).Unix()
	report := &EventCompactionReport{
		BeforeTS: cutoff,
	}

	runs, err := s.store.ListRuns(ctx, s.cfg.TenantID, []model.RunStatus{
		model.RunStatusSucceeded,
		model.RunStatusFailed,
		model.RunStatusAborted,
	}, s.cfg.Limit)
	if err != nil {
		return report, fmt.Errorf("list runs for compaction: %w", err)
	}
	report.RunsScanned = len(runs)

	for _, run := range runs {
		if run == nil || run.EndedAt == nil {
			continue
		}
		if run.EndedAt.UTC().Unix() > cutoff {
			continue
		}
		removed, err := s.store.CompactEvents(ctx, run.TaskID, run.RunID, cutoff)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("task=%s run=%s: %v", run.TaskID, run.RunID, err))
			continue
		}
		report.RunsCompacted++
		report.EventsRemoved += removed
	}
	return report, nil
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
