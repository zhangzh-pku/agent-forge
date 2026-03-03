// Package ops provides operational recovery and consistency tooling.
package ops

import (
	"context"
	"fmt"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/queue"
	"github.com/agentforge/agentforge/pkg/state"
	"github.com/agentforge/agentforge/pkg/task"
)

// RecoveryReport summarizes a redrive pass.
type RecoveryReport struct {
	Scanned   int      `json:"scanned"`
	Recovered int      `json:"recovered"`
	Skipped   int      `json:"skipped"`
	Errors    []string `json:"errors,omitempty"`
}

// Recoverer redrives stale queued/running runs.
type Recoverer struct {
	store state.Store
	queue queue.Queue
}

// NewRecoverer creates an operational run recoverer.
func NewRecoverer(store state.Store, q queue.Queue) *Recoverer {
	return &Recoverer{store: store, queue: q}
}

// RecoverStaleRuns finds stale RUNNING/RUN_QUEUED runs and requeues them.
func (r *Recoverer) RecoverStaleRuns(ctx context.Context, tenantID string, staleFor time.Duration, limit int) (*RecoveryReport, error) {
	if staleFor <= 0 {
		staleFor = 10 * time.Minute
	}
	if limit <= 0 {
		limit = 200
	}

	now := time.Now().UTC()
	runs, err := r.store.ListRuns(ctx, tenantID, []model.RunStatus{
		model.RunStatusRunning,
		model.RunStatusQueued,
	}, limit)
	if err != nil {
		return nil, fmt.Errorf("ops: list runs: %w", err)
	}

	out := &RecoveryReport{}
	for _, run := range runs {
		out.Scanned++
		if ctx.Err() != nil {
			return out, ctx.Err()
		}

		taskObj, err := r.store.GetTask(ctx, run.TaskID)
		if err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("task lookup failed task=%s run=%s: %v", run.TaskID, run.RunID, err))
			continue
		}
		if taskObj.ActiveRunID != run.RunID {
			out.Skipped++
			continue
		}

		if !isRunStale(run, taskObj, now, staleFor) {
			out.Skipped++
			continue
		}

		if err := r.store.ResetRunToQueued(ctx, run.TaskID, run.RunID); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("reset run failed task=%s run=%s: %v", run.TaskID, run.RunID, err))
			continue
		}
		_ = r.store.UpdateTaskStatusForRun(ctx, run.TaskID, run.RunID, []model.TaskStatus{
			model.TaskStatusRunning, model.TaskStatusQueued, model.TaskStatusFailed, model.TaskStatusAborted, model.TaskStatusSucceeded,
		}, model.TaskStatusQueued)

		taskType := task.InferTaskType(taskObj.Prompt)
		estTokens, estCost := task.EstimateUsage(taskObj.Prompt, run.ModelConfig)
		msg := &model.SQSMessage{
			TenantID:        run.TenantID,
			TaskID:          run.TaskID,
			RunID:           run.RunID,
			TaskType:        taskType,
			SubmittedAt:     now.Unix(),
			Attempt:         1,
			DedupeKey:       run.TaskID + "#" + run.RunID,
			EstimatedTokens: estTokens,
			EstimatedCost:   estCost,
		}
		if err := r.queue.Enqueue(ctx, msg); err != nil {
			out.Errors = append(out.Errors, fmt.Sprintf("requeue failed task=%s run=%s: %v", run.TaskID, run.RunID, err))
			continue
		}
		out.Recovered++
	}
	return out, nil
}

func isRunStale(run *model.Run, taskObj *model.Task, now time.Time, staleFor time.Duration) bool {
	switch run.Status {
	case model.RunStatusQueued:
		age := now.Sub(taskObj.UpdatedAt)
		return age >= staleFor
	case model.RunStatusRunning:
		if run.StartedAt == nil {
			return true
		}
		return now.Sub(*run.StartedAt) >= staleFor
	default:
		return false
	}
}
