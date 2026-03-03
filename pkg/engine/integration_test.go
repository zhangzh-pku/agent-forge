package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/api"
	artstore "github.com/agentforge/agentforge/pkg/artifact"
	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/queue"
	"github.com/agentforge/agentforge/pkg/state"
	"github.com/agentforge/agentforge/pkg/stream"
	"github.com/agentforge/agentforge/pkg/task"
)

func waitForTaskStatus(t *testing.T, store *state.MemoryStore, taskID string, want model.TaskStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		taskObj, err := store.GetTask(context.Background(), taskID)
		if err == nil && taskObj.Status == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	taskObj, err := store.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("task lookup failed while waiting for status %s: %v", want, err)
	}
	t.Fatalf("timeout waiting for task status %s, got %s", want, taskObj.Status)
}

// TestFullLoop tests the complete POST → queue → worker → GET steps flow.
func TestFullLoop(t *testing.T) {
	store := state.NewMemoryStore()
	artifacts := artstore.NewMemoryStore()
	q := queue.NewMemoryQueue(100)
	pusher := stream.NewMockPusher()
	llm := NewMockLLMClient(2)

	// Set up API.
	svc := task.NewService(store, q)
	handler := api.NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Step 1: POST /tasks.
	body, _ := json.Marshal(map[string]string{"prompt": "create a hello world file"})
	req := httptest.NewRequest("POST", "/tasks", bytes.NewReader(body))
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("POST /tasks: expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var createResp struct {
		TaskID string `json:"task_id"`
		RunID  string `json:"run_id"`
	}
	json.NewDecoder(rr.Body).Decode(&createResp)
	t.Logf("Created task=%s run=%s", createResp.TaskID, createResp.RunID)

	// Step 2: Consume from queue (simulating worker).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	workerDone := make(chan error, 1)
	worker := NewWorker(store, artifacts, q, llm, NewRegistry(), pusher, DefaultEngineConfig())

	go func() {
		// Process one message then stop.
		err := q.StartConsumer(ctx, worker.handleMessage)
		workerDone <- err
	}()

	waitForTaskStatus(t, store, createResp.TaskID, model.TaskStatusSucceeded, 3*time.Second)
	cancel()
	<-workerDone

	// Step 3: Verify task status.
	taskObj, err := store.GetTask(context.Background(), createResp.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if taskObj.Status != model.TaskStatusSucceeded {
		t.Fatalf("expected task SUCCEEDED, got %s", taskObj.Status)
	}

	// Step 4: GET steps via API.
	stepsPath := "/tasks/" + createResp.TaskID + "/runs/" + createResp.RunID + "/steps?from=0&limit=200"
	req = httptest.NewRequest("GET", stepsPath, nil)
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET steps: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var stepsResp struct {
		Steps []map[string]interface{} `json:"steps"`
	}
	json.NewDecoder(rr.Body).Decode(&stepsResp)

	if len(stepsResp.Steps) < 3 {
		t.Fatalf("expected at least 3 steps, got %d", len(stepsResp.Steps))
	}
	t.Logf("Got %d steps", len(stepsResp.Steps))

	// Verify last step is final.
	lastStep := stepsResp.Steps[len(stepsResp.Steps)-1]
	if lastStep["type"] != "final" {
		t.Fatalf("last step type: expected 'final', got %q", lastStep["type"])
	}
}

func TestWorkerSkipsStaleNonActiveRun(t *testing.T) {
	store := state.NewMemoryStore()
	artifacts := artstore.NewMemoryStore()
	q := queue.NewMemoryQueue(10)
	pusher := stream.NewMockPusher()
	llm := NewMockLLMClient(1)

	now := time.Now().UTC()
	taskObj := &model.Task{
		TaskID:      "task_stale",
		TenantID:    "tnt_1",
		UserID:      "user_1",
		Status:      model.TaskStatusQueued,
		ActiveRunID: "run_new",
		Prompt:      "stale run should be skipped",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.PutTask(context.Background(), taskObj); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRun(context.Background(), &model.Run{
		TaskID:   "task_stale",
		RunID:    "run_old",
		TenantID: "tnt_1",
		Status:   model.RunStatusQueued,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRun(context.Background(), &model.Run{
		TaskID:   "task_stale",
		RunID:    "run_new",
		TenantID: "tnt_1",
		Status:   model.RunStatusQueued,
	}); err != nil {
		t.Fatal(err)
	}

	worker := NewWorker(store, artifacts, q, llm, NewRegistry(), pusher, DefaultEngineConfig())
	msg := &model.SQSMessage{
		TenantID:    "tnt_1",
		TaskID:      "task_stale",
		RunID:       "run_old",
		SubmittedAt: now.Unix(),
		Attempt:     1,
	}
	if err := worker.handleMessage(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	runOld, err := store.GetRun(context.Background(), "task_stale", "run_old")
	if err != nil {
		t.Fatal(err)
	}
	if runOld.Status != model.RunStatusAborted {
		t.Fatalf("expected stale run to be ABORTED, got %s", runOld.Status)
	}

	taskAfter, err := store.GetTask(context.Background(), "task_stale")
	if err != nil {
		t.Fatal(err)
	}
	if taskAfter.ActiveRunID != "run_new" {
		t.Fatalf("expected active run to stay run_new, got %s", taskAfter.ActiveRunID)
	}
	if taskAfter.Status != model.TaskStatusQueued {
		t.Fatalf("expected task status to remain QUEUED, got %s", taskAfter.Status)
	}

	steps, err := store.ListSteps(context.Background(), "run_old", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 0 {
		t.Fatalf("expected no steps for stale run, got %d", len(steps))
	}
}

// TestFullLoopWithAbort tests POST → abort → worker detects abort.
func TestFullLoopWithAbort(t *testing.T) {
	store := state.NewMemoryStore()
	artifacts := artstore.NewMemoryStore()
	q := queue.NewMemoryQueue(100)
	pusher := stream.NewMockPusher()
	llm := NewMockLLMClient(10) // Many steps, but abort will stop it.

	svc := task.NewService(store, q)
	handler := api.NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Create task.
	body, _ := json.Marshal(map[string]string{"prompt": "long task"})
	req := httptest.NewRequest("POST", "/tasks", bytes.NewReader(body))
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	var createResp struct {
		TaskID string `json:"task_id"`
		RunID  string `json:"run_id"`
	}
	json.NewDecoder(rr.Body).Decode(&createResp)

	// Abort before worker processes.
	abortBody, _ := json.Marshal(map[string]string{"reason": "testing abort"})
	req = httptest.NewRequest("POST", "/tasks/"+createResp.TaskID+"/abort", bytes.NewReader(abortBody))
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("abort: expected 200, got %d", rr.Code)
	}

	// Now run worker.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	worker := NewWorker(store, artifacts, q, llm, NewRegistry(), pusher, DefaultEngineConfig())
	go q.StartConsumer(ctx, worker.handleMessage)
	waitForTaskStatus(t, store, createResp.TaskID, model.TaskStatusAborted, 3*time.Second)
	cancel()

	// Verify aborted.
	taskObj, _ := store.GetTask(context.Background(), createResp.TaskID)
	if taskObj.Status != model.TaskStatusAborted {
		t.Fatalf("expected ABORTED, got %s", taskObj.Status)
	}
}

// TestFullLoopWithResume tests POST → run → resume → verify restored.
func TestFullLoopWithResume(t *testing.T) {
	store := state.NewMemoryStore()
	artifacts := artstore.NewMemoryStore()
	q := queue.NewMemoryQueue(100)
	pusher := stream.NewMockPusher()

	svc := task.NewService(store, q)
	handler := api.NewHandler(svc)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Create task.
	body, _ := json.Marshal(map[string]string{"prompt": "create files"})
	req := httptest.NewRequest("POST", "/tasks", bytes.NewReader(body))
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	var createResp struct {
		TaskID string `json:"task_id"`
		RunID  string `json:"run_id"`
	}
	json.NewDecoder(rr.Body).Decode(&createResp)

	// Run worker for first execution.
	llm1 := NewMockLLMClient(2)
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	worker1 := NewWorker(store, artifacts, q, llm1, NewRegistry(), pusher, DefaultEngineConfig())
	go q.StartConsumer(ctx1, worker1.handleMessage)
	waitForTaskStatus(t, store, createResp.TaskID, model.TaskStatusSucceeded, 3*time.Second)
	cancel1()

	// Verify first run completed.
	taskObj, _ := store.GetTask(context.Background(), createResp.TaskID)
	if taskObj.Status != model.TaskStatusSucceeded {
		t.Fatalf("first run: expected SUCCEEDED, got %s", taskObj.Status)
	}

	// Resume from step 1.
	resumeBody, _ := json.Marshal(map[string]interface{}{
		"from_run_id":     createResp.RunID,
		"from_step_index": 1,
	})
	req = httptest.NewRequest("POST", "/tasks/"+createResp.TaskID+"/resume", bytes.NewReader(resumeBody))
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resumeResp struct {
		TaskID string `json:"task_id"`
		RunID  string `json:"run_id"`
	}
	json.NewDecoder(rr.Body).Decode(&resumeResp)
	t.Logf("Resume: new run=%s", resumeResp.RunID)

	// Run worker for resumed execution.
	llm2 := NewMockLLMClient(1)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	worker2 := NewWorker(store, artifacts, q, llm2, NewRegistry(), pusher, DefaultEngineConfig())
	go q.StartConsumer(ctx2, worker2.handleMessage)
	waitForTaskStatus(t, store, createResp.TaskID, model.TaskStatusSucceeded, 3*time.Second)
	cancel2()

	// Verify resumed run completed.
	taskObj, _ = store.GetTask(context.Background(), createResp.TaskID)
	if taskObj.Status != model.TaskStatusSucceeded {
		t.Fatalf("resume run: expected SUCCEEDED, got %s", taskObj.Status)
	}

	// Verify new run has steps.
	steps, err := store.ListSteps(context.Background(), resumeResp.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) < 2 {
		t.Fatalf("expected at least 2 steps in resume run, got %d", len(steps))
	}
	t.Logf("Resume run has %d steps", len(steps))
}

func TestDisconnectReconnectReplayRecovery(t *testing.T) {
	store := state.NewMemoryStore()
	artifacts := artstore.NewMemoryStore()
	q := queue.NewMemoryQueue(100)
	pusher := stream.NewMockPusher()
	llm := NewMockLLMClient(2)

	svc := task.NewService(store, q)
	handler := api.NewHandler(svc)
	wsHandler := api.NewWSHandler(store, svc)
	wsHandler.SetReplayPusher(pusher)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	wsHandler.RegisterRoutes(mux)

	// Create task.
	body, _ := json.Marshal(map[string]string{"prompt": "replay test"})
	req := httptest.NewRequest("POST", "/tasks", bytes.NewReader(body))
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", rr.Code)
	}
	var createResp struct {
		TaskID string `json:"task_id"`
		RunID  string `json:"run_id"`
	}
	_ = json.NewDecoder(rr.Body).Decode(&createResp)

	// Execute task with no active WS connection.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	worker := NewWorker(store, artifacts, q, llm, NewRegistry(), pusher, DefaultEngineConfig())
	go q.StartConsumer(ctx, worker.handleMessage)
	waitForTaskStatus(t, store, createResp.TaskID, model.TaskStatusSucceeded, 3*time.Second)
	cancel()

	// Reconnect from seq=0 should replay persisted events.
	connectBody, _ := json.Marshal(map[string]interface{}{
		"connection_id": "conn_replay",
		"task_id":       createResp.TaskID,
		"run_id":        createResp.RunID,
		"last_seq":      0,
	})
	req = httptest.NewRequest("POST", "/ws/connect", bytes.NewReader(connectBody))
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("ws connect: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	events := pusher.Events()
	if len(events) == 0 {
		t.Fatal("expected replayed events on reconnect")
	}
	foundComplete := false
	for _, ev := range events {
		if ev.ConnectionID == "conn_replay" && ev.Event.Type == model.StreamEventComplete {
			foundComplete = true
			break
		}
	}
	if !foundComplete {
		t.Fatal("expected replayed complete event for reconnecting client")
	}
}
