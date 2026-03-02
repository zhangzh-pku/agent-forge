// Package state defines the interface for persisting task/run/step metadata.
package state

import (
	"context"
	"errors"

	"github.com/agentforge/agentforge/pkg/model"
)

var (
	// ErrNotFound is returned when the requested entity does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when a conditional write fails (idempotency/claim).
	ErrConflict = errors.New("conflict: condition check failed")
	// ErrAlreadyExists is returned when a unique constraint is violated.
	ErrAlreadyExists = errors.New("already exists")
)

// TaskStore persists task metadata.
type TaskStore interface {
	// PutTask creates a new task. Returns ErrAlreadyExists if idempotency_key collides.
	PutTask(ctx context.Context, task *model.Task) error
	// GetTask retrieves a task by ID. Returns ErrNotFound if missing.
	GetTask(ctx context.Context, taskID string) (*model.Task, error)
	// UpdateTaskStatus atomically updates task status with a condition.
	UpdateTaskStatus(ctx context.Context, taskID string, from []model.TaskStatus, to model.TaskStatus) error
	// UpdateTaskStatusForRun atomically updates task status only if active_run_id matches runID.
	UpdateTaskStatusForRun(ctx context.Context, taskID, runID string, from []model.TaskStatus, to model.TaskStatus) error
	// SetAbortRequested marks a task for abortion.
	SetAbortRequested(ctx context.Context, taskID string, reason string) error
	// ClearAbortRequested resets the abort flag on a task (used during resume).
	ClearAbortRequested(ctx context.Context, taskID string) error
	// SetActiveRun updates the active run ID on a task.
	SetActiveRun(ctx context.Context, taskID string, runID string) error
	// GetTaskByIdempotencyKey looks up a task by tenant + idempotency key.
	GetTaskByIdempotencyKey(ctx context.Context, tenantID, idempotencyKey string) (*model.Task, error)
}

// RunStore persists run (attempt) metadata.
type RunStore interface {
	// PutRun creates a new run. Returns ErrAlreadyExists on duplicate.
	PutRun(ctx context.Context, run *model.Run) error
	// GetRun retrieves a run by task+run ID.
	GetRun(ctx context.Context, taskID, runID string) (*model.Run, error)
	// ClaimRun atomically transitions RUN_QUEUED→RUNNING. Returns ErrConflict if already claimed.
	ClaimRun(ctx context.Context, taskID, runID string) error
	// CompleteRun sets the run's final status and ended_at.
	CompleteRun(ctx context.Context, taskID, runID string, status model.RunStatus) error
	// UpdateLastStepIndex updates the run's last_step_index.
	UpdateLastStepIndex(ctx context.Context, taskID, runID string, stepIndex int) error
}

// StepStore persists step metadata.
type StepStore interface {
	// PutStep creates a new step. Uses conditional write to prevent duplicates.
	PutStep(ctx context.Context, step *model.Step) error
	// GetStep retrieves a single step.
	GetStep(ctx context.Context, runID string, stepIndex int) (*model.Step, error)
	// ListSteps returns steps for a run, paginated.
	ListSteps(ctx context.Context, runID string, from, limit int) ([]*model.Step, error)
}

// ConnectionStore persists WebSocket connection mappings.
type ConnectionStore interface {
	// PutConnection registers a new WebSocket connection.
	PutConnection(ctx context.Context, conn *model.Connection) error
	// GetConnection retrieves a connection by ID. Returns ErrNotFound if missing.
	GetConnection(ctx context.Context, connectionID string) (*model.Connection, error)
	// DeleteConnection removes a connection.
	DeleteConnection(ctx context.Context, connectionID string) error
	// GetConnectionsByTask returns all connections subscribed to a task.
	GetConnectionsByTask(ctx context.Context, taskID string) ([]*model.Connection, error)
}

// Store combines all sub-stores for convenience.
type Store interface {
	TaskStore
	RunStore
	StepStore
	ConnectionStore
}
