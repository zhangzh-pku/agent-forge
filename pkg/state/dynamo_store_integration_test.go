package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestDynamoStoreIntegration_BasicLifecycleAndAtomicUsage(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("AGENTFORGE_INTEGRATION_AWS_ENDPOINT"))
	if endpoint == "" {
		t.Skip("set AGENTFORGE_INTEGRATION_AWS_ENDPOINT to run DynamoDB integration tests")
	}

	ctx := context.Background()
	client := integrationDynamoClient(t, endpoint)

	cfg := createIntegrationStateTables(t, ctx, client, fmt.Sprintf("af-it-%d", time.Now().UnixNano()))
	store, err := NewDynamoStore(client, cfg)
	if err != nil {
		t.Fatalf("new dynamo store: %v", err)
	}

	task := &model.Task{
		TaskID:      "task_it_1",
		TenantID:    "tnt_it",
		UserID:      "user_it",
		Status:      model.TaskStatusQueued,
		ActiveRunID: "run_it_1",
		Prompt:      "integration test prompt",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("put task: %v", err)
	}

	run := &model.Run{
		TaskID:   task.TaskID,
		RunID:    "run_it_1",
		TenantID: task.TenantID,
		Status:   model.RunStatusQueued,
	}
	if err := store.PutRun(ctx, run); err != nil {
		t.Fatalf("put run: %v", err)
	}

	// Additional rows to verify tenant/status listing uses indexed query semantics.
	otherTenantTask := &model.Task{
		TaskID:      "task_it_other_tenant",
		TenantID:    "tnt_other",
		UserID:      "user_it",
		Status:      model.TaskStatusQueued,
		ActiveRunID: "run_it_other_tenant",
		Prompt:      "other tenant",
		CreatedAt:   time.Now().UTC().Add(time.Second),
		UpdatedAt:   time.Now().UTC().Add(time.Second),
	}
	if err := store.PutTask(ctx, otherTenantTask); err != nil {
		t.Fatalf("put other-tenant task: %v", err)
	}
	if err := store.PutRun(ctx, &model.Run{
		TaskID:   otherTenantTask.TaskID,
		RunID:    "run_it_other_tenant",
		TenantID: otherTenantTask.TenantID,
		Status:   model.RunStatusQueued,
	}); err != nil {
		t.Fatalf("put other-tenant run: %v", err)
	}

	sameTenantFailedTask := &model.Task{
		TaskID:      "task_it_failed",
		TenantID:    task.TenantID,
		UserID:      "user_it",
		Status:      model.TaskStatusFailed,
		ActiveRunID: "run_it_failed",
		Prompt:      "failed task",
		CreatedAt:   time.Now().UTC().Add(2 * time.Second),
		UpdatedAt:   time.Now().UTC().Add(2 * time.Second),
	}
	if err := store.PutTask(ctx, sameTenantFailedTask); err != nil {
		t.Fatalf("put same-tenant failed task: %v", err)
	}
	if err := store.PutRun(ctx, &model.Run{
		TaskID:   sameTenantFailedTask.TaskID,
		RunID:    "run_it_failed",
		TenantID: sameTenantFailedTask.TenantID,
		Status:   model.RunStatusFailed,
	}); err != nil {
		t.Fatalf("put same-tenant failed run: %v", err)
	}

	gotTask, err := store.GetTask(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if gotTask.TaskID != task.TaskID || gotTask.TenantID != task.TenantID {
		t.Fatalf("unexpected task payload: %+v", gotTask)
	}

	queuedTasks, err := store.ListTasks(ctx, task.TenantID, []model.TaskStatus{model.TaskStatusQueued}, 10)
	if err != nil {
		t.Fatalf("list queued tasks for tenant: %v", err)
	}
	waitForDynamoIntegrationCondition(t, 5*time.Second, 100*time.Millisecond, func() (bool, error) {
		queuedTasks, err = store.ListTasks(ctx, task.TenantID, []model.TaskStatus{model.TaskStatusQueued}, 10)
		if err != nil {
			return false, err
		}
		return len(queuedTasks) == 1 && queuedTasks[0].TaskID == task.TaskID, nil
	}, "tenant queued task list did not converge")
	waitForDynamoIntegrationCondition(t, 5*time.Second, 100*time.Millisecond, func() (bool, error) {
		allQueuedTasks, err := store.ListTasks(ctx, "", []model.TaskStatus{model.TaskStatusQueued}, 10)
		if err != nil {
			return false, err
		}
		return len(allQueuedTasks) == 2, nil
	}, "global queued task list did not converge")

	queuedRuns, err := store.ListRuns(ctx, task.TenantID, []model.RunStatus{model.RunStatusQueued}, 10)
	if err != nil {
		t.Fatalf("list queued runs for tenant: %v", err)
	}
	waitForDynamoIntegrationCondition(t, 5*time.Second, 100*time.Millisecond, func() (bool, error) {
		queuedRuns, err = store.ListRuns(ctx, task.TenantID, []model.RunStatus{model.RunStatusQueued}, 10)
		if err != nil {
			return false, err
		}
		return len(queuedRuns) == 1 && queuedRuns[0].RunID == run.RunID, nil
	}, "tenant queued run list did not converge")
	waitForDynamoIntegrationCondition(t, 5*time.Second, 100*time.Millisecond, func() (bool, error) {
		allQueuedRuns, err := store.ListRuns(ctx, "", []model.RunStatus{model.RunStatusQueued}, 10)
		if err != nil {
			return false, err
		}
		return len(allQueuedRuns) == 2, nil
	}, "global queued run list did not converge")

	conn := &model.Connection{
		ConnectionID: "conn_it_1",
		TenantID:     task.TenantID,
		TaskID:       task.TaskID,
		RunID:        run.RunID,
		ConnectedAt:  time.Now().UTC(),
		TTL:          time.Now().Add(5 * time.Minute).Unix(),
	}
	if err := store.PutConnection(ctx, conn); err != nil {
		t.Fatalf("put connection: %v", err)
	}
	conns, err := store.GetConnectionsByTask(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("get connections by task: %v", err)
	}
	if len(conns) != 1 || conns[0].ConnectionID != conn.ConnectionID {
		t.Fatalf("unexpected connections: %+v", conns)
	}

	const writers = 24
	usagePerWrite := &model.TokenUsage{Input: 2, Output: 3, Total: 5}
	costPerWrite := 0.0015

	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.AddRunUsage(ctx, task.TaskID, run.RunID, usagePerWrite, costPerWrite); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("add run usage: %v", err)
		}
	}

	gotRun, err := store.GetRun(ctx, task.TaskID, run.RunID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if gotRun.TotalTokenUsage == nil {
		t.Fatal("expected total_token_usage to be set")
	}
	wantInput := writers * usagePerWrite.Input
	wantOutput := writers * usagePerWrite.Output
	wantTotal := writers * usagePerWrite.Total
	if gotRun.TotalTokenUsage.Input != wantInput ||
		gotRun.TotalTokenUsage.Output != wantOutput ||
		gotRun.TotalTokenUsage.Total != wantTotal {
		t.Fatalf("unexpected token usage: got=%+v want={input:%d output:%d total:%d}", gotRun.TotalTokenUsage, wantInput, wantOutput, wantTotal)
	}
	wantCost := float64(writers) * costPerWrite
	if diff := gotRun.TotalCostUSD - wantCost; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("unexpected total_cost_usd: got=%f want=%f", gotRun.TotalCostUSD, wantCost)
	}
}

func TestDynamoStoreIntegration_ClaimResetAndResumeTransition(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("AGENTFORGE_INTEGRATION_AWS_ENDPOINT"))
	if endpoint == "" {
		t.Skip("set AGENTFORGE_INTEGRATION_AWS_ENDPOINT to run DynamoDB integration tests")
	}

	ctx := context.Background()
	client := integrationDynamoClient(t, endpoint)

	cfg := createIntegrationStateTables(t, ctx, client, fmt.Sprintf("af-it-claim-%d", time.Now().UnixNano()))
	store, err := NewDynamoStore(client, cfg)
	if err != nil {
		t.Fatalf("new dynamo store: %v", err)
	}

	now := time.Now().UTC()
	task := &model.Task{
		TaskID:      "task_claim_1",
		TenantID:    "tenant_claim",
		UserID:      "user_claim",
		Status:      model.TaskStatusQueued,
		ActiveRunID: "run_claim_1",
		Prompt:      "claim/reset integration",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	run1 := &model.Run{
		TaskID:   task.TaskID,
		RunID:    "run_claim_1",
		TenantID: task.TenantID,
		Status:   model.RunStatusQueued,
	}
	if err := store.ApplyCreateTransition(ctx, task, run1); err != nil {
		t.Fatalf("apply create transition: %v", err)
	}

	if err := store.ClaimRun(ctx, task.TaskID, run1.RunID); err != nil {
		t.Fatalf("claim run: %v", err)
	}
	if err := store.ClaimRun(ctx, task.TaskID, run1.RunID); !errors.Is(err, ErrConflict) {
		t.Fatalf("second claim expected ErrConflict, got %v", err)
	}
	claimedRun, err := store.GetRun(ctx, task.TaskID, run1.RunID)
	if err != nil {
		t.Fatalf("get claimed run: %v", err)
	}
	if claimedRun.Status != model.RunStatusRunning {
		t.Fatalf("expected run status RUNNING after claim, got %s", claimedRun.Status)
	}
	if claimedRun.StartedAt == nil {
		t.Fatal("expected started_at to be set after claim")
	}

	if err := store.ResetRunToQueued(ctx, task.TaskID, run1.RunID); err != nil {
		t.Fatalf("reset run to queued: %v", err)
	}
	resetRun, err := store.GetRun(ctx, task.TaskID, run1.RunID)
	if err != nil {
		t.Fatalf("get reset run: %v", err)
	}
	if resetRun.Status != model.RunStatusQueued {
		t.Fatalf("expected run status RUN_QUEUED after reset, got %s", resetRun.Status)
	}
	if resetRun.QueuedAt == nil {
		t.Fatal("expected queued_at to be set after reset")
	}
	if resetRun.StartedAt != nil {
		t.Fatal("expected started_at to be cleared after reset")
	}
	if resetRun.EndedAt != nil {
		t.Fatal("expected ended_at to be cleared after reset")
	}

	if err := store.UpdateTaskStatus(ctx, task.TaskID, []model.TaskStatus{model.TaskStatusQueued}, model.TaskStatusFailed); err != nil {
		t.Fatalf("update task status to failed: %v", err)
	}
	if err := store.SetAbortRequested(ctx, task.TaskID, "resume test"); err != nil {
		t.Fatalf("set abort requested: %v", err)
	}

	run2 := &model.Run{
		TaskID:      task.TaskID,
		RunID:       "run_claim_2",
		TenantID:    task.TenantID,
		Status:      model.RunStatusQueued,
		ParentRunID: run1.RunID,
	}
	if err := store.ApplyResumeTransition(ctx, task.TaskID, run2, []model.TaskStatus{
		model.TaskStatusQueued,
		model.TaskStatusSucceeded,
		model.TaskStatusFailed,
		model.TaskStatusAborted,
	}, model.TaskStatusQueued); err != nil {
		t.Fatalf("apply resume transition: %v", err)
	}

	gotTask, err := store.GetTask(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("get task after resume transition: %v", err)
	}
	if gotTask.ActiveRunID != run2.RunID {
		t.Fatalf("expected active_run_id=%s, got %s", run2.RunID, gotTask.ActiveRunID)
	}
	if gotTask.Status != model.TaskStatusQueued {
		t.Fatalf("expected task status QUEUED after resume transition, got %s", gotTask.Status)
	}
	if gotTask.AbortRequested {
		t.Fatal("expected abort_requested to be cleared by resume transition")
	}
	if gotTask.AbortReason != "" {
		t.Fatalf("expected abort_reason to be cleared, got %q", gotTask.AbortReason)
	}
	if gotTask.AbortTS != nil {
		t.Fatal("expected abort_ts to be removed by resume transition")
	}
}

func TestDynamoStoreIntegration_TransactionalAndConditionalSemantics(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("AGENTFORGE_INTEGRATION_AWS_ENDPOINT"))
	if endpoint == "" {
		t.Skip("set AGENTFORGE_INTEGRATION_AWS_ENDPOINT to run DynamoDB integration tests")
	}

	ctx := context.Background()
	client := integrationDynamoClient(t, endpoint)

	cfg := createIntegrationStateTables(t, ctx, client, fmt.Sprintf("af-it-txn-%d", time.Now().UnixNano()))
	store, err := NewDynamoStore(client, cfg)
	if err != nil {
		t.Fatalf("new dynamo store: %v", err)
	}

	now := time.Now().UTC()
	task1 := &model.Task{
		TaskID:         "task_txn_1",
		TenantID:       "tenant_txn",
		UserID:         "user_txn",
		Status:         model.TaskStatusQueued,
		ActiveRunID:    "run_txn_1",
		Prompt:         "txn integration",
		IdempotencyKey: "idem_txn_1",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	run1 := &model.Run{
		TaskID:   task1.TaskID,
		RunID:    "run_txn_1",
		TenantID: task1.TenantID,
		Status:   model.RunStatusQueued,
	}
	if err := store.ApplyCreateTransition(ctx, task1, run1); err != nil {
		t.Fatalf("seed apply create transition: %v", err)
	}

	gotByIdem, err := store.GetTaskByIdempotencyKey(ctx, task1.TenantID, task1.IdempotencyKey)
	if err != nil {
		t.Fatalf("get task by idempotency key: %v", err)
	}
	if gotByIdem.TaskID != task1.TaskID {
		t.Fatalf("unexpected idempotency task mapping: got %s want %s", gotByIdem.TaskID, task1.TaskID)
	}

	task2 := &model.Task{
		TaskID:         "task_txn_2",
		TenantID:       task1.TenantID,
		UserID:         task1.UserID,
		Status:         model.TaskStatusQueued,
		ActiveRunID:    "run_txn_2",
		Prompt:         "txn conflict integration",
		IdempotencyKey: task1.IdempotencyKey,
		CreatedAt:      now.Add(time.Second),
		UpdatedAt:      now.Add(time.Second),
	}
	run2 := &model.Run{
		TaskID:   task2.TaskID,
		RunID:    "run_txn_2",
		TenantID: task2.TenantID,
		Status:   model.RunStatusQueued,
	}
	if err := store.ApplyCreateTransition(ctx, task2, run2); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("conflicting apply create transition expected ErrAlreadyExists, got %v", err)
	}
	if _, err := store.GetTask(ctx, task2.TaskID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("transaction should rollback task insert, got %v", err)
	}
	if _, err := store.GetRun(ctx, task2.TaskID, run2.RunID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("transaction should rollback run insert, got %v", err)
	}

	if err := store.UpdateTaskStatus(ctx, task1.TaskID, []model.TaskStatus{model.TaskStatusFailed}, model.TaskStatusSucceeded); !errors.Is(err, ErrConflict) {
		t.Fatalf("unexpected update task status result for non-matching from-state: %v", err)
	}

	conflictResumeRun := &model.Run{
		TaskID:      task1.TaskID,
		RunID:       "run_txn_resume_conflict",
		TenantID:    task1.TenantID,
		Status:      model.RunStatusQueued,
		ParentRunID: run1.RunID,
	}
	if err := store.ApplyResumeTransition(ctx, task1.TaskID, conflictResumeRun, []model.TaskStatus{model.TaskStatusFailed}, model.TaskStatusQueued); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting apply resume transition expected ErrConflict, got %v", err)
	}
	if _, err := store.GetRun(ctx, task1.TaskID, conflictResumeRun.RunID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resume conflict should not create run row, got %v", err)
	}

	if err := store.AddRunUsage(ctx, task1.TaskID, "run_missing", &model.TokenUsage{Input: 1, Output: 1, Total: 2}, 0.01); !errors.Is(err, ErrNotFound) {
		t.Fatalf("add run usage on missing run expected ErrNotFound, got %v", err)
	}
}

func TestDynamoStoreIntegration_CompactEvents(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("AGENTFORGE_INTEGRATION_AWS_ENDPOINT"))
	if endpoint == "" {
		t.Skip("set AGENTFORGE_INTEGRATION_AWS_ENDPOINT to run DynamoDB integration tests")
	}

	ctx := context.Background()
	client := integrationDynamoClient(t, endpoint)

	cfg := createIntegrationStateTables(t, ctx, client, fmt.Sprintf("af-it-compact-%d", time.Now().UnixNano()))
	store, err := NewDynamoStore(client, cfg)
	if err != nil {
		t.Fatalf("new dynamo store: %v", err)
	}

	now := time.Now().UTC()
	task := &model.Task{
		TaskID:      "task_compact_1",
		TenantID:    "tenant_compact",
		UserID:      "user_compact",
		Status:      model.TaskStatusQueued,
		ActiveRunID: "run_compact_1",
		Prompt:      "compact integration",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	run := &model.Run{
		TaskID:   task.TaskID,
		RunID:    "run_compact_1",
		TenantID: task.TenantID,
		Status:   model.RunStatusQueued,
	}
	if err := store.ApplyCreateTransition(ctx, task, run); err != nil {
		t.Fatalf("apply create transition: %v", err)
	}

	events := []*model.StreamEvent{
		{TaskID: task.TaskID, RunID: run.RunID, Seq: 1, TS: 100, Type: model.StreamEventStepStart},
		{TaskID: task.TaskID, RunID: run.RunID, Seq: 2, TS: 200, Type: model.StreamEventStepEnd},
		{TaskID: task.TaskID, RunID: run.RunID, Seq: 3, TS: 300, Type: model.StreamEventComplete},
	}
	for _, ev := range events {
		if err := store.PutEvent(ctx, ev); err != nil {
			t.Fatalf("put event seq=%d: %v", ev.Seq, err)
		}
	}

	waitForDynamoIntegrationCondition(t, 5*time.Second, 100*time.Millisecond, func() (bool, error) {
		replayed, err := store.ReplayEvents(ctx, task.TaskID, run.RunID, 0, 0, 10)
		if err != nil {
			return false, err
		}
		return len(replayed) == 3, nil
	}, "event replay did not converge before compaction")

	removed, err := store.CompactEvents(ctx, task.TaskID, run.RunID, 300)
	if err != nil {
		t.Fatalf("compact events: %v", err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 events removed by compaction, got %d", removed)
	}

	waitForDynamoIntegrationCondition(t, 5*time.Second, 100*time.Millisecond, func() (bool, error) {
		replayed, err := store.ReplayEvents(ctx, task.TaskID, run.RunID, 0, 0, 10)
		if err != nil {
			return false, err
		}
		return len(replayed) == 1 && replayed[0].Seq == 3 && replayed[0].TS == 300, nil
	}, "event replay did not converge after compaction")
}

func TestDynamoStoreIntegration_EventIsolationByTaskRunKey(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("AGENTFORGE_INTEGRATION_AWS_ENDPOINT"))
	if endpoint == "" {
		t.Skip("set AGENTFORGE_INTEGRATION_AWS_ENDPOINT to run DynamoDB integration tests")
	}

	ctx := context.Background()
	client := integrationDynamoClient(t, endpoint)

	cfg := createIntegrationStateTables(t, ctx, client, fmt.Sprintf("af-it-evt-iso-%d", time.Now().UnixNano()))
	store, err := NewDynamoStore(client, cfg)
	if err != nil {
		t.Fatalf("new dynamo store: %v", err)
	}

	now := time.Now().UTC()
	taskA := &model.Task{
		TaskID:      "task_evt_iso_a",
		TenantID:    "tenant_evt_iso_a",
		UserID:      "user_evt_iso",
		Status:      model.TaskStatusQueued,
		ActiveRunID: "run_evt_shared",
		Prompt:      "event isolation A",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	taskB := &model.Task{
		TaskID:      "task_evt_iso_b",
		TenantID:    "tenant_evt_iso_b",
		UserID:      "user_evt_iso",
		Status:      model.TaskStatusQueued,
		ActiveRunID: "run_evt_shared",
		Prompt:      "event isolation B",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	runA := &model.Run{
		TaskID:   taskA.TaskID,
		RunID:    "run_evt_shared",
		TenantID: taskA.TenantID,
		Status:   model.RunStatusQueued,
	}
	runB := &model.Run{
		TaskID:   taskB.TaskID,
		RunID:    "run_evt_shared",
		TenantID: taskB.TenantID,
		Status:   model.RunStatusQueued,
	}
	if err := store.ApplyCreateTransition(ctx, taskA, runA); err != nil {
		t.Fatalf("apply create transition task A: %v", err)
	}
	if err := store.ApplyCreateTransition(ctx, taskB, runB); err != nil {
		t.Fatalf("apply create transition task B: %v", err)
	}

	evA := &model.StreamEvent{
		TaskID: taskA.TaskID,
		RunID:  runA.RunID,
		Seq:    1,
		TS:     100,
		Type:   model.StreamEventStepEnd,
	}
	evB := &model.StreamEvent{
		TaskID: taskB.TaskID,
		RunID:  runB.RunID,
		Seq:    1, // same run+seq as A but different task
		TS:     101,
		Type:   model.StreamEventStepEnd,
	}
	if err := store.PutEvent(ctx, evA); err != nil {
		t.Fatalf("put event A: %v", err)
	}
	if err := store.PutEvent(ctx, evB); err != nil {
		t.Fatalf("put event B: %v", err)
	}

	waitForDynamoIntegrationCondition(t, 5*time.Second, 100*time.Millisecond, func() (bool, error) {
		gotA, err := store.ReplayEvents(ctx, taskA.TaskID, runA.RunID, 0, 0, 10)
		if err != nil {
			return false, err
		}
		gotB, err := store.ReplayEvents(ctx, taskB.TaskID, runB.RunID, 0, 0, 10)
		if err != nil {
			return false, err
		}
		return len(gotA) == 1 && len(gotB) == 1 && gotA[0].TaskID == taskA.TaskID && gotB[0].TaskID == taskB.TaskID, nil
	}, "event replay isolation by task+run did not converge")

	removed, err := store.CompactEvents(ctx, taskA.TaskID, runA.RunID, 200)
	if err != nil {
		t.Fatalf("compact events task A: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected one removed event for task A, got %d", removed)
	}

	waitForDynamoIntegrationCondition(t, 5*time.Second, 100*time.Millisecond, func() (bool, error) {
		gotA, err := store.ReplayEvents(ctx, taskA.TaskID, runA.RunID, 0, 0, 10)
		if err != nil {
			return false, err
		}
		gotB, err := store.ReplayEvents(ctx, taskB.TaskID, runB.RunID, 0, 0, 10)
		if err != nil {
			return false, err
		}
		return len(gotA) == 0 && len(gotB) == 1 && gotB[0].TaskID == taskB.TaskID, nil
	}, "compaction should not affect different task sharing run_id")
}

func integrationDynamoClient(t *testing.T, endpoint string) *dynamodb.Client {
	t.Helper()
	cfg, err := awscfg.LoadDefaultConfig(
		context.Background(),
		awscfg.WithRegion("us-east-1"),
		awscfg.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}
	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = &endpoint
	})
}

func createIntegrationStateTables(t *testing.T, ctx context.Context, client *dynamodb.Client, prefix string) DynamoStoreConfig {
	t.Helper()
	cfg := DynamoStoreConfig{
		TasksTable:       prefix + "-tasks",
		RunsTable:        prefix + "-runs",
		StepsTable:       prefix + "-steps",
		ConnectionsTable: prefix + "-connections",
		ConnectionIndex:  "task-index",
		TaskTenantIndex:  defaultTaskTenantIndex,
		RunTenantIndex:   defaultRunTenantIndex,
		TaskEntityIndex:  defaultTaskEntityIndex,
		RunEntityIndex:   defaultRunEntityIndex,
	}
	createKVTableWithTenantAndEntityIndexes(t, ctx, client, cfg.TasksTable, cfg.TaskTenantIndex, cfg.TaskEntityIndex)
	createKVTableWithTenantAndEntityIndexes(t, ctx, client, cfg.RunsTable, cfg.RunTenantIndex, cfg.RunEntityIndex)
	createKVTable(t, ctx, client, cfg.StepsTable)
	createConnectionsTableWithTaskIndex(t, ctx, client, cfg.ConnectionsTable, cfg.ConnectionIndex)
	return cfg
}

func waitForDynamoIntegrationCondition(
	t *testing.T,
	timeout time.Duration,
	interval time.Duration,
	check func() (bool, error),
	msg string,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		ok, err := check()
		if err != nil {
			lastErr = err
		} else if ok {
			return
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				t.Fatalf("%s: %v", msg, lastErr)
			}
			t.Fatalf("%s", msg)
		}
		time.Sleep(interval)
	}
}

func createKVTable(t *testing.T, ctx context.Context, client *dynamodb.Client, tableName string) {
	t.Helper()
	_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		AttributeDefinitions: []dbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: dbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: dbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []dbtypes.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: dbtypes.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: dbtypes.KeyTypeRange},
		},
		BillingMode: dbtypes.BillingModePayPerRequest,
	})
	if err != nil {
		t.Fatalf("create table %s: %v", tableName, err)
	}
	waiter := dynamodb.NewTableExistsWaiter(client)
	if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(tableName)}, 2*time.Minute); err != nil {
		t.Fatalf("wait for table %s: %v", tableName, err)
	}
}

func createKVTableWithTenantAndEntityIndexes(t *testing.T, ctx context.Context, client *dynamodb.Client, tableName, tenantIndexName, entityIndexName string) {
	t.Helper()
	_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		AttributeDefinitions: []dbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: dbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: dbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsi1pk"), AttributeType: dbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsi1sk"), AttributeType: dbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsi2pk"), AttributeType: dbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsi2sk"), AttributeType: dbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []dbtypes.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: dbtypes.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: dbtypes.KeyTypeRange},
		},
		GlobalSecondaryIndexes: []dbtypes.GlobalSecondaryIndex{
			{
				IndexName: aws.String(tenantIndexName),
				KeySchema: []dbtypes.KeySchemaElement{
					{AttributeName: aws.String("gsi1pk"), KeyType: dbtypes.KeyTypeHash},
					{AttributeName: aws.String("gsi1sk"), KeyType: dbtypes.KeyTypeRange},
				},
				Projection: &dbtypes.Projection{ProjectionType: dbtypes.ProjectionTypeAll},
			},
			{
				IndexName: aws.String(entityIndexName),
				KeySchema: []dbtypes.KeySchemaElement{
					{AttributeName: aws.String("gsi2pk"), KeyType: dbtypes.KeyTypeHash},
					{AttributeName: aws.String("gsi2sk"), KeyType: dbtypes.KeyTypeRange},
				},
				Projection: &dbtypes.Projection{ProjectionType: dbtypes.ProjectionTypeAll},
			},
		},
		BillingMode: dbtypes.BillingModePayPerRequest,
	})
	if err != nil {
		t.Fatalf("create table %s with tenant/entity indexes: %v", tableName, err)
	}
	waiter := dynamodb.NewTableExistsWaiter(client)
	if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(tableName)}, 2*time.Minute); err != nil {
		t.Fatalf("wait for table %s: %v", tableName, err)
	}
	waitForDynamoIntegrationGSIActive(t, ctx, client, tableName, tenantIndexName, 2*time.Minute)
	waitForDynamoIntegrationGSIActive(t, ctx, client, tableName, entityIndexName, 2*time.Minute)
}

func createConnectionsTableWithTaskIndex(t *testing.T, ctx context.Context, client *dynamodb.Client, tableName, indexName string) {
	t.Helper()
	_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		AttributeDefinitions: []dbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: dbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: dbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsi1pk"), AttributeType: dbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsi1sk"), AttributeType: dbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []dbtypes.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: dbtypes.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: dbtypes.KeyTypeRange},
		},
		GlobalSecondaryIndexes: []dbtypes.GlobalSecondaryIndex{
			{
				IndexName: aws.String(indexName),
				KeySchema: []dbtypes.KeySchemaElement{
					{AttributeName: aws.String("gsi1pk"), KeyType: dbtypes.KeyTypeHash},
					{AttributeName: aws.String("gsi1sk"), KeyType: dbtypes.KeyTypeRange},
				},
				Projection: &dbtypes.Projection{ProjectionType: dbtypes.ProjectionTypeAll},
			},
		},
		BillingMode: dbtypes.BillingModePayPerRequest,
	})
	if err != nil {
		t.Fatalf("create table %s: %v", tableName, err)
	}
	waiter := dynamodb.NewTableExistsWaiter(client)
	if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(tableName)}, 2*time.Minute); err != nil {
		t.Fatalf("wait for table %s: %v", tableName, err)
	}
	waitForDynamoIntegrationGSIActive(t, ctx, client, tableName, indexName, 2*time.Minute)
}

func waitForDynamoIntegrationGSIActive(
	t *testing.T,
	ctx context.Context,
	client *dynamodb.Client,
	tableName string,
	indexName string,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		out, err := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
			TableName: aws.String(tableName),
		})
		if err != nil {
			if time.Now().After(deadline) {
				t.Fatalf("describe table %s: %v", tableName, err)
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if out.Table == nil {
			if time.Now().After(deadline) {
				t.Fatalf("describe table %s returned nil table", tableName)
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}
		for _, gsi := range out.Table.GlobalSecondaryIndexes {
			if aws.ToString(gsi.IndexName) == indexName && gsi.IndexStatus == dbtypes.IndexStatusActive {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("gsi %s on table %s did not become ACTIVE", indexName, tableName)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
