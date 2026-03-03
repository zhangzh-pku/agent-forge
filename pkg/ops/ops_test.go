package ops

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/queue"
	"github.com/agentforge/agentforge/pkg/state"
)

func TestRecoverStaleRuns(t *testing.T) {
	store := state.NewMemoryStore()
	q := queue.NewMemoryQueue(10)
	rec := NewRecoverer(store, q)
	ctx := context.Background()

	now := time.Now().UTC()
	started := now.Add(-30 * time.Minute)
	taskObj := &model.Task{
		TaskID:      "task_1",
		TenantID:    "t1",
		UserID:      "u1",
		Status:      model.TaskStatusRunning,
		ActiveRunID: "run_1",
		Prompt:      "fix bug in go code",
		CreatedAt:   now.Add(-40 * time.Minute),
		UpdatedAt:   now.Add(-20 * time.Minute),
	}
	run := &model.Run{
		TaskID:    "task_1",
		RunID:     "run_1",
		TenantID:  "t1",
		Status:    model.RunStatusRunning,
		StartedAt: &started,
	}
	if err := store.PutTask(ctx, taskObj); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	report, err := rec.RecoverStaleRuns(ctx, "t1", 10*time.Minute, 50)
	if err != nil {
		t.Fatal(err)
	}
	if report.Recovered != 1 {
		t.Fatalf("expected recovered=1, got %+v", report)
	}

	updated, err := store.GetRun(ctx, "task_1", "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.RunStatusQueued {
		t.Fatalf("expected run reset to RUN_QUEUED, got %s", updated.Status)
	}

	consumeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var got atomic.Int64
	go q.StartConsumer(consumeCtx, func(_ context.Context, msg *model.SQSMessage) error {
		if msg.TaskID == "task_1" && msg.RunID == "run_1" {
			got.Add(1)
			cancel()
		}
		return nil
	})
	<-consumeCtx.Done()
	if got.Load() != 1 {
		t.Fatalf("expected recovered message to be re-enqueued, got=%d", got.Load())
	}
}

func TestConsistencyCheckAndRepair(t *testing.T) {
	store := state.NewMemoryStore()
	checker := NewConsistencyChecker(store)
	ctx := context.Background()
	now := time.Now().UTC()

	taskObj := &model.Task{
		TaskID:      "task_1",
		TenantID:    "t1",
		UserID:      "u1",
		Status:      model.TaskStatusRunning,
		ActiveRunID: "run_1",
		Prompt:      "task",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	run := &model.Run{
		TaskID:        "task_1",
		RunID:         "run_1",
		TenantID:      "t1",
		Status:        model.RunStatusSucceeded, // mismatch with task RUNNING
		LastStepIndex: 0,
	}
	if err := store.PutTask(ctx, taskObj); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := store.PutStep(ctx, &model.Step{
			RunID:     "run_1",
			StepIndex: i,
			Status:    model.StepStatusOK,
			TSStart:   now,
			TSEnd:     now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	report, err := checker.Check(ctx, "t1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Issues) < 2 {
		t.Fatalf("expected at least 2 issues, got %+v", report.Issues)
	}

	repair, err := checker.Repair(ctx, report.Issues)
	if err != nil {
		t.Fatal(err)
	}
	if repair.Applied == 0 {
		t.Fatalf("expected at least one repair, got %+v", repair)
	}

	taskAfter, _ := store.GetTask(ctx, "task_1")
	if taskAfter.Status != model.TaskStatusSucceeded {
		t.Fatalf("expected task status repaired to SUCCEEDED, got %s", taskAfter.Status)
	}
	runAfter, _ := store.GetRun(ctx, "task_1", "run_1")
	if runAfter.LastStepIndex != 2 {
		t.Fatalf("expected run last_step_index repaired to 2, got %d", runAfter.LastStepIndex)
	}
}
