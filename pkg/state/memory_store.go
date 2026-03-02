package state

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
)

// MemoryStore is an in-memory implementation of Store for testing and local dev.
type MemoryStore struct {
	mu          sync.RWMutex
	tasks       map[string]*model.Task       // keyed by task_id
	runs        map[string]*model.Run        // keyed by "task_id#run_id"
	steps       map[string]*model.Step       // keyed by "run_id#step_index"
	connections map[string]*model.Connection // keyed by connection_id
	idempotency map[string]string            // keyed by "tenant_id#idempotency_key" → task_id
}

// NewMemoryStore creates a new in-memory state store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tasks:       make(map[string]*model.Task),
		runs:        make(map[string]*model.Run),
		steps:       make(map[string]*model.Step),
		connections: make(map[string]*model.Connection),
		idempotency: make(map[string]string),
	}
}

func runKey(taskID, runID string) string   { return taskID + "#" + runID }
func stepKey(runID string, idx int) string { return fmt.Sprintf("%s#%08d", runID, idx) }
func idempKey(tenantID, key string) string { return tenantID + "#" + key }

// cloneTask deep-copies a Task including pointer fields.
func cloneTask(t *model.Task) *model.Task {
	clone := *t
	if t.ModelConfig != nil {
		mc := *t.ModelConfig
		clone.ModelConfig = &mc
	}
	if t.AbortTS != nil {
		ts := *t.AbortTS
		clone.AbortTS = &ts
	}
	return &clone
}

// cloneRun deep-copies a Run including pointer fields.
func cloneRun(r *model.Run) *model.Run {
	clone := *r
	if r.ModelConfig != nil {
		mc := *r.ModelConfig
		clone.ModelConfig = &mc
	}
	if r.StartedAt != nil {
		ts := *r.StartedAt
		clone.StartedAt = &ts
	}
	if r.EndedAt != nil {
		ts := *r.EndedAt
		clone.EndedAt = &ts
	}
	if r.ResumeFromStepIndex != nil {
		idx := *r.ResumeFromStepIndex
		clone.ResumeFromStepIndex = &idx
	}
	return &clone
}

// cloneStep deep-copies a Step including pointer fields.
func cloneStep(st *model.Step) *model.Step {
	clone := *st
	if st.TokenUsage != nil {
		tu := *st.TokenUsage
		clone.TokenUsage = &tu
	}
	if st.CheckpointRef != nil {
		cr := *st.CheckpointRef
		if st.CheckpointRef.Memory != nil {
			m := *st.CheckpointRef.Memory
			cr.Memory = &m
		}
		if st.CheckpointRef.Workspace != nil {
			w := *st.CheckpointRef.Workspace
			cr.Workspace = &w
		}
		clone.CheckpointRef = &cr
	}
	return &clone
}

// --- TaskStore ---

func (s *MemoryStore) PutTask(_ context.Context, task *model.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if task.IdempotencyKey != "" {
		ik := idempKey(task.TenantID, task.IdempotencyKey)
		if _, exists := s.idempotency[ik]; exists {
			return ErrAlreadyExists
		}
		s.idempotency[ik] = task.TaskID
	}

	if _, exists := s.tasks[task.TaskID]; exists {
		return ErrAlreadyExists
	}
	s.tasks[task.TaskID] = cloneTask(task)
	return nil
}

func (s *MemoryStore) GetTask(_ context.Context, taskID string) (*model.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, ok := s.tasks[taskID]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneTask(t), nil
}

func (s *MemoryStore) UpdateTaskStatus(_ context.Context, taskID string, from []model.TaskStatus, to model.TaskStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.tasks[taskID]
	if !ok {
		return ErrNotFound
	}
	allowed := false
	for _, f := range from {
		if t.Status == f {
			allowed = true
			break
		}
	}
	if !allowed {
		return ErrConflict
	}
	t.Status = to
	t.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *MemoryStore) UpdateTaskStatusForRun(_ context.Context, taskID, runID string, from []model.TaskStatus, to model.TaskStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.tasks[taskID]
	if !ok {
		return ErrNotFound
	}
	if t.ActiveRunID != runID {
		return ErrConflict
	}
	allowed := false
	for _, f := range from {
		if t.Status == f {
			allowed = true
			break
		}
	}
	if !allowed {
		return ErrConflict
	}
	t.Status = to
	t.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *MemoryStore) SetAbortRequested(_ context.Context, taskID string, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.tasks[taskID]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	t.AbortRequested = true
	t.AbortReason = reason
	t.AbortTS = &now
	t.UpdatedAt = now
	return nil
}

func (s *MemoryStore) ClearAbortRequested(_ context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.tasks[taskID]
	if !ok {
		return ErrNotFound
	}
	t.AbortRequested = false
	t.AbortReason = ""
	t.AbortTS = nil
	t.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *MemoryStore) SetActiveRun(_ context.Context, taskID string, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.tasks[taskID]
	if !ok {
		return ErrNotFound
	}
	t.ActiveRunID = runID
	t.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *MemoryStore) GetTaskByIdempotencyKey(_ context.Context, tenantID, idempotencyKey string) (*model.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ik := idempKey(tenantID, idempotencyKey)
	taskID, ok := s.idempotency[ik]
	if !ok {
		return nil, ErrNotFound
	}
	t, ok := s.tasks[taskID]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneTask(t), nil
}

// --- RunStore ---

func (s *MemoryStore) PutRun(_ context.Context, run *model.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := runKey(run.TaskID, run.RunID)
	if _, exists := s.runs[key]; exists {
		return ErrAlreadyExists
	}
	s.runs[key] = cloneRun(run)
	return nil
}

func (s *MemoryStore) GetRun(_ context.Context, taskID, runID string) (*model.Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	r, ok := s.runs[runKey(taskID, runID)]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneRun(r), nil
}

func (s *MemoryStore) ClaimRun(_ context.Context, taskID, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.runs[runKey(taskID, runID)]
	if !ok {
		return ErrNotFound
	}
	if r.Status != model.RunStatusQueued {
		return ErrConflict
	}
	now := time.Now().UTC()
	r.Status = model.RunStatusRunning
	r.StartedAt = &now
	return nil
}

func (s *MemoryStore) CompleteRun(_ context.Context, taskID, runID string, status model.RunStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.runs[runKey(taskID, runID)]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	r.Status = status
	r.EndedAt = &now
	return nil
}

func (s *MemoryStore) UpdateLastStepIndex(_ context.Context, taskID, runID string, stepIndex int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.runs[runKey(taskID, runID)]
	if !ok {
		return ErrNotFound
	}
	r.LastStepIndex = stepIndex
	return nil
}

// --- StepStore ---

func (s *MemoryStore) PutStep(_ context.Context, step *model.Step) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := stepKey(step.RunID, step.StepIndex)
	if _, exists := s.steps[key]; exists {
		return ErrConflict // idempotent: attribute_not_exists equivalent
	}
	s.steps[key] = cloneStep(step)
	return nil
}

func (s *MemoryStore) GetStep(_ context.Context, runID string, stepIndex int) (*model.Step, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st, ok := s.steps[stepKey(runID, stepIndex)]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneStep(st), nil
}

func (s *MemoryStore) ListSteps(_ context.Context, runID string, from, limit int) ([]*model.Step, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*model.Step
	for _, st := range s.steps {
		if st.RunID == runID && st.StepIndex >= from {
			result = append(result, cloneStep(st))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].StepIndex < result[j].StepIndex
	})
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// --- ConnectionStore ---

func (s *MemoryStore) PutConnection(_ context.Context, conn *model.Connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	clone := *conn
	s.connections[conn.ConnectionID] = &clone
	return nil
}

func (s *MemoryStore) GetConnection(_ context.Context, connectionID string) (*model.Connection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	c, ok := s.connections[connectionID]
	if !ok {
		return nil, ErrNotFound
	}
	clone := *c
	return &clone, nil
}

func (s *MemoryStore) DeleteConnection(_ context.Context, connectionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.connections, connectionID)
	return nil
}

func (s *MemoryStore) GetConnectionsByTask(_ context.Context, taskID string) ([]*model.Connection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*model.Connection
	for _, c := range s.connections {
		if c.TaskID == taskID {
			clone := *c
			result = append(result, &clone)
		}
	}
	return result, nil
}
