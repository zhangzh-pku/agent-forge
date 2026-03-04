// Package task implements task lifecycle operations.
package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
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

// ErrValidation indicates request-level validation failures in task service APIs.
var ErrValidation = errors.New("validation error")

const allowedModelsEnv = "AGENTFORGE_ALLOWED_MODEL_IDS"

var defaultAllowedModelIDs = []string{
	"gpt-4o-mini",
	"gpt-4o",
	"gpt-4.1",
	"gpt-4",
	"claude-3",
	"claude-3-haiku",
	"claude-3-sonnet",
	"claude-3-opus",
	"claude-sonnet-4-20250514",
	"claude-opus-4-6",
}

// NewService creates a new task service.
func NewService(store state.Store, q queue.Queue) *Service {
	return &Service{store: store, queue: q}
}

// Create creates a new task and its first run, then enqueues it.
func (s *Service) Create(ctx context.Context, req *CreateRequest) (*CreateResponse, error) {
	if req == nil {
		return nil, validationError("request is required")
	}
	if strings.TrimSpace(req.TenantID) == "" {
		return nil, validationError("tenant_id is required")
	}
	if strings.TrimSpace(req.UserID) == "" {
		return nil, validationError("user_id is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, validationError("prompt is required")
	}
	normalizedMC, err := normalizeAndValidateModelConfig(req.ModelConfig)
	if err != nil {
		return nil, err
	}

	// Check idempotency.
	if req.IdempotencyKey != "" {
		existing, err := s.store.GetTaskByIdempotencyKey(ctx, req.TenantID, req.IdempotencyKey)
		if err == nil {
			// Best-effort self-healing for create path gaps: if the active run is
			// still queued, ensure it is (re-)enqueued.
			if err := s.ensureActiveQueuedRunEnqueued(ctx, existing); err != nil {
				return nil, err
			}
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
		ModelConfig:    normalizedMC,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	run := &model.Run{
		TaskID:      taskID,
		RunID:       runID,
		TenantID:    req.TenantID,
		Status:      model.RunStatusQueued,
		QueuedAt:    &now,
		ModelConfig: normalizedMC,
	}

	if err := s.store.ApplyCreateTransition(ctx, task, run); err != nil {
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

	// Enqueue pointer message.
	if err := s.enqueueQueuedRun(ctx, task, run, now); err != nil {
		return nil, err
	}

	return &CreateResponse{TaskID: taskID, RunID: runID}, nil
}

// Get retrieves task metadata.
func (s *Service) Get(ctx context.Context, tenantID, taskID string) (*model.Task, error) {
	taskObj, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !hasTaskTenantAccess(taskObj, tenantID) {
		return nil, state.ErrNotFound
	}
	return taskObj, nil
}

// GetForUser retrieves task metadata and validates tenant/user access.
func (s *Service) GetForUser(ctx context.Context, tenantID, userID, taskID string) (*model.Task, error) {
	taskObj, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !hasTaskAccess(taskObj, tenantID, userID) {
		return nil, state.ErrNotFound
	}
	return taskObj, nil
}

// Abort marks a task for abortion.
func (s *Service) Abort(ctx context.Context, tenantID, taskID string, req *AbortRequest) (*model.Task, error) {
	if req == nil {
		req = &AbortRequest{TenantID: tenantID}
	}
	if req.UserID == "" {
		taskObj, err := s.store.GetTask(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if !hasTaskTenantAccess(taskObj, tenantID) {
			return nil, state.ErrNotFound
		}
		if err := s.store.SetAbortRequested(ctx, taskID, req.Reason); err != nil {
			return nil, err
		}
		return s.store.GetTask(ctx, taskID)
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
	if req == nil {
		return nil, validationError("request is required")
	}
	if strings.TrimSpace(req.TenantID) == "" {
		return nil, validationError("tenant_id is required")
	}
	if strings.TrimSpace(req.UserID) == "" {
		return nil, validationError("user_id is required")
	}
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !hasTaskAccess(task, req.TenantID, req.UserID) {
		return nil, state.ErrNotFound
	}

	if req.FromRunID == "" {
		return nil, validationError("from_run_id is required for resume")
	}

	if req.FromStepIndex < 0 {
		return nil, validationError("from_step_index must be non-negative")
	}

	// Validate that from_run_id belongs to this task.
	if _, err := s.store.GetRun(ctx, taskID, req.FromRunID); err != nil {
		return nil, validationError("from_run_id not found for this task")
	}

	// Validate that the step exists and has a checkpoint.
	step, err := s.store.GetStep(ctx, req.FromRunID, req.FromStepIndex)
	if err != nil {
		return nil, validationError("checkpoint step not found: cannot resume from a step that does not exist")
	}
	if step.CheckpointRef == nil {
		return nil, validationError("checkpoint step has no checkpoint data: cannot resume")
	}

	now := time.Now().UTC()
	newRunID := util.NewID("run_")
	stepIdx := req.FromStepIndex

	mc := req.ModelConfigOverride
	if mc == nil {
		mc = task.ModelConfig
	}
	mc, err = normalizeAndValidateModelConfig(mc)
	if err != nil {
		return nil, err
	}

	run := &model.Run{
		TaskID:              taskID,
		RunID:               newRunID,
		TenantID:            req.TenantID,
		Status:              model.RunStatusQueued,
		QueuedAt:            &now,
		ParentRunID:         req.FromRunID,
		ResumeFromStepIndex: &stepIdx,
		ModelConfig:         mc,
	}

	// Atomically apply run creation + task state transition for resume.
	if err := s.store.ApplyResumeTransition(ctx, taskID, run, []model.TaskStatus{
		model.TaskStatusQueued,
		model.TaskStatusSucceeded, model.TaskStatusFailed, model.TaskStatusAborted,
	}, model.TaskStatusQueued); err != nil {
		return nil, err
	}

	taskType := InferTaskType(task.Prompt)
	estimatedTokens, estimatedCost := EstimateUsage(task.Prompt, mc)

	if err := s.queue.Enqueue(ctx, &model.SQSMessage{
		TenantID:        req.TenantID,
		TaskID:          taskID,
		RunID:           newRunID,
		TaskType:        taskType,
		SubmittedAt:     now.Unix(),
		Attempt:         1,
		DedupeKey:       taskID + "#" + newRunID,
		EstimatedTokens: estimatedTokens,
		EstimatedCost:   estimatedCost,
	}); err != nil {
		s.markRunAndTaskFailed(taskID, newRunID)
		return nil, err
	}

	return &ResumeResponse{TaskID: taskID, RunID: newRunID}, nil
}

// ListSteps retrieves steps for a run with pagination.
func (s *Service) ListSteps(ctx context.Context, tenantID, taskID, runID string, from, limit int) ([]*model.Step, error) {
	taskObj, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !hasTaskTenantAccess(taskObj, tenantID) {
		return nil, state.ErrNotFound
	}
	if _, err := s.store.GetRun(ctx, taskID, runID); err != nil {
		return nil, state.ErrNotFound
	}
	return s.store.ListSteps(ctx, runID, from, limit)
}

// GetRunForUser retrieves run metadata with tenant/user access validation.
func (s *Service) GetRunForUser(ctx context.Context, tenantID, userID, taskID, runID string) (*model.Run, error) {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !hasTaskAccess(task, tenantID, userID) {
		return nil, state.ErrNotFound
	}
	run, err := s.store.GetRun(ctx, taskID, runID)
	if err != nil {
		return nil, state.ErrNotFound
	}
	return run, nil
}

// ReplayEventsForUser fetches persisted stream events for reconnect replay.
func (s *Service) ReplayEventsForUser(ctx context.Context, tenantID, userID, taskID, runID string, fromSeq, fromTS int64, limit int) ([]*model.StreamEvent, error) {
	taskObj, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !hasTaskAccess(taskObj, tenantID, userID) {
		return nil, state.ErrNotFound
	}
	if _, err := s.store.GetRun(ctx, taskID, runID); err != nil {
		return nil, state.ErrNotFound
	}
	return s.store.ReplayEvents(ctx, taskID, runID, fromSeq, fromTS, limit)
}

// CompactRunEventsForUser compacts old persisted stream events.
func (s *Service) CompactRunEventsForUser(ctx context.Context, tenantID, userID, taskID, runID string, beforeTS int64) (int, error) {
	taskObj, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return 0, err
	}
	if !hasTaskAccess(taskObj, tenantID, userID) {
		return 0, state.ErrNotFound
	}
	if _, err := s.store.GetRun(ctx, taskID, runID); err != nil {
		return 0, state.ErrNotFound
	}
	return s.store.CompactEvents(ctx, taskID, runID, beforeTS)
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
	if !hasTaskTenantAccess(task, tenantID) {
		return false
	}
	if strings.TrimSpace(userID) == "" {
		return false
	}
	if strings.TrimSpace(task.UserID) == "" {
		return false
	}
	return task.UserID == userID
}

func hasTaskTenantAccess(task *model.Task, tenantID string) bool {
	if task == nil {
		return false
	}
	return task.TenantID == tenantID
}

func validationError(msg string) error {
	return fmt.Errorf("%w: %s", ErrValidation, msg)
}

func normalizeAndValidateModelConfig(mc *model.ModelConfig) (*model.ModelConfig, error) {
	if mc == nil {
		return nil, nil
	}

	copyMC := *mc
	copyMC.ModelID = strings.TrimSpace(copyMC.ModelID)
	if copyMC.ModelID == "" {
		return nil, validationError("model_config.model_id is required")
	}
	if !isAllowedModelID(copyMC.ModelID) {
		return nil, validationError("model_config.model_id is not allowed")
	}
	if copyMC.Temperature < 0 || copyMC.Temperature > 2 {
		return nil, validationError("model_config.temperature must be between 0 and 2")
	}
	if copyMC.MaxTokens < 0 {
		return nil, validationError("model_config.max_tokens must be >= 0")
	}
	if copyMC.CostCapUSD < 0 {
		return nil, validationError("model_config.cost_cap_usd must be >= 0")
	}

	if pm := strings.TrimSpace(copyMC.PolicyMode); pm != "" {
		switch pm {
		case "latency-first", "quality-first", "cost-cap":
			copyMC.PolicyMode = pm
		default:
			return nil, validationError("model_config.policy_mode is invalid")
		}
	}

	if len(copyMC.FallbackModelIDs) > 0 {
		fallback := make([]string, 0, len(copyMC.FallbackModelIDs))
		seen := make(map[string]struct{}, len(copyMC.FallbackModelIDs))
		for _, m := range copyMC.FallbackModelIDs {
			modelID := strings.TrimSpace(m)
			if modelID == "" {
				continue
			}
			if !isAllowedModelID(modelID) {
				return nil, validationError("model_config.fallback_model_ids contains unsupported model")
			}
			if _, exists := seen[modelID]; exists {
				continue
			}
			seen[modelID] = struct{}{}
			fallback = append(fallback, modelID)
		}
		copyMC.FallbackModelIDs = fallback
	}

	return &copyMC, nil
}

func isAllowedModelID(modelID string) bool {
	allowed := make(map[string]struct{})
	raw := strings.TrimSpace(os.Getenv(allowedModelsEnv))
	if raw == "" {
		for _, m := range defaultAllowedModelIDs {
			allowed[strings.ToLower(strings.TrimSpace(m))] = struct{}{}
		}
	} else {
		for _, part := range strings.Split(raw, ",") {
			m := strings.ToLower(strings.TrimSpace(part))
			if m == "" {
				continue
			}
			allowed[m] = struct{}{}
		}
	}
	_, ok := allowed[strings.ToLower(strings.TrimSpace(modelID))]
	return ok
}

func (s *Service) markRunAndTaskFailed(taskID, runID string) {
	if taskID == "" || runID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.store.CompleteRun(ctx, taskID, runID, model.RunStatusFailed)
	_ = s.store.UpdateTaskStatusForRun(ctx, taskID, runID, []model.TaskStatus{
		model.TaskStatusQueued,
		model.TaskStatusRunning,
	}, model.TaskStatusFailed)
}

func (s *Service) ensureActiveQueuedRunEnqueued(ctx context.Context, taskObj *model.Task) error {
	if taskObj == nil || taskObj.ActiveRunID == "" {
		return nil
	}
	run, err := s.store.GetRun(ctx, taskObj.TaskID, taskObj.ActiveRunID)
	if err != nil {
		return err
	}
	return s.enqueueQueuedRun(ctx, taskObj, run, time.Now().UTC())
}

func (s *Service) enqueueQueuedRun(ctx context.Context, taskObj *model.Task, run *model.Run, submittedAt time.Time) error {
	if taskObj == nil || run == nil {
		return nil
	}
	if taskObj.Status != model.TaskStatusQueued || run.Status != model.RunStatusQueued {
		return nil
	}
	tenantID := taskObj.TenantID
	if strings.TrimSpace(run.TenantID) != "" {
		tenantID = strings.TrimSpace(run.TenantID)
	}
	taskType := InferTaskType(taskObj.Prompt)
	estimatedTokens, estimatedCost := EstimateUsage(taskObj.Prompt, run.ModelConfig)
	return s.queue.Enqueue(ctx, &model.SQSMessage{
		TenantID:        tenantID,
		TaskID:          taskObj.TaskID,
		RunID:           run.RunID,
		TaskType:        taskType,
		SubmittedAt:     submittedAt.Unix(),
		Attempt:         1,
		DedupeKey:       taskObj.TaskID + "#" + run.RunID,
		EstimatedTokens: estimatedTokens,
		EstimatedCost:   estimatedCost,
	})
}
