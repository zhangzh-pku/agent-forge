package ops

import (
	"context"
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/queue"
	"github.com/agentforge/agentforge/pkg/state"
)

func TestSchedulerRunOnceRecoversStaleRun(t *testing.T) {
	store := state.NewMemoryStore()
	q := queue.NewMemoryQueue(10)
	s := NewScheduler(store, q, SchedulerConfig{
		StaleFor: 5 * time.Minute,
		Limit:    100,
	})

	now := time.Now().UTC()
	started := now.Add(-30 * time.Minute)
	taskObj := &model.Task{
		TaskID:      "task_1",
		TenantID:    "t1",
		UserID:      "u1",
		Status:      model.TaskStatusRunning,
		ActiveRunID: "run_1",
		Prompt:      "test",
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
	if err := store.PutTask(context.Background(), taskObj); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	report, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Recovery == nil {
		t.Fatal("expected recovery report")
	}
	if report.Recovery.Recovered != 1 {
		t.Fatalf("expected recovered=1, got %+v", report.Recovery)
	}
}

func TestSchedulerRunOnceConsistencyCheckAndRepair(t *testing.T) {
	store := state.NewMemoryStore()
	q := queue.NewMemoryQueue(10)
	s := NewScheduler(store, q, SchedulerConfig{
		Limit:             100,
		ConsistencyCheck:  true,
		ConsistencyRepair: true,
	})

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
		Status:        model.RunStatusSucceeded,
		LastStepIndex: 0,
	}
	if err := store.PutTask(context.Background(), taskObj); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := store.PutStep(context.Background(), &model.Step{
			RunID:     "run_1",
			StepIndex: i,
			Status:    model.StepStatusOK,
			TSStart:   now,
			TSEnd:     now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	report, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Consistency == nil {
		t.Fatal("expected consistency report")
	}
	if len(report.Consistency.Issues) == 0 {
		t.Fatal("expected drift issues")
	}
	if report.Repair == nil || report.Repair.Applied == 0 {
		t.Fatalf("expected repairs applied, got %+v", report.Repair)
	}
}
