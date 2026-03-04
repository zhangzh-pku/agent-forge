package state

import (
	"context"
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
)

func TestPutAndGetTask(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	task := &model.Task{
		TaskID:    "task_1",
		TenantID:  "tnt_1",
		UserID:    "user_1",
		Status:    model.TaskStatusQueued,
		Prompt:    "test",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetTask(ctx, "task_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TenantID != "tnt_1" {
		t.Fatalf("expected tnt_1, got %s", got.TenantID)
	}
	if got.Prompt != "test" {
		t.Fatalf("expected 'test', got %s", got.Prompt)
	}
}

func TestPutTaskDuplicate(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	task := &model.Task{TaskID: "task_1", TenantID: "tnt_1", Status: model.TaskStatusQueued, CreatedAt: now, UpdatedAt: now}
	if err := s.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Duplicate should fail.
	task2 := &model.Task{TaskID: "task_1", TenantID: "tnt_1", Status: model.TaskStatusQueued, CreatedAt: now, UpdatedAt: now}
	if err := s.PutTask(ctx, task2); err != ErrAlreadyExists {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestPutTaskIdempotencyKey(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	task := &model.Task{
		TaskID:         "task_1",
		TenantID:       "tnt_1",
		IdempotencyKey: "ik_1",
		Status:         model.TaskStatusQueued,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Same idempotency key, different task ID should fail.
	task2 := &model.Task{
		TaskID:         "task_2",
		TenantID:       "tnt_1",
		IdempotencyKey: "ik_1",
		Status:         model.TaskStatusQueued,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.PutTask(ctx, task2); err != ErrAlreadyExists {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}

	// Different tenant, same idempotency key should succeed (tenant isolation).
	task3 := &model.Task{
		TaskID:         "task_3",
		TenantID:       "tnt_2",
		IdempotencyKey: "ik_1",
		Status:         model.TaskStatusQueued,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.PutTask(ctx, task3); err != nil {
		t.Fatalf("expected success for different tenant, got %v", err)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	s := NewMemoryStore()
	_, err := s.GetTask(context.Background(), "nonexistent")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetTaskByIdempotencyKey(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	task := &model.Task{
		TaskID:         "task_1",
		TenantID:       "tnt_1",
		IdempotencyKey: "ik_1",
		Status:         model.TaskStatusQueued,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	s.PutTask(ctx, task)

	got, err := s.GetTaskByIdempotencyKey(ctx, "tnt_1", "ik_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskID != "task_1" {
		t.Fatalf("expected task_1, got %s", got.TaskID)
	}

	// Wrong tenant.
	_, err = s.GetTaskByIdempotencyKey(ctx, "tnt_wrong", "ik_1")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateTaskStatus(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	task := &model.Task{TaskID: "task_1", Status: model.TaskStatusQueued, CreatedAt: now, UpdatedAt: now}
	s.PutTask(ctx, task)

	// Valid transition.
	if err := s.UpdateTaskStatus(ctx, "task_1", []model.TaskStatus{model.TaskStatusQueued}, model.TaskStatusRunning); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetTask(ctx, "task_1")
	if got.Status != model.TaskStatusRunning {
		t.Fatalf("expected RUNNING, got %s", got.Status)
	}

	// Invalid transition.
	if err := s.UpdateTaskStatus(ctx, "task_1", []model.TaskStatus{model.TaskStatusQueued}, model.TaskStatusSucceeded); err != ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestUpdateTaskStatusForRun(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	task := &model.Task{
		TaskID:      "task_1",
		ActiveRunID: "run_1",
		Status:      model.TaskStatusQueued,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.PutTask(ctx, task)

	// Valid transition when active_run_id matches.
	if err := s.UpdateTaskStatusForRun(ctx, "task_1", "run_1", []model.TaskStatus{model.TaskStatusQueued}, model.TaskStatusRunning); err != nil {
		t.Fatal(err)
	}

	// Reject mismatched active run.
	if err := s.UpdateTaskStatusForRun(ctx, "task_1", "run_2", []model.TaskStatus{model.TaskStatusRunning}, model.TaskStatusSucceeded); err != ErrConflict {
		t.Fatalf("expected ErrConflict for wrong run, got %v", err)
	}
}

func TestSetAbortRequested(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	task := &model.Task{TaskID: "task_1", Status: model.TaskStatusRunning, CreatedAt: now, UpdatedAt: now}
	s.PutTask(ctx, task)

	if err := s.SetAbortRequested(ctx, "task_1", "user cancel"); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetTask(ctx, "task_1")
	if !got.AbortRequested {
		t.Fatal("expected abort_requested=true")
	}
	if got.AbortReason != "user cancel" {
		t.Fatalf("expected 'user cancel', got %q", got.AbortReason)
	}
	if got.AbortTS == nil {
		t.Fatal("expected non-nil abort_ts")
	}

	requested, reason, err := s.IsAbortRequested(ctx, "task_1")
	if err != nil {
		t.Fatal(err)
	}
	if !requested || reason != "user cancel" {
		t.Fatalf("unexpected abort state: requested=%v reason=%q", requested, reason)
	}
}

func TestClearAbortRequested(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	task := &model.Task{TaskID: "task_1", Status: model.TaskStatusRunning, CreatedAt: now, UpdatedAt: now}
	s.PutTask(ctx, task)
	s.SetAbortRequested(ctx, "task_1", "test")
	s.ClearAbortRequested(ctx, "task_1")

	got, _ := s.GetTask(ctx, "task_1")
	if got.AbortRequested {
		t.Fatal("expected abort_requested=false after clear")
	}
	if got.AbortReason != "" {
		t.Fatal("expected empty abort_reason")
	}
	if got.AbortTS != nil {
		t.Fatal("expected nil abort_ts")
	}
}

func TestSetActiveRun(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	task := &model.Task{TaskID: "task_1", ActiveRunID: "run_1", CreatedAt: now, UpdatedAt: now}
	s.PutTask(ctx, task)

	if err := s.SetActiveRun(ctx, "task_1", "run_2"); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetTask(ctx, "task_1")
	if got.ActiveRunID != "run_2" {
		t.Fatalf("expected run_2, got %s", got.ActiveRunID)
	}
}

func TestApplyCreateTransitionAtomicSuccess(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	task := &model.Task{
		TaskID:         "task_1",
		TenantID:       "tnt_1",
		UserID:         "user_1",
		Status:         model.TaskStatusQueued,
		ActiveRunID:    "run_1",
		IdempotencyKey: "idem_1",
		Prompt:         "test prompt",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	run := &model.Run{
		TaskID:      "task_1",
		RunID:       "run_1",
		TenantID:    "tnt_1",
		Status:      model.RunStatusQueued,
		ModelConfig: task.ModelConfig,
	}

	if err := s.ApplyCreateTransition(ctx, task, run); err != nil {
		t.Fatalf("ApplyCreateTransition error: %v", err)
	}

	gotTask, err := s.GetTask(ctx, "task_1")
	if err != nil {
		t.Fatal(err)
	}
	if gotTask.ActiveRunID != "run_1" {
		t.Fatalf("expected active run run_1, got %s", gotTask.ActiveRunID)
	}

	gotRun, err := s.GetRun(ctx, "task_1", "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if gotRun.Status != model.RunStatusQueued {
		t.Fatalf("expected queued run, got %s", gotRun.Status)
	}

	gotByIDKey, err := s.GetTaskByIdempotencyKey(ctx, "tnt_1", "idem_1")
	if err != nil {
		t.Fatal(err)
	}
	if gotByIDKey.TaskID != "task_1" {
		t.Fatalf("expected idempotency lookup task_1, got %s", gotByIDKey.TaskID)
	}
}

func TestApplyCreateTransitionNoPartialOnConflict(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	existingTask := &model.Task{
		TaskID:         "task_existing",
		TenantID:       "tnt_1",
		UserID:         "user_1",
		Status:         model.TaskStatusQueued,
		ActiveRunID:    "run_existing",
		IdempotencyKey: "idem_1",
		Prompt:         "existing",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	existingRun := &model.Run{
		TaskID:   "task_existing",
		RunID:    "run_existing",
		TenantID: "tnt_1",
		Status:   model.RunStatusQueued,
	}
	if err := s.ApplyCreateTransition(ctx, existingTask, existingRun); err != nil {
		t.Fatalf("seed ApplyCreateTransition error: %v", err)
	}

	// New task with conflicting idempotency key should fail atomically.
	newTask := &model.Task{
		TaskID:         "task_new",
		TenantID:       "tnt_1",
		UserID:         "user_1",
		Status:         model.TaskStatusQueued,
		ActiveRunID:    "run_new",
		IdempotencyKey: "idem_1",
		Prompt:         "new",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	newRun := &model.Run{
		TaskID:   "task_new",
		RunID:    "run_new",
		TenantID: "tnt_1",
		Status:   model.RunStatusQueued,
	}
	if err := s.ApplyCreateTransition(ctx, newTask, newRun); err != ErrAlreadyExists {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}

	if _, err := s.GetTask(ctx, "task_new"); err != ErrNotFound {
		t.Fatalf("expected no task_new created, got err=%v", err)
	}
	if _, err := s.GetRun(ctx, "task_new", "run_new"); err != ErrNotFound {
		t.Fatalf("expected no run_new created, got err=%v", err)
	}
}

func TestApplyResumeTransitionAtomicSuccess(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	task := &model.Task{
		TaskID:         "task_1",
		TenantID:       "tnt_1",
		UserID:         "user_1",
		Status:         model.TaskStatusSucceeded,
		ActiveRunID:    "run_old",
		AbortRequested: true,
		AbortReason:    "previous abort",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	stepIdx := 1
	run := &model.Run{
		TaskID:              "task_1",
		RunID:               "run_new",
		TenantID:            "tnt_1",
		Status:              model.RunStatusQueued,
		ParentRunID:         "run_old",
		ResumeFromStepIndex: &stepIdx,
	}

	err := s.ApplyResumeTransition(ctx, "task_1", run, []model.TaskStatus{
		model.TaskStatusSucceeded, model.TaskStatusFailed, model.TaskStatusAborted,
	}, model.TaskStatusQueued)
	if err != nil {
		t.Fatal(err)
	}

	gotTask, _ := s.GetTask(ctx, "task_1")
	if gotTask.ActiveRunID != "run_new" {
		t.Fatalf("expected active run run_new, got %s", gotTask.ActiveRunID)
	}
	if gotTask.Status != model.TaskStatusQueued {
		t.Fatalf("expected QUEUED, got %s", gotTask.Status)
	}
	if gotTask.AbortRequested || gotTask.AbortReason != "" || gotTask.AbortTS != nil {
		t.Fatalf("expected abort flags cleared, got abort_requested=%v reason=%q", gotTask.AbortRequested, gotTask.AbortReason)
	}

	gotRun, err := s.GetRun(ctx, "task_1", "run_new")
	if err != nil {
		t.Fatal(err)
	}
	if gotRun.ParentRunID != "run_old" {
		t.Fatalf("expected parent run run_old, got %s", gotRun.ParentRunID)
	}
}

func TestApplyResumeTransitionNoPartialOnConflict(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	task := &model.Task{
		TaskID:         "task_1",
		TenantID:       "tnt_1",
		UserID:         "user_1",
		Status:         model.TaskStatusRunning,
		ActiveRunID:    "run_old",
		AbortRequested: true,
		AbortReason:    "keep me",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	run := &model.Run{
		TaskID:   "task_1",
		RunID:    "run_new",
		TenantID: "tnt_1",
		Status:   model.RunStatusQueued,
	}

	// Disallow RUNNING -> QUEUED for this operation to force conflict.
	err := s.ApplyResumeTransition(ctx, "task_1", run, []model.TaskStatus{
		model.TaskStatusSucceeded, model.TaskStatusFailed, model.TaskStatusAborted,
	}, model.TaskStatusQueued)
	if err != ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	gotTask, _ := s.GetTask(ctx, "task_1")
	if gotTask.ActiveRunID != "run_old" {
		t.Fatalf("expected active run unchanged run_old, got %s", gotTask.ActiveRunID)
	}
	if gotTask.Status != model.TaskStatusRunning {
		t.Fatalf("expected status unchanged RUNNING, got %s", gotTask.Status)
	}
	if !gotTask.AbortRequested || gotTask.AbortReason != "keep me" {
		t.Fatalf("expected abort flags unchanged, got abort_requested=%v reason=%q", gotTask.AbortRequested, gotTask.AbortReason)
	}
	if _, err := s.GetRun(ctx, "task_1", "run_new"); err != ErrNotFound {
		t.Fatalf("expected no run_new created, got err=%v", err)
	}
}

func TestPutAndGetRun(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	run := &model.Run{TaskID: "task_1", RunID: "run_1", TenantID: "tnt_1", Status: model.RunStatusQueued}
	if err := s.PutRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetRun(ctx, "task_1", "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TenantID != "tnt_1" {
		t.Fatalf("expected tnt_1, got %s", got.TenantID)
	}
}

func TestAddRunUsage(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	run := &model.Run{TaskID: "task_1", RunID: "run_1", TenantID: "tnt_1", Status: model.RunStatusRunning}
	if err := s.PutRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	if err := s.AddRunUsage(ctx, "task_1", "run_1", &model.TokenUsage{Input: 10, Output: 20, Total: 30}, 0.12); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRunUsage(ctx, "task_1", "run_1", &model.TokenUsage{Input: 1, Output: 2, Total: 3}, 0.01); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetRun(ctx, "task_1", "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalTokenUsage == nil || got.TotalTokenUsage.Total != 33 {
		t.Fatalf("expected total tokens 33, got %+v", got.TotalTokenUsage)
	}
	if got.TotalCostUSD < 0.1299 || got.TotalCostUSD > 0.1301 {
		t.Fatalf("expected total cost about 0.13, got %f", got.TotalCostUSD)
	}
}

func TestResetRunToQueued(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	run := &model.Run{
		TaskID:    "task_1",
		RunID:     "run_1",
		Status:    model.RunStatusRunning,
		StartedAt: &now,
		EndedAt:   &now,
	}
	if err := s.PutRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetRunToQueued(ctx, "task_1", "run_1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(ctx, "task_1", "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.RunStatusQueued {
		t.Fatalf("expected RUN_QUEUED, got %s", got.Status)
	}
	if got.StartedAt != nil || got.EndedAt != nil {
		t.Fatal("expected started_at and ended_at cleared")
	}
	if got.QueuedAt == nil {
		t.Fatal("expected queued_at to be set when resetting to RUN_QUEUED")
	}
}

func TestPutRunQueuedSetsQueuedAt(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	run := &model.Run{
		TaskID:   "task_1",
		RunID:    "run_1",
		TenantID: "tnt_1",
		Status:   model.RunStatusQueued,
	}
	if err := s.PutRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(ctx, "task_1", "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.QueuedAt == nil {
		t.Fatal("expected queued_at to be populated for queued run")
	}
}

func TestListTasksAndRuns(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	_ = s.PutTask(ctx, &model.Task{TaskID: "task_a", TenantID: "t1", Status: model.TaskStatusQueued, CreatedAt: now, UpdatedAt: now})
	_ = s.PutTask(ctx, &model.Task{TaskID: "task_b", TenantID: "t1", Status: model.TaskStatusRunning, CreatedAt: now.Add(time.Second), UpdatedAt: now})
	_ = s.PutTask(ctx, &model.Task{TaskID: "task_c", TenantID: "t2", Status: model.TaskStatusRunning, CreatedAt: now.Add(2 * time.Second), UpdatedAt: now})

	tasks, err := s.ListTasks(ctx, "t1", []model.TaskStatus{model.TaskStatusRunning}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].TaskID != "task_b" {
		t.Fatalf("unexpected task list: %+v", tasks)
	}

	_ = s.PutRun(ctx, &model.Run{TaskID: "task_a", RunID: "run_1", TenantID: "t1", Status: model.RunStatusQueued})
	_ = s.PutRun(ctx, &model.Run{TaskID: "task_b", RunID: "run_2", TenantID: "t1", Status: model.RunStatusRunning})
	_ = s.PutRun(ctx, &model.Run{TaskID: "task_c", RunID: "run_3", TenantID: "t2", Status: model.RunStatusRunning})

	runs, err := s.ListRuns(ctx, "t1", []model.RunStatus{model.RunStatusRunning}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].RunID != "run_2" {
		t.Fatalf("unexpected run list: %+v", runs)
	}
}

func TestPutRunDuplicate(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	run := &model.Run{TaskID: "task_1", RunID: "run_1", Status: model.RunStatusQueued}
	s.PutRun(ctx, run)

	run2 := &model.Run{TaskID: "task_1", RunID: "run_1", Status: model.RunStatusQueued}
	if err := s.PutRun(ctx, run2); err != ErrAlreadyExists {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestClaimRun(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	run := &model.Run{TaskID: "task_1", RunID: "run_1", Status: model.RunStatusQueued}
	s.PutRun(ctx, run)

	// First claim should succeed.
	if err := s.ClaimRun(ctx, "task_1", "run_1"); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetRun(ctx, "task_1", "run_1")
	if got.Status != model.RunStatusRunning {
		t.Fatalf("expected RUNNING, got %s", got.Status)
	}
	if got.StartedAt == nil {
		t.Fatal("expected non-nil started_at")
	}

	// Second claim should fail (idempotent).
	if err := s.ClaimRun(ctx, "task_1", "run_1"); err != ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestCompleteRun(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	run := &model.Run{TaskID: "task_1", RunID: "run_1", Status: model.RunStatusRunning}
	s.PutRun(ctx, run)

	if err := s.CompleteRun(ctx, "task_1", "run_1", model.RunStatusSucceeded); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetRun(ctx, "task_1", "run_1")
	if got.Status != model.RunStatusSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s", got.Status)
	}
	if got.EndedAt == nil {
		t.Fatal("expected non-nil ended_at")
	}
}

func TestUpdateLastStepIndex(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	run := &model.Run{TaskID: "task_1", RunID: "run_1", Status: model.RunStatusRunning}
	s.PutRun(ctx, run)

	s.UpdateLastStepIndex(ctx, "task_1", "run_1", 5)

	got, _ := s.GetRun(ctx, "task_1", "run_1")
	if got.LastStepIndex != 5 {
		t.Fatalf("expected 5, got %d", got.LastStepIndex)
	}
}

func TestPutAndGetStep(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	step := &model.Step{
		RunID:     "run_1",
		StepIndex: 0,
		Type:      model.StepTypeLLMCall,
		Status:    model.StepStatusOK,
		TSStart:   time.Now().UTC(),
		TSEnd:     time.Now().UTC(),
	}
	if err := s.PutStep(ctx, step); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetStep(ctx, "run_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != model.StepTypeLLMCall {
		t.Fatalf("expected llm_call, got %s", got.Type)
	}
}

func TestPutStepIdempotent(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	step := &model.Step{RunID: "run_1", StepIndex: 0, Type: model.StepTypeLLMCall, Status: model.StepStatusOK}
	s.PutStep(ctx, step)

	// Duplicate step write should fail.
	step2 := &model.Step{RunID: "run_1", StepIndex: 0, Type: model.StepTypeToolCall, Status: model.StepStatusOK}
	if err := s.PutStep(ctx, step2); err != ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestListSteps(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 0; i < 5; i++ {
		s.PutStep(ctx, &model.Step{RunID: "run_1", StepIndex: i, Status: model.StepStatusOK, TSStart: now, TSEnd: now})
	}

	// List all.
	steps, err := s.ListSteps(ctx, "run_1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 5 {
		t.Fatalf("expected 5 steps, got %d", len(steps))
	}

	// List with offset.
	steps, _ = s.ListSteps(ctx, "run_1", 2, 100)
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps from index 2, got %d", len(steps))
	}
	if steps[0].StepIndex != 2 {
		t.Fatalf("expected first step index 2, got %d", steps[0].StepIndex)
	}

	// List with limit.
	steps, _ = s.ListSteps(ctx, "run_1", 0, 2)
	if len(steps) != 2 {
		t.Fatalf("expected 2 steps with limit, got %d", len(steps))
	}

	// Steps should be ordered.
	for i := 1; i < len(steps); i++ {
		if steps[i].StepIndex <= steps[i-1].StepIndex {
			t.Fatal("steps not ordered")
		}
	}
}

func TestConnectionStore(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	conn := &model.Connection{
		ConnectionID: "conn_1",
		TenantID:     "tnt_1",
		TaskID:       "task_1",
		ConnectedAt:  time.Now().UTC(),
	}
	if err := s.PutConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}

	// Get connections by task.
	conns, err := s.GetConnectionsByTask(ctx, "task_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(conns))
	}

	// Delete connection.
	if err := s.DeleteConnection(ctx, "conn_1"); err != nil {
		t.Fatal(err)
	}

	conns, _ = s.GetConnectionsByTask(ctx, "task_1")
	if len(conns) != 0 {
		t.Fatalf("expected 0 connections after delete, got %d", len(conns))
	}
}

func TestGetConnectionsByTaskMultiple(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	s.PutConnection(ctx, &model.Connection{ConnectionID: "conn_1", TaskID: "task_1", ConnectedAt: time.Now().UTC()})
	s.PutConnection(ctx, &model.Connection{ConnectionID: "conn_2", TaskID: "task_1", ConnectedAt: time.Now().UTC()})
	s.PutConnection(ctx, &model.Connection{ConnectionID: "conn_3", TaskID: "task_2", ConnectedAt: time.Now().UTC()})

	conns, _ := s.GetConnectionsByTask(ctx, "task_1")
	if len(conns) != 2 {
		t.Fatalf("expected 2 connections for task_1, got %d", len(conns))
	}

	conns, _ = s.GetConnectionsByTask(ctx, "task_2")
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection for task_2, got %d", len(conns))
	}
}

func TestGetConnection(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	conn := &model.Connection{
		ConnectionID: "conn_1",
		TenantID:     "tnt_1",
		TaskID:       "task_1",
		ConnectedAt:  time.Now().UTC(),
	}
	s.PutConnection(ctx, conn)

	got, err := s.GetConnection(ctx, "conn_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.TenantID != "tnt_1" {
		t.Fatalf("expected tnt_1, got %s", got.TenantID)
	}

	// Nonexistent connection.
	_, err = s.GetConnection(ctx, "nonexistent")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	// Create a task.
	task := &model.Task{TaskID: "task_c", TenantID: "tnt_1", Status: model.TaskStatusQueued, CreatedAt: now, UpdatedAt: now}
	s.PutTask(ctx, task)

	// Run concurrent reads and writes.
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				s.GetTask(ctx, "task_c")
				s.SetAbortRequested(ctx, "task_c", "test")
				s.ClearAbortRequested(ctx, "task_c")
			}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestDeepCloneModelConfig(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	mc := &model.ModelConfig{ModelID: "gpt-4", Temperature: 0.7, MaxTokens: 4096, FallbackModelIDs: []string{"gpt-4o-mini"}}
	task := &model.Task{
		TaskID:      "task_dc",
		TenantID:    "tnt_1",
		Status:      model.TaskStatusQueued,
		ModelConfig: mc,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.PutTask(ctx, task)

	// Mutate the original ModelConfig.
	mc.ModelID = "gpt-3.5"
	mc.MaxTokens = 1
	mc.FallbackModelIDs[0] = "other"

	// Stored copy should be unaffected.
	got, _ := s.GetTask(ctx, "task_dc")
	if got.ModelConfig.ModelID != "gpt-4" {
		t.Fatalf("expected gpt-4, got %s — shallow copy leak", got.ModelConfig.ModelID)
	}
	if got.ModelConfig.MaxTokens != 4096 {
		t.Fatalf("expected 4096, got %d — shallow copy leak", got.ModelConfig.MaxTokens)
	}
	if len(got.ModelConfig.FallbackModelIDs) != 1 || got.ModelConfig.FallbackModelIDs[0] != "gpt-4o-mini" {
		t.Fatalf("expected deep-copied fallback model IDs, got %+v", got.ModelConfig.FallbackModelIDs)
	}

	// Also mutating the returned clone should not affect the store.
	got.ModelConfig.ModelID = "claude-3"
	got2, _ := s.GetTask(ctx, "task_dc")
	if got2.ModelConfig.ModelID != "gpt-4" {
		t.Fatalf("expected gpt-4 after clone mutation, got %s", got2.ModelConfig.ModelID)
	}
}

func TestTaskMutationReturnsClone(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()

	task := &model.Task{TaskID: "task_1", TenantID: "tnt_1", Status: model.TaskStatusQueued, CreatedAt: now, UpdatedAt: now}
	s.PutTask(ctx, task)

	// Modifying the returned task should not affect the store.
	got, _ := s.GetTask(ctx, "task_1")
	got.Status = model.TaskStatusFailed

	got2, _ := s.GetTask(ctx, "task_1")
	if got2.Status != model.TaskStatusQueued {
		t.Fatal("store mutation via returned clone")
	}
}

func TestEventReplayAndCompaction(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	now := time.Now().Unix()
	for i := int64(1); i <= 5; i++ {
		if err := s.PutEvent(ctx, &model.StreamEvent{
			TaskID: "task_1",
			RunID:  "run_1",
			Seq:    i,
			TS:     now + i,
			Type:   model.StreamEventStepEnd,
		}); err != nil {
			t.Fatal(err)
		}
	}

	events, err := s.ReplayEvents(ctx, "task_1", "run_1", 2, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events from seq>2, got %d", len(events))
	}
	if events[0].Seq != 3 {
		t.Fatalf("expected first seq 3, got %d", events[0].Seq)
	}

	removed, err := s.CompactEvents(ctx, "task_1", "run_1", now+4)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Fatalf("expected 3 compacted events, got %d", removed)
	}
	events, _ = s.ReplayEvents(ctx, "task_1", "run_1", 0, 0, 10)
	if len(events) != 2 {
		t.Fatalf("expected 2 events after compaction, got %d", len(events))
	}
}

func TestEventStoreIsolationByTaskRunKey(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()

	evA := &model.StreamEvent{
		TaskID: "task_a",
		RunID:  "run_shared",
		Seq:    1,
		TS:     time.Now().Unix(),
		Type:   model.StreamEventStepEnd,
	}
	evB := &model.StreamEvent{
		TaskID: "task_b",
		RunID:  "run_shared",
		Seq:    1, // same seq/run as evA, different task
		TS:     time.Now().Unix() + 1,
		Type:   model.StreamEventStepEnd,
	}
	if err := s.PutEvent(ctx, evA); err != nil {
		t.Fatal(err)
	}
	if err := s.PutEvent(ctx, evB); err != nil {
		t.Fatal(err)
	}

	gotA, err := s.ReplayEvents(ctx, "task_a", "run_shared", 0, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotA) != 1 || gotA[0].TaskID != "task_a" {
		t.Fatalf("expected isolated event for task_a, got %+v", gotA)
	}

	gotB, err := s.ReplayEvents(ctx, "task_b", "run_shared", 0, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotB) != 1 || gotB[0].TaskID != "task_b" {
		t.Fatalf("expected isolated event for task_b, got %+v", gotB)
	}

	removed, err := s.CompactEvents(ctx, "task_a", "run_shared", time.Now().Add(time.Minute).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 removed event for task_a, got %d", removed)
	}
	gotB, err = s.ReplayEvents(ctx, "task_b", "run_shared", 0, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotB) != 1 {
		t.Fatalf("expected task_b events unaffected by task_a compaction, got %d", len(gotB))
	}
}
