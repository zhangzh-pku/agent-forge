// Package task implements task lifecycle operations.
package task

import (
	"context"
	"errors"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/queue"
	"github.com/agentforge/agentforge/pkg/state"
	"github.com/agentforge/agentforge/pkg/util"
)

// CreateRequest is the input for creating a new task.
type CreateRequest struct {
	TenantID       string             `json:"tenant_id"`
	UserID         string             `json:"user_id"`
	Prompt         string             `json:"prompt"`
	ModelConfig    *model.ModelConfig `json:"model_config,omitempty"`
	IdempotencyKey string             `json:"idempotency_key,omitempty"`
}

// CreateResponse is the output of task creation.
type CreateResponse struct {
	TaskID string `json:"task_id"`
	RunID  string `json:"run_id"`
}

// ResumeRequest is the input for resuming a task from a checkpoint.
type ResumeRequest struct {
	TenantID            string             `json:"tenant_id"`
	UserID              string             `json:"user_id,omitempty"`
	FromRunID           string             `json:"from_run_id"`
	FromStepIndex       int                `json:"from_step_index"`
	ModelConfigOverride *model.ModelConfig `json:"model_config_override,omitempty"`
}

// ResumeResponse is the output of task resume.
type ResumeResponse struct {
	TaskID string `json:"task_id"`
	RunID  string `json:"run_id"`
}

// AbortRequest is the input for aborting a task.
type AbortRequest struct {
	TenantID string `json:"tenant_id"`
	UserID   string `json:"user_id,omitempty"`
	Reason   string `json:"reason"`
}

// Service provides task lifecycle operations.
type Service struct {
	store state.Store
	queue queue.Queue
}

// NewService creates a new task service.
func NewService(store state.Store, q queue.Queue) *Service {
	return &Service{store: store, queue: q}
}

// Create creates a new task and its first run, then enqueues it.
func (s *Service) Create(ctx context.Context, req *CreateRequest) (*CreateResponse, error) {
	// Check idempotency.
	if req.IdempotencyKey != "" {
		existing, err := s.store.GetTaskByIdempotencyKey(ctx, req.TenantID, req.IdempotencyKey)
		if err == nil {
			return &CreateResponse{TaskID: existing.TaskID, RunID: existing.ActiveRunID}, nil
		}
		if !errors.Is(err, state.ErrNotFound) {
			return nil, err
		}
	}

	now := time.Now().UTC()
	taskID := util.NewID("task_")
	runID := util.NewID("run_")

	task := &model.Task{
		TaskID:         taskID,
		TenantID:       req.TenantID,
		UserID:         req.UserID,
		Status:         model.TaskStatusQueued,
		ActiveRunID:    runID,
		IdempotencyKey: req.IdempotencyKey,
		Prompt:         req.Prompt,
		ModelConfig:    req.ModelConfig,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	run := &model.Run{
		TaskID:      taskID,
		RunID:       runID,
		TenantID:    req.TenantID,
		Status:      model.RunStatusQueued,
		ModelConfig: req.ModelConfig,
	}

	if err := s.store.PutTask(ctx, task); err != nil {
		if errors.Is(err, state.ErrAlreadyExists) && req.IdempotencyKey != "" {
			// Race condition: another request created it. Fetch and return.
			existing, err2 := s.store.GetTaskByIdempotencyKey(ctx, req.TenantID, req.IdempotencyKey)
			if err2 != nil {
				return nil, err2
			}
			return &CreateResponse{TaskID: existing.TaskID, RunID: existing.ActiveRunID}, nil
		}
		return nil, err
	}

	if err := s.store.PutRun(ctx, run); err != nil {
		return nil, err
	}

	// Enqueue pointer message.
	if err := s.queue.Enqueue(ctx, &model.SQSMessage{
		TenantID:    req.TenantID,
		TaskID:      taskID,
		RunID:       runID,
		SubmittedAt: now.Unix(),
		Attempt:     1,
	}); err != nil {
		return nil, err
	}

	return &CreateResponse{TaskID: taskID, RunID: runID}, nil
}

// Get retrieves task metadata.
func (s *Service) Get(ctx context.Context, tenantID, taskID string) (*model.Task, error) {
	return s.GetForUser(ctx, tenantID, "", taskID)
}

// GetForUser retrieves task metadata and validates tenant/user access.
func (s *Service) GetForUser(ctx context.Context, tenantID, userID, taskID string) (*model.Task, error) {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !hasTaskAccess(task, tenantID, userID) {
		return nil, state.ErrNotFound
	}
	return task, nil
}

// Abort marks a task for abortion.
func (s *Service) Abort(ctx context.Context, tenantID, taskID string, req *AbortRequest) (*model.Task, error) {
	if req == nil {
		req = &AbortRequest{TenantID: tenantID}
	}
	return s.AbortForUser(ctx, tenantID, req.UserID, taskID, req)
}

// AbortForUser marks a task for abortion with tenant/user access validation.
func (s *Service) AbortForUser(ctx context.Context, tenantID, userID, taskID string, req *AbortRequest) (*model.Task, error) {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !hasTaskAccess(task, tenantID, userID) {
		return nil, state.ErrNotFound
	}

	if err := s.store.SetAbortRequested(ctx, taskID, req.Reason); err != nil {
		return nil, err
	}

	return s.store.GetTask(ctx, taskID)
}

// Resume creates a new run from a checkpoint and enqueues it.
func (s *Service) Resume(ctx context.Context, taskID string, req *ResumeRequest) (*ResumeResponse, error) {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !hasTaskAccess(task, req.TenantID, req.UserID) {
		return nil, state.ErrNotFound
	}

	if req.FromRunID == "" {
		return nil, errors.New("from_run_id is required for resume")
	}

	if req.FromStepIndex < 0 {
		return nil, errors.New("from_step_index must be non-negative")
	}

	// Validate that from_run_id belongs to this task.
	if _, err := s.store.GetRun(ctx, taskID, req.FromRunID); err != nil {
		return nil, errors.New("from_run_id not found for this task")
	}

	// Validate that the step exists and has a checkpoint.
	step, err := s.store.GetStep(ctx, req.FromRunID, req.FromStepIndex)
	if err != nil {
		return nil, errors.New("checkpoint step not found: cannot resume from a step that does not exist")
	}
	if step.CheckpointRef == nil {
		return nil, errors.New("checkpoint step has no checkpoint data: cannot resume")
	}

	now := time.Now().UTC()
	newRunID := util.NewID("run_")
	stepIdx := req.FromStepIndex

	mc := req.ModelConfigOverride
	if mc == nil {
		mc = task.ModelConfig
	}

	run := &model.Run{
		TaskID:              taskID,
		RunID:               newRunID,
		TenantID:            req.TenantID,
		Status:              model.RunStatusQueued,
		ParentRunID:         req.FromRunID,
		ResumeFromStepIndex: &stepIdx,
		ModelConfig:         mc,
	}

	if err := s.store.PutRun(ctx, run); err != nil {
		return nil, err
	}

	// Update active run and reset task state for new execution.
	if err := s.store.SetActiveRun(ctx, taskID, newRunID); err != nil {
		return nil, err
	}
	// Clear abort flag so the new run doesn't immediately abort.
	if err := s.store.ClearAbortRequested(ctx, taskID); err != nil {
		return nil, err
	}
	if err := s.store.UpdateTaskStatus(ctx, taskID, []model.TaskStatus{
		model.TaskStatusQueued, model.TaskStatusRunning,
		model.TaskStatusSucceeded, model.TaskStatusFailed, model.TaskStatusAborted,
	}, model.TaskStatusQueued); err != nil {
		return nil, err
	}

	if err := s.queue.Enqueue(ctx, &model.SQSMessage{
		TenantID:    req.TenantID,
		TaskID:      taskID,
		RunID:       newRunID,
		SubmittedAt: now.Unix(),
		Attempt:     1,
	}); err != nil {
		return nil, err
	}

	return &ResumeResponse{TaskID: taskID, RunID: newRunID}, nil
}

// ListSteps retrieves steps for a run with pagination.
func (s *Service) ListSteps(ctx context.Context, tenantID, taskID, runID string, from, limit int) ([]*model.Step, error) {
	return s.ListStepsForUser(ctx, tenantID, "", taskID, runID, from, limit)
}

// ListStepsForUser retrieves steps for a run with tenant/user access validation.
func (s *Service) ListStepsForUser(ctx context.Context, tenantID, userID, taskID, runID string, from, limit int) ([]*model.Step, error) {
	// Verify tenant access.
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !hasTaskAccess(task, tenantID, userID) {
		return nil, state.ErrNotFound
	}
	// Verify that the run belongs to this task.
	if _, err := s.store.GetRun(ctx, taskID, runID); err != nil {
		return nil, state.ErrNotFound
	}
	return s.store.ListSteps(ctx, runID, from, limit)
}

func hasTaskAccess(task *model.Task, tenantID, userID string) bool {
	if task.TenantID != tenantID {
		return false
	}
	// Allow legacy callers without user scope, but enforce user isolation when provided.
	if userID != "" && task.UserID != "" && task.UserID != userID {
		return false
	}
	return true
}
