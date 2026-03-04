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
	mu             sync.RWMutex
	tasks          map[string]*model.Task          // keyed by task_id
	runs           map[string]*model.Run           // keyed by "task_id#run_id"
	steps          map[string]*model.Step          // keyed by "run_id#step_index"
	connections    map[string]*model.Connection    // keyed by connection_id
	events         map[string][]*model.StreamEvent // keyed by run_id
	eventRetention time.Duration
	idempotency    map[string]string // keyed by "tenant_id#idempotency_key" → task_id
}

// HealthCheck always succeeds for in-memory local state.
func (s *MemoryStore) HealthCheck(_ context.Context) error {
	return nil
}

// NewMemoryStore creates a new in-memory state store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tasks:          make(map[string]*model.Task),
		runs:           make(map[string]*model.Run),
		steps:          make(map[string]*model.Step),
		connections:    make(map[string]*model.Connection),
		events:         make(map[string][]*model.StreamEvent),
		eventRetention: 24 * time.Hour,
		idempotency:    make(map[string]string),
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
		if len(t.ModelConfig.FallbackModelIDs) > 0 {
			mc.FallbackModelIDs = append([]string(nil), t.ModelConfig.FallbackModelIDs...)
		}
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
		if len(r.ModelConfig.FallbackModelIDs) > 0 {
			mc.FallbackModelIDs = append([]string(nil), r.ModelConfig.FallbackModelIDs...)
		}
		clone.ModelConfig = &mc
	}
	if r.QueuedAt != nil {
		ts := *r.QueuedAt
		clone.QueuedAt = &ts
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
	if r.TotalTokenUsage != nil {
		tu := *r.TotalTokenUsage
		clone.TotalTokenUsage = &tu
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

func cloneEvent(ev *model.StreamEvent) *model.StreamEvent {
	if ev == nil {
		return nil
	}
	clone := *ev
	return &clone
}

// --- TaskStore ---

func (s *MemoryStore) ApplyCreateTransition(_ context.Context, task *model.Task, run *model.Run) error {
	run = ensureQueuedAt(run, time.Now().UTC())
	if task == nil || run == nil || task.TaskID == "" || run.TaskID == "" || run.RunID == "" {
		return ErrConflict
	}
	if run.TaskID != task.TaskID {
		return ErrConflict
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tasks[task.TaskID]; exists {
		return ErrAlreadyExists
	}
	if _, exists := s.runs[runKey(run.TaskID, run.RunID)]; exists {
		return ErrAlreadyExists
	}

	if task.IdempotencyKey != "" {
		ik := idempKey(task.TenantID, task.IdempotencyKey)
		if _, exists := s.idempotency[ik]; exists {
			return ErrAlreadyExists
		}
		s.idempotency[ik] = task.TaskID
	}

	s.tasks[task.TaskID] = cloneTask(task)
	s.runs[runKey(run.TaskID, run.RunID)] = cloneRun(run)
	return nil
}

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

func (s *MemoryStore) IsAbortRequested(_ context.Context, taskID string) (bool, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, ok := s.tasks[taskID]
	if !ok {
		return false, "", ErrNotFound
	}
	return t.AbortRequested, t.AbortReason, nil
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

func (s *MemoryStore) ApplyResumeTransition(_ context.Context, taskID string, run *model.Run, from []model.TaskStatus, to model.TaskStatus) error {
	run = ensureQueuedAt(run, time.Now().UTC())
	s.mu.Lock()
	defer s.mu.Unlock()

	if run == nil || run.TaskID == "" || run.RunID == "" || run.TaskID != taskID {
		return ErrConflict
	}

	t, ok := s.tasks[taskID]
	if !ok {
		return ErrNotFound
	}

	key := runKey(run.TaskID, run.RunID)
	if _, exists := s.runs[key]; exists {
		return ErrAlreadyExists
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

	// Commit all resume mutations atomically under one lock.
	s.runs[key] = cloneRun(run)
	t.ActiveRunID = run.RunID
	t.AbortRequested = false
	t.AbortReason = ""
	t.AbortTS = nil
	t.Status = to
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

func (s *MemoryStore) ListTasks(_ context.Context, tenantID string, statuses []model.TaskStatus, limit int) ([]*model.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filterStatus := make(map[model.TaskStatus]struct{}, len(statuses))
	for _, st := range statuses {
		filterStatus[st] = struct{}{}
	}

	out := make([]*model.Task, 0, len(s.tasks))
	for _, task := range s.tasks {
		if tenantID != "" && task.TenantID != tenantID {
			continue
		}
		if len(filterStatus) > 0 {
			if _, ok := filterStatus[task.Status]; !ok {
				continue
			}
		}
		out = append(out, cloneTask(task))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].TaskID < out[j].TaskID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// --- RunStore ---

func (s *MemoryStore) PutRun(_ context.Context, run *model.Run) error {
	run = ensureQueuedAt(run, time.Now().UTC())
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

func (s *MemoryStore) AddRunUsage(_ context.Context, taskID, runID string, usage *model.TokenUsage, costUSD float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.runs[runKey(taskID, runID)]
	if !ok {
		return ErrNotFound
	}
	if usage != nil {
		if r.TotalTokenUsage == nil {
			r.TotalTokenUsage = &model.TokenUsage{}
		}
		r.TotalTokenUsage.Input += usage.Input
		r.TotalTokenUsage.Output += usage.Output
		r.TotalTokenUsage.Total += usage.Total
	}
	if costUSD > 0 {
		r.TotalCostUSD += costUSD
	}
	return nil
}

func (s *MemoryStore) ResetRunToQueued(_ context.Context, taskID, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.runs[runKey(taskID, runID)]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	r.Status = model.RunStatusQueued
	r.QueuedAt = &now
	r.StartedAt = nil
	r.EndedAt = nil
	return nil
}

func (s *MemoryStore) ListRuns(_ context.Context, tenantID string, statuses []model.RunStatus, limit int) ([]*model.Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filterStatus := make(map[model.RunStatus]struct{}, len(statuses))
	for _, st := range statuses {
		filterStatus[st] = struct{}{}
	}

	out := make([]*model.Run, 0, len(s.runs))
	for _, run := range s.runs {
		if tenantID != "" && run.TenantID != tenantID {
			continue
		}
		if len(filterStatus) > 0 {
			if _, ok := filterStatus[run.Status]; !ok {
				continue
			}
		}
		out = append(out, cloneRun(run))
	}
	sort.Slice(out, func(i, j int) bool {
		iStart := time.Time{}
		jStart := time.Time{}
		if out[i].StartedAt != nil {
			iStart = *out[i].StartedAt
		}
		if out[j].StartedAt != nil {
			jStart = *out[j].StartedAt
		}
		if iStart.Equal(jStart) {
			return runKey(out[i].TaskID, out[i].RunID) < runKey(out[j].TaskID, out[j].RunID)
		}
		return iStart.Before(jStart)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
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

// --- EventStore ---

func (s *MemoryStore) PutEvent(_ context.Context, event *model.StreamEvent) error {
	if event == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if event.RunID == "" {
		return ErrConflict
	}
	if event.TS == 0 {
		event.TS = time.Now().Unix()
	}
	runEvents := s.events[event.RunID]
	// Deduplicate by sequence within a run.
	for _, existing := range runEvents {
		if existing.Seq == event.Seq {
			return nil
		}
	}
	runEvents = append(runEvents, cloneEvent(event))
	sort.Slice(runEvents, func(i, j int) bool {
		if runEvents[i].Seq == runEvents[j].Seq {
			return runEvents[i].TS < runEvents[j].TS
		}
		return runEvents[i].Seq < runEvents[j].Seq
	})
	// Retention compaction.
	if s.eventRetention > 0 {
		cutoff := time.Now().Add(-s.eventRetention).Unix()
		filtered := runEvents[:0]
		for _, ev := range runEvents {
			if ev.TS >= cutoff {
				filtered = append(filtered, ev)
			}
		}
		runEvents = filtered
	}
	s.events[event.RunID] = runEvents
	return nil
}

func (s *MemoryStore) ReplayEvents(_ context.Context, taskID, runID string, fromSeq, fromTS int64, limit int) ([]*model.StreamEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	runEvents := s.events[runID]
	if len(runEvents) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	out := make([]*model.StreamEvent, 0, minInt(limit, len(runEvents)))
	for _, ev := range runEvents {
		if taskID != "" && ev.TaskID != taskID {
			continue
		}
		if fromSeq > 0 && ev.Seq <= fromSeq {
			continue
		}
		if fromTS > 0 && ev.TS < fromTS {
			continue
		}
		out = append(out, cloneEvent(ev))
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *MemoryStore) CompactEvents(_ context.Context, taskID, runID string, beforeTS int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if beforeTS <= 0 {
		return 0, nil
	}
	runEvents := s.events[runID]
	if len(runEvents) == 0 {
		return 0, nil
	}
	removed := 0
	kept := runEvents[:0]
	for _, ev := range runEvents {
		if taskID != "" && ev.TaskID != taskID {
			kept = append(kept, ev)
			continue
		}
		if ev.TS < beforeTS {
			removed++
			continue
		}
		kept = append(kept, ev)
	}
	s.events[runID] = kept
	return removed, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
