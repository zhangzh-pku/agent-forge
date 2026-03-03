package task

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/queue"
	"github.com/agentforge/agentforge/pkg/state"
)

type failingQueue struct{}

func (f *failingQueue) Enqueue(_ context.Context, _ *model.SQSMessage) error {
	return errors.New("enqueue failed")
}

func (f *failingQueue) StartConsumer(_ context.Context, _ queue.MessageHandler) error {
	return nil
}

func newTestService() (*Service, *state.MemoryStore, *queue.MemoryQueue) {
	store := state.NewMemoryStore()
	q := queue.NewMemoryQueue(100)
	svc := NewService(store, q)
	return svc, store, q
}

func TestCreateTask(t *testing.T) {
	svc, store, _ := newTestService()
	ctx := context.Background()

	resp, err := svc.Create(ctx, &CreateRequest{
		TenantID: "tnt_1",
		UserID:   "user_1",
		Prompt:   "test prompt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.TaskID == "" || resp.RunID == "" {
		t.Fatal("expected non-empty task_id and run_id")
	}

	// Verify task in store.
	task, err := store.GetTask(ctx, resp.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.TenantID != "tnt_1" {
		t.Fatalf("expected tnt_1, got %s", task.TenantID)
	}
	if task.Status != model.TaskStatusQueued {
		t.Fatalf("expected QUEUED, got %s", task.Status)
	}
	if task.ActiveRunID != resp.RunID {
		t.Fatalf("expected active_run_id=%s, got %s", resp.RunID, task.ActiveRunID)
	}

	// Verify run in store.
	run, err := store.GetRun(ctx, resp.TaskID, resp.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != model.RunStatusQueued {
		t.Fatalf("expected RUN_QUEUED, got %s", run.Status)
	}
}

func TestCreateTaskWithModelConfig(t *testing.T) {
	svc, store, _ := newTestService()
	ctx := context.Background()

	mc := &model.ModelConfig{ModelID: "gpt-4", Temperature: 0.7, MaxTokens: 1000}
	resp, err := svc.Create(ctx, &CreateRequest{
		TenantID:    "tnt_1",
		UserID:      "user_1",
		Prompt:      "test",
		ModelConfig: mc,
	})
	if err != nil {
		t.Fatal(err)
	}

	task, _ := store.GetTask(ctx, resp.TaskID)
	if task.ModelConfig == nil || task.ModelConfig.ModelID != "gpt-4" {
		t.Fatal("expected model config to be saved")
	}

	run, _ := store.GetRun(ctx, resp.TaskID, resp.RunID)
	if run.ModelConfig == nil || run.ModelConfig.ModelID != "gpt-4" {
		t.Fatal("expected model config in run")
	}
}

func TestCreateTaskIdempotency(t *testing.T) {
	svc, _, _ := newTestService()
	ctx := context.Background()

	req := &CreateRequest{
		TenantID:       "tnt_1",
		UserID:         "user_1",
		Prompt:         "test",
		IdempotencyKey: "idem_1",
	}

	resp1, err := svc.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}

	// Second create with same idempotency key should return same IDs.
	resp2, err := svc.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}

	if resp1.TaskID != resp2.TaskID || resp1.RunID != resp2.RunID {
		t.Fatalf("idempotency failed: resp1=%+v resp2=%+v", resp1, resp2)
	}
}

func TestGetTask(t *testing.T) {
	svc, _, _ := newTestService()
	ctx := context.Background()

	resp, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "test"})

	// Correct tenant.
	task, err := svc.Get(ctx, "tnt_1", resp.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.TaskID != resp.TaskID {
		t.Fatal("task ID mismatch")
	}

	// Wrong tenant should fail (multi-tenant isolation).
	_, err = svc.Get(ctx, "tnt_wrong", resp.TaskID)
	if err == nil {
		t.Fatal("expected error for wrong tenant")
	}
}

func TestGetTaskWrongUser(t *testing.T) {
	svc, _, _ := newTestService()
	ctx := context.Background()

	resp, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "test"})

	_, err := svc.GetForUser(ctx, "tnt_1", "user_2", resp.TaskID)
	if err == nil {
		t.Fatal("expected error for wrong user")
	}
}

func TestAbortTask(t *testing.T) {
	svc, _, _ := newTestService()
	ctx := context.Background()

	resp, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "test"})

	task, err := svc.Abort(ctx, "tnt_1", resp.TaskID, &AbortRequest{TenantID: "tnt_1", Reason: "cancel"})
	if err != nil {
		t.Fatal(err)
	}
	if !task.AbortRequested {
		t.Fatal("expected abort_requested=true")
	}
	if task.AbortReason != "cancel" {
		t.Fatalf("expected reason 'cancel', got %q", task.AbortReason)
	}
}

func TestAbortTaskWrongTenant(t *testing.T) {
	svc, _, _ := newTestService()
	ctx := context.Background()

	resp, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "test"})

	_, err := svc.Abort(ctx, "tnt_wrong", resp.TaskID, &AbortRequest{TenantID: "tnt_wrong", Reason: "cancel"})
	if err == nil {
		t.Fatal("expected error for wrong tenant")
	}
}

func createStepsWithCheckpoints(ctx context.Context, store *state.MemoryStore, runID string, count int) {
	for i := 0; i < count; i++ {
		store.PutStep(ctx, &model.Step{
			RunID:     runID,
			StepIndex: i,
			Type:      model.StepTypeLLMCall,
			Status:    model.StepStatusOK,
			TSStart:   time.Now().UTC(),
			TSEnd:     time.Now().UTC(),
			CheckpointRef: &model.CheckpointRef{
				Memory:    &model.ArtifactRef{S3Key: "memory/test/step.json.gz", SHA256: "abc", Size: 100},
				Workspace: &model.ArtifactRef{S3Key: "workspaces/test/step.tar.gz", SHA256: "def", Size: 200},
			},
		})
	}
}

func TestResumeTask(t *testing.T) {
	svc, store, _ := newTestService()
	ctx := context.Background()

	resp, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "test"})

	// Simulate first run completing with steps.
	store.ClaimRun(ctx, resp.TaskID, resp.RunID)
	createStepsWithCheckpoints(ctx, store, resp.RunID, 3)
	store.CompleteRun(ctx, resp.TaskID, resp.RunID, model.RunStatusSucceeded)
	store.UpdateTaskStatus(ctx, resp.TaskID, []model.TaskStatus{model.TaskStatusQueued, model.TaskStatusRunning}, model.TaskStatusSucceeded)

	// Resume from step 1.
	resumeResp, err := svc.Resume(ctx, resp.TaskID, &ResumeRequest{
		TenantID:      "tnt_1",
		FromRunID:     resp.RunID,
		FromStepIndex: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumeResp.TaskID != resp.TaskID {
		t.Fatal("task ID mismatch")
	}
	if resumeResp.RunID == resp.RunID {
		t.Fatal("expected new run ID")
	}

	// Verify new run.
	run, _ := store.GetRun(ctx, resp.TaskID, resumeResp.RunID)
	if run.ParentRunID != resp.RunID {
		t.Fatalf("expected parent_run_id=%s, got %s", resp.RunID, run.ParentRunID)
	}
	if run.ResumeFromStepIndex == nil || *run.ResumeFromStepIndex != 1 {
		t.Fatal("expected resume_from_step_index=1")
	}

	// Task should be back to QUEUED.
	task, _ := store.GetTask(ctx, resp.TaskID)
	if task.Status != model.TaskStatusQueued {
		t.Fatalf("expected QUEUED after resume, got %s", task.Status)
	}
	if task.ActiveRunID != resumeResp.RunID {
		t.Fatal("expected active_run_id updated")
	}
}

func TestResumeWrongUser(t *testing.T) {
	svc, store, _ := newTestService()
	ctx := context.Background()

	resp, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "test"})
	store.ClaimRun(ctx, resp.TaskID, resp.RunID)
	createStepsWithCheckpoints(ctx, store, resp.RunID, 2)
	store.CompleteRun(ctx, resp.TaskID, resp.RunID, model.RunStatusSucceeded)
	store.UpdateTaskStatus(ctx, resp.TaskID, []model.TaskStatus{model.TaskStatusQueued, model.TaskStatusRunning}, model.TaskStatusSucceeded)

	_, err := svc.Resume(ctx, resp.TaskID, &ResumeRequest{
		TenantID:      "tnt_1",
		UserID:        "user_2",
		FromRunID:     resp.RunID,
		FromStepIndex: 0,
	})
	if err == nil {
		t.Fatal("expected error for wrong user on resume")
	}
}

func TestResumeEnqueueFailureMarksRunAndTaskFailed(t *testing.T) {
	store := state.NewMemoryStore()
	createSvc := NewService(store, queue.NewMemoryQueue(10))
	svc := NewService(store, &failingQueue{})
	ctx := context.Background()

	resp, err := createSvc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "test"})
	if err != nil {
		t.Fatal(err)
	}
	store.ClaimRun(ctx, resp.TaskID, resp.RunID)
	createStepsWithCheckpoints(ctx, store, resp.RunID, 2)
	store.CompleteRun(ctx, resp.TaskID, resp.RunID, model.RunStatusSucceeded)
	store.UpdateTaskStatus(ctx, resp.TaskID, []model.TaskStatus{model.TaskStatusQueued, model.TaskStatusRunning}, model.TaskStatusSucceeded)

	_, err = svc.Resume(ctx, resp.TaskID, &ResumeRequest{
		TenantID:      "tnt_1",
		FromRunID:     resp.RunID,
		FromStepIndex: 0,
	})
	if err == nil {
		t.Fatal("expected enqueue error")
	}

	taskObj, err := store.GetTask(ctx, resp.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if taskObj.Status != model.TaskStatusFailed {
		t.Fatalf("expected task FAILED after enqueue failure, got %s", taskObj.Status)
	}
	if taskObj.ActiveRunID == resp.RunID {
		t.Fatal("expected active run switched to new run")
	}

	run, err := store.GetRun(ctx, resp.TaskID, taskObj.ActiveRunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != model.RunStatusFailed {
		t.Fatalf("expected new run FAILED after enqueue failure, got %s", run.Status)
	}
}

func TestResumeWithModelConfigOverride(t *testing.T) {
	svc, store, _ := newTestService()
	ctx := context.Background()

	mc := &model.ModelConfig{ModelID: "gpt-4", Temperature: 0.5}
	resp, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "test", ModelConfig: mc})
	store.ClaimRun(ctx, resp.TaskID, resp.RunID)
	createStepsWithCheckpoints(ctx, store, resp.RunID, 2)
	store.CompleteRun(ctx, resp.TaskID, resp.RunID, model.RunStatusSucceeded)
	store.UpdateTaskStatus(ctx, resp.TaskID, []model.TaskStatus{model.TaskStatusQueued, model.TaskStatusRunning}, model.TaskStatusSucceeded)

	newMC := &model.ModelConfig{ModelID: "claude-3", Temperature: 0.9}
	resumeResp, _ := svc.Resume(ctx, resp.TaskID, &ResumeRequest{
		TenantID:            "tnt_1",
		FromRunID:           resp.RunID,
		FromStepIndex:       0,
		ModelConfigOverride: newMC,
	})

	run, _ := store.GetRun(ctx, resp.TaskID, resumeResp.RunID)
	if run.ModelConfig.ModelID != "claude-3" {
		t.Fatalf("expected claude-3, got %s", run.ModelConfig.ModelID)
	}
}

func TestResumeEmptyFromRunID(t *testing.T) {
	svc, _, _ := newTestService()
	ctx := context.Background()

	resp, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "test"})

	_, err := svc.Resume(ctx, resp.TaskID, &ResumeRequest{
		TenantID:      "tnt_1",
		FromRunID:     "",
		FromStepIndex: 0,
	})
	if err == nil {
		t.Fatal("expected error for empty from_run_id")
	}
}

func TestResumeWrongTaskRunID(t *testing.T) {
	svc, store, _ := newTestService()
	ctx := context.Background()

	// Create two separate tasks.
	resp1, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "task1"})
	resp2, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "task2"})

	store.ClaimRun(ctx, resp2.TaskID, resp2.RunID)
	createStepsWithCheckpoints(ctx, store, resp2.RunID, 2)
	store.CompleteRun(ctx, resp2.TaskID, resp2.RunID, model.RunStatusSucceeded)

	// Try to resume task1 using task2's run - should fail.
	_, err := svc.Resume(ctx, resp1.TaskID, &ResumeRequest{
		TenantID:      "tnt_1",
		FromRunID:     resp2.RunID,
		FromStepIndex: 0,
	})
	if err == nil {
		t.Fatal("expected error for wrong task's run_id")
	}
}

func TestResumeNegativeStepIndex(t *testing.T) {
	svc, _, _ := newTestService()
	ctx := context.Background()

	resp, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "test"})

	_, err := svc.Resume(ctx, resp.TaskID, &ResumeRequest{
		TenantID:      "tnt_1",
		FromRunID:     resp.RunID,
		FromStepIndex: -1,
	})
	if err == nil {
		t.Fatal("expected error for negative step index")
	}
}

func TestResumeFromNonexistentStep(t *testing.T) {
	svc, store, _ := newTestService()
	ctx := context.Background()

	resp, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "test"})
	store.ClaimRun(ctx, resp.TaskID, resp.RunID)
	// Only create 1 step (index 0).
	createStepsWithCheckpoints(ctx, store, resp.RunID, 1)
	store.CompleteRun(ctx, resp.TaskID, resp.RunID, model.RunStatusSucceeded)
	store.UpdateTaskStatus(ctx, resp.TaskID, []model.TaskStatus{model.TaskStatusQueued, model.TaskStatusRunning}, model.TaskStatusSucceeded)

	// Try to resume from step 5, which doesn't exist.
	_, err := svc.Resume(ctx, resp.TaskID, &ResumeRequest{
		TenantID:      "tnt_1",
		FromRunID:     resp.RunID,
		FromStepIndex: 5,
	})
	if err == nil {
		t.Fatal("expected error for resuming from nonexistent step")
	}
}

func TestResumeClearsAbortFlag(t *testing.T) {
	svc, store, _ := newTestService()
	ctx := context.Background()

	resp, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "test"})
	store.SetAbortRequested(ctx, resp.TaskID, "aborted")
	store.ClaimRun(ctx, resp.TaskID, resp.RunID)
	createStepsWithCheckpoints(ctx, store, resp.RunID, 2)
	store.CompleteRun(ctx, resp.TaskID, resp.RunID, model.RunStatusAborted)
	store.UpdateTaskStatus(ctx, resp.TaskID,
		[]model.TaskStatus{model.TaskStatusQueued, model.TaskStatusRunning},
		model.TaskStatusAborted)

	_, err := svc.Resume(ctx, resp.TaskID, &ResumeRequest{
		TenantID:      "tnt_1",
		FromRunID:     resp.RunID,
		FromStepIndex: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	task, _ := store.GetTask(ctx, resp.TaskID)
	if task.AbortRequested {
		t.Fatal("expected abort cleared after resume")
	}
}

func TestListSteps(t *testing.T) {
	svc, store, _ := newTestService()
	ctx := context.Background()

	resp, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "test"})

	// Write some steps.
	for i := 0; i < 3; i++ {
		store.PutStep(ctx, &model.Step{RunID: resp.RunID, StepIndex: i, Status: model.StepStatusOK})
	}

	steps, err := svc.ListSteps(ctx, "tnt_1", resp.TaskID, resp.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}

	// Wrong tenant.
	_, err = svc.ListSteps(ctx, "tnt_wrong", resp.TaskID, resp.RunID, 0, 100)
	if err == nil {
		t.Fatal("expected error for wrong tenant")
	}

	// Wrong user.
	_, err = svc.ListStepsForUser(ctx, "tnt_1", "user_2", resp.TaskID, resp.RunID, 0, 100)
	if err == nil {
		t.Fatal("expected error for wrong user")
	}
}

func TestListStepsWrongRunID(t *testing.T) {
	svc, store, _ := newTestService()
	ctx := context.Background()

	resp, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "test"})

	// Write some steps under the real run.
	for i := 0; i < 3; i++ {
		store.PutStep(ctx, &model.Step{RunID: resp.RunID, StepIndex: i, Status: model.StepStatusOK})
	}

	// Try to list steps with a run_id that doesn't belong to this task.
	_, err := svc.ListSteps(ctx, "tnt_1", resp.TaskID, "run_nonexistent", 0, 100)
	if err == nil {
		t.Fatal("expected error for run not belonging to task")
	}
}

func TestGetRunForUser(t *testing.T) {
	svc, store, _ := newTestService()
	ctx := context.Background()

	resp, err := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "test"})
	if err != nil {
		t.Fatal(err)
	}

	run, err := svc.GetRunForUser(ctx, "tnt_1", "user_1", resp.TaskID, resp.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.RunID != resp.RunID {
		t.Fatalf("expected run_id %s, got %s", resp.RunID, run.RunID)
	}

	if _, err := svc.GetRunForUser(ctx, "tnt_1", "user_2", resp.TaskID, resp.RunID); err == nil {
		t.Fatal("expected error for wrong user")
	}

	if _, err := svc.GetRunForUser(ctx, "tnt_1", "user_1", resp.TaskID, "run_missing"); err == nil {
		t.Fatal("expected error for missing run")
	}

	// Ensure usage fields are wired through store values.
	if err := store.AddRunUsage(ctx, resp.TaskID, resp.RunID, &model.TokenUsage{Input: 1, Output: 2, Total: 3}, 0.01); err != nil {
		t.Fatal(err)
	}
	run, err = svc.GetRunForUser(ctx, "tnt_1", "user_1", resp.TaskID, resp.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.TotalTokenUsage == nil || run.TotalTokenUsage.Total != 3 {
		t.Fatalf("expected run usage total=3, got %+v", run.TotalTokenUsage)
	}
}

func TestListStepsCrossTaskRun(t *testing.T) {
	svc, store, _ := newTestService()
	ctx := context.Background()

	// Create two tasks.
	resp1, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "task1"})
	resp2, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "task2"})

	// Write steps under task2's run.
	for i := 0; i < 3; i++ {
		store.PutStep(ctx, &model.Step{RunID: resp2.RunID, StepIndex: i, Status: model.StepStatusOK})
	}

	// Try listing task1 with task2's run — should fail since run belongs to task2.
	_, err := svc.ListSteps(ctx, "tnt_1", resp1.TaskID, resp2.RunID, 0, 100)
	if err == nil {
		t.Fatal("expected error for cross-task run listing")
	}
}

func TestResumeModelConfigInheritsFromTask(t *testing.T) {
	svc, store, _ := newTestService()
	ctx := context.Background()

	mc := &model.ModelConfig{ModelID: "gpt-4", Temperature: 0.7}
	resp, _ := svc.Create(ctx, &CreateRequest{TenantID: "tnt_1", UserID: "user_1", Prompt: "test", ModelConfig: mc})
	store.ClaimRun(ctx, resp.TaskID, resp.RunID)
	createStepsWithCheckpoints(ctx, store, resp.RunID, 2)
	store.CompleteRun(ctx, resp.TaskID, resp.RunID, model.RunStatusSucceeded)
	store.UpdateTaskStatus(ctx, resp.TaskID, []model.TaskStatus{model.TaskStatusQueued, model.TaskStatusRunning}, model.TaskStatusSucceeded)

	// Resume without model_config_override — should inherit from task.
	resumeResp, err := svc.Resume(ctx, resp.TaskID, &ResumeRequest{
		TenantID:      "tnt_1",
		FromRunID:     resp.RunID,
		FromStepIndex: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	run, _ := store.GetRun(ctx, resp.TaskID, resumeResp.RunID)
	if run.ModelConfig == nil || run.ModelConfig.ModelID != "gpt-4" {
		t.Fatal("expected model config inherited from task")
	}
}
