package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/queue"
	"github.com/agentforge/agentforge/pkg/state"
	"github.com/agentforge/agentforge/pkg/stream"
	"github.com/agentforge/agentforge/pkg/task"
)

func setupTestServer() (*http.ServeMux, *state.MemoryStore) {
	store := state.NewMemoryStore()
	q := queue.NewMemoryQueue(100)
	svc := task.NewService(store, q)
	handler := NewHandler(svc)
	handler.SetTenantRuntimeProvider(q)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return mux, store
}

func createStepsForResume(store *state.MemoryStore, runID string, count int) {
	ctx := context.Background()
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

func doRequest(mux http.Handler, method, path string, body interface{}) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestCreateTask(t *testing.T) {
	mux, _ := setupTestServer()

	rr := doRequest(mux, "POST", "/tasks", map[string]string{
		"prompt": "hello world",
	})

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp)

	if resp["task_id"] == "" {
		t.Fatal("expected task_id")
	}
	if resp["run_id"] == "" {
		t.Fatal("expected run_id")
	}
}

func TestCreateTaskIdempotency(t *testing.T) {
	mux, _ := setupTestServer()

	body := map[string]string{
		"prompt":          "hello",
		"idempotency_key": "idem_1",
	}

	rr1 := doRequest(mux, "POST", "/tasks", body)
	if rr1.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr1.Code)
	}
	var resp1 map[string]string
	json.NewDecoder(rr1.Body).Decode(&resp1)

	rr2 := doRequest(mux, "POST", "/tasks", body)
	if rr2.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr2.Code)
	}
	var resp2 map[string]string
	json.NewDecoder(rr2.Body).Decode(&resp2)

	if resp1["task_id"] != resp2["task_id"] {
		t.Fatalf("idempotency failed: %s != %s", resp1["task_id"], resp2["task_id"])
	}
}

func TestGetTask(t *testing.T) {
	mux, _ := setupTestServer()

	rr := doRequest(mux, "POST", "/tasks", map[string]string{"prompt": "test"})
	var createResp map[string]string
	json.NewDecoder(rr.Body).Decode(&createResp)

	rr = doRequest(mux, "GET", "/tasks/"+createResp["task_id"], nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetRun(t *testing.T) {
	mux, _ := setupTestServer()

	rr := doRequest(mux, "POST", "/tasks", map[string]string{"prompt": "test"})
	var createResp map[string]string
	json.NewDecoder(rr.Body).Decode(&createResp)

	rr = doRequest(mux, "GET", "/tasks/"+createResp["task_id"]+"/runs/"+createResp["run_id"], nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var runResp map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&runResp); err != nil {
		t.Fatal(err)
	}
	if runResp["run_id"] != createResp["run_id"] {
		t.Fatalf("expected run_id=%s, got %v", createResp["run_id"], runResp["run_id"])
	}
}

func TestReplayEvents(t *testing.T) {
	mux, store := setupTestServer()

	rr := doRequest(mux, "POST", "/tasks", map[string]string{"prompt": "test"})
	var createResp map[string]string
	json.NewDecoder(rr.Body).Decode(&createResp)

	_ = store.PutEvent(context.Background(), &model.StreamEvent{
		TaskID: createResp["task_id"],
		RunID:  createResp["run_id"],
		Seq:    1,
		TS:     time.Now().Unix(),
		Type:   model.StreamEventStepStart,
	})
	_ = store.PutEvent(context.Background(), &model.StreamEvent{
		TaskID: createResp["task_id"],
		RunID:  createResp["run_id"],
		Seq:    2,
		TS:     time.Now().Unix(),
		Type:   model.StreamEventStepEnd,
	})

	rr = doRequest(mux, "GET", "/tasks/"+createResp["task_id"]+"/runs/"+createResp["run_id"]+"/events/replay?from_seq=1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Events []model.StreamEvent `json:"events"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 1 || body.Events[0].Seq != 2 {
		t.Fatalf("expected replay seq 2, got %+v", body.Events)
	}

	compactBody := map[string]interface{}{"before_ts": time.Now().Add(time.Second).Unix()}
	rr = doRequest(mux, "POST", "/tasks/"+createResp["task_id"]+"/runs/"+createResp["run_id"]+"/events/compact", compactBody)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 on compact, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAbortTask(t *testing.T) {
	mux, _ := setupTestServer()

	rr := doRequest(mux, "POST", "/tasks", map[string]string{"prompt": "test"})
	var createResp map[string]string
	json.NewDecoder(rr.Body).Decode(&createResp)

	rr = doRequest(mux, "POST", "/tasks/"+createResp["task_id"]+"/abort", map[string]string{
		"reason": "user cancelled",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var taskResp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&taskResp)
	if taskResp["abort_requested"] != true {
		t.Fatal("expected abort_requested=true")
	}
}

func TestResumeTask(t *testing.T) {
	mux, store := setupTestServer()

	rr := doRequest(mux, "POST", "/tasks", map[string]string{"prompt": "test"})
	var createResp map[string]string
	json.NewDecoder(rr.Body).Decode(&createResp)

	// Create steps with checkpoints so resume validation passes.
	createStepsForResume(store, createResp["run_id"], 3)

	rr = doRequest(mux, "POST", "/tasks/"+createResp["task_id"]+"/resume", map[string]interface{}{
		"from_run_id":     createResp["run_id"],
		"from_step_index": 1,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resumeResp map[string]string
	json.NewDecoder(rr.Body).Decode(&resumeResp)
	if resumeResp["run_id"] == "" {
		t.Fatal("expected new run_id")
	}
	if resumeResp["run_id"] == createResp["run_id"] {
		t.Fatal("expected different run_id")
	}
}

func TestResumeTaskWithModelConfig(t *testing.T) {
	mux, store := setupTestServer()

	rr := doRequest(mux, "POST", "/tasks", map[string]string{"prompt": "test"})
	var createResp map[string]string
	json.NewDecoder(rr.Body).Decode(&createResp)

	// Create steps with checkpoints so resume validation passes.
	createStepsForResume(store, createResp["run_id"], 2)

	rr = doRequest(mux, "POST", "/tasks/"+createResp["task_id"]+"/resume", map[string]interface{}{
		"from_run_id":     createResp["run_id"],
		"from_step_index": 0,
		"model_config_override": map[string]interface{}{
			"model_id":    "claude-opus-4-6",
			"temperature": 0.7,
			"max_tokens":  4096,
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resumeResp map[string]string
	json.NewDecoder(rr.Body).Decode(&resumeResp)

	// Verify the new run has the model config override.
	run, err := store.GetRun(context.Background(), createResp["task_id"], resumeResp["run_id"])
	if err != nil {
		t.Fatal(err)
	}
	if run.ModelConfig == nil {
		t.Fatal("expected model_config on resumed run")
	}
	if run.ModelConfig.ModelID != "claude-opus-4-6" {
		t.Fatalf("expected model_id=claude-opus-4-6, got %s", run.ModelConfig.ModelID)
	}
	if run.ModelConfig.MaxTokens != 4096 {
		t.Fatalf("expected max_tokens=4096, got %d", run.ModelConfig.MaxTokens)
	}
}

func TestResumeTaskClearsAbortFlag(t *testing.T) {
	mux, store := setupTestServer()

	// Create and abort.
	rr := doRequest(mux, "POST", "/tasks", map[string]string{"prompt": "test"})
	var createResp map[string]string
	json.NewDecoder(rr.Body).Decode(&createResp)

	doRequest(mux, "POST", "/tasks/"+createResp["task_id"]+"/abort", map[string]string{
		"reason": "stop it",
	})

	// Verify abort is set.
	taskObj, _ := store.GetTask(context.Background(), createResp["task_id"])
	if !taskObj.AbortRequested {
		t.Fatal("expected abort_requested=true")
	}

	// Create steps with checkpoints so resume validation passes.
	createStepsForResume(store, createResp["run_id"], 2)

	// Resume should clear abort flag.
	doRequest(mux, "POST", "/tasks/"+createResp["task_id"]+"/resume", map[string]interface{}{
		"from_run_id":     createResp["run_id"],
		"from_step_index": 0,
	})

	taskObj, _ = store.GetTask(context.Background(), createResp["task_id"])
	if taskObj.AbortRequested {
		t.Fatal("expected abort_requested=false after resume")
	}
}

func TestCreateTaskWithModelConfig(t *testing.T) {
	mux, store := setupTestServer()

	rr := doRequest(mux, "POST", "/tasks", map[string]interface{}{
		"prompt": "test",
		"model_config": map[string]interface{}{
			"model_id":    "gpt-4",
			"temperature": 0.5,
			"max_tokens":  2048,
		},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var createResp map[string]string
	json.NewDecoder(rr.Body).Decode(&createResp)

	taskObj, err := store.GetTask(context.Background(), createResp["task_id"])
	if err != nil {
		t.Fatal(err)
	}
	if taskObj.ModelConfig == nil {
		t.Fatal("expected model_config on task")
	}
	if taskObj.ModelConfig.ModelID != "gpt-4" {
		t.Fatalf("expected model_id=gpt-4, got %s", taskObj.ModelConfig.ModelID)
	}
	if taskObj.ModelConfig.MaxTokens != 2048 {
		t.Fatalf("expected max_tokens=2048, got %d", taskObj.ModelConfig.MaxTokens)
	}
}

func TestCreateTaskRejectsInvalidModelConfig(t *testing.T) {
	mux, _ := setupTestServer()

	rr := doRequest(mux, "POST", "/tasks", map[string]interface{}{
		"prompt": "test",
		"model_config": map[string]interface{}{
			"model_id":    "unknown-model",
			"temperature": 0.5,
			"max_tokens":  2048,
		},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "unknown-model") {
		t.Fatalf("expected sanitized validation error, got %s", rr.Body.String())
	}
}

func TestCreateTaskEmptyPrompt(t *testing.T) {
	mux, _ := setupTestServer()

	rr := doRequest(mux, "POST", "/tasks", map[string]string{
		"prompt": "",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetTenantRuntime(t *testing.T) {
	mux, _ := setupTestServer()

	rr := doRequest(mux, "POST", "/tasks", map[string]string{
		"prompt": "hello runtime",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create task: expected 201, got %d", rr.Code)
	}

	rr = doRequest(mux, "GET", "/tenants/tnt_1/runtime", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("runtime: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var body map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["tenant_id"] != "tnt_1" {
		t.Fatalf("expected tenant_id=tnt_1, got %v", body["tenant_id"])
	}
}

func TestGetTenantAlerts(t *testing.T) {
	mux, _ := setupTestServer()

	rr := doRequest(mux, "GET", "/tenants/tnt_1/alerts?limit=10", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("alerts: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var body map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["alerts"]; !ok {
		t.Fatalf("expected alerts field, got %v", body)
	}
}

func TestGetTenantRuntimeWrongTenant(t *testing.T) {
	mux, _ := setupTestServer()

	req := httptest.NewRequest(http.MethodGet, "/tenants/tnt_2/runtime", nil)
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-tenant runtime access, got %d", rr.Code)
	}
}

func TestCreateTaskInvalidBody(t *testing.T) {
	mux, _ := setupTestServer()

	req := httptest.NewRequest("POST", "/tasks", bytes.NewReader([]byte("not json")))
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCreateTaskOversizedBody(t *testing.T) {
	mux, _ := setupTestServer()

	// Create a body larger than maxRequestBodySize (1MB).
	bigBody := make([]byte, maxRequestBodySize+1024)
	for i := range bigBody {
		bigBody[i] = 'a'
	}

	req := httptest.NewRequest("POST", "/tasks", bytes.NewReader(bigBody))
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetTaskNotFound(t *testing.T) {
	mux, _ := setupTestServer()

	rr := doRequest(mux, "GET", "/tasks/nonexistent", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGetTaskWrongTenant(t *testing.T) {
	mux, _ := setupTestServer()

	// Create a task as tnt_1.
	rr := doRequest(mux, "POST", "/tasks", map[string]string{"prompt": "test"})
	var createResp map[string]string
	json.NewDecoder(rr.Body).Decode(&createResp)

	// Try to get it as tnt_2.
	req := httptest.NewRequest("GET", "/tasks/"+createResp["task_id"], nil)
	req.Header.Set("X-Tenant-Id", "tnt_2")
	req.Header.Set("X-User-Id", "user_1")
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req)

	if rr2.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for wrong tenant, got %d", rr2.Code)
	}
}

func TestGetTaskWrongUser(t *testing.T) {
	mux, _ := setupTestServer()

	// Create a task as user_1.
	rr := doRequest(mux, "POST", "/tasks", map[string]string{"prompt": "test"})
	var createResp map[string]string
	json.NewDecoder(rr.Body).Decode(&createResp)

	// Try to get it as user_2 in the same tenant.
	req := httptest.NewRequest("GET", "/tasks/"+createResp["task_id"], nil)
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_2")
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req)

	if rr2.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for wrong user, got %d", rr2.Code)
	}
}

func TestListStepsWrongUser(t *testing.T) {
	mux, store := setupTestServer()
	ctx := context.Background()

	rr := doRequest(mux, "POST", "/tasks", map[string]string{"prompt": "test"})
	var createResp map[string]string
	json.NewDecoder(rr.Body).Decode(&createResp)

	// Seed one step for this run.
	store.PutStep(ctx, &model.Step{
		RunID:     createResp["run_id"],
		StepIndex: 0,
		Type:      model.StepTypeLLMCall,
		Status:    model.StepStatusOK,
		TSStart:   time.Now().UTC(),
		TSEnd:     time.Now().UTC(),
	})

	req := httptest.NewRequest("GET", "/tasks/"+createResp["task_id"]+"/runs/"+createResp["run_id"]+"/steps", nil)
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_2")
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req)

	if rr2.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for wrong user step listing, got %d: %s", rr2.Code, rr2.Body.String())
	}
}

func TestIdempotencyKeyViaHeader(t *testing.T) {
	mux, _ := setupTestServer()

	body, _ := json.Marshal(map[string]string{"prompt": "test"})

	// First request with idempotency key in header.
	req := httptest.NewRequest("POST", "/tasks", bytes.NewReader(body))
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	req.Header.Set("Idempotency-Key", "header_key_1")
	req.Header.Set("Content-Type", "application/json")
	rr1 := httptest.NewRecorder()
	mux.ServeHTTP(rr1, req)
	if rr1.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr1.Code)
	}
	var resp1 map[string]string
	json.NewDecoder(rr1.Body).Decode(&resp1)

	// Second request with same key.
	req = httptest.NewRequest("POST", "/tasks", bytes.NewReader(body))
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	req.Header.Set("Idempotency-Key", "header_key_1")
	req.Header.Set("Content-Type", "application/json")
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req)

	var resp2 map[string]string
	json.NewDecoder(rr2.Body).Decode(&resp2)

	if resp1["task_id"] != resp2["task_id"] {
		t.Fatalf("idempotency via header failed: %s != %s", resp1["task_id"], resp2["task_id"])
	}
}

func TestMethodNotAllowed(t *testing.T) {
	mux, _ := setupTestServer()

	rr := doRequest(mux, "DELETE", "/tasks", nil)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rr.Code)
	}
}

func TestResumeTaskEmptyFromRunID(t *testing.T) {
	mux, _ := setupTestServer()

	rr := doRequest(mux, "POST", "/tasks", map[string]string{"prompt": "test"})
	var createResp map[string]string
	json.NewDecoder(rr.Body).Decode(&createResp)

	rr = doRequest(mux, "POST", "/tasks/"+createResp["task_id"]+"/resume", map[string]interface{}{
		"from_run_id":     "",
		"from_step_index": 0,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty from_run_id, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestResumeTaskInvalidStep(t *testing.T) {
	mux, store := setupTestServer()

	rr := doRequest(mux, "POST", "/tasks", map[string]string{"prompt": "test"})
	var createResp map[string]string
	json.NewDecoder(rr.Body).Decode(&createResp)

	// Create steps with checkpoints - only 2.
	createStepsForResume(store, createResp["run_id"], 2)

	// Try to resume from step 10 which doesn't exist.
	rr = doRequest(mux, "POST", "/tasks/"+createResp["task_id"]+"/resume", map[string]interface{}{
		"from_run_id":     createResp["run_id"],
		"from_step_index": 10,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid step, got %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "checkpoint") || strings.Contains(rr.Body.String(), "step not found") {
		t.Fatalf("expected sanitized error message, got %s", rr.Body.String())
	}
}

func TestWSDisconnectRequiresAuth(t *testing.T) {
	store := state.NewMemoryStore()
	wsHandler := NewWSHandler(store, nil)
	mux := http.NewServeMux()
	wsHandler.RegisterRoutes(mux)

	// Try disconnect without auth headers.
	body, _ := json.Marshal(map[string]string{"connection_id": "conn_1"})
	req := httptest.NewRequest("POST", "/ws/disconnect", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestWSDisconnectTenantIsolation(t *testing.T) {
	store := state.NewMemoryStore()
	ctx := context.Background()

	// Create a connection owned by tnt_1.
	store.PutConnection(ctx, &model.Connection{
		ConnectionID: "conn_1",
		TenantID:     "tnt_1",
		TaskID:       "task_1",
		ConnectedAt:  time.Now().UTC(),
	})

	wsHandler := NewWSHandler(store, nil)
	mux := http.NewServeMux()
	wsHandler.RegisterRoutes(mux)

	// Try to disconnect as tnt_2 - should fail.
	body, _ := json.Marshal(map[string]string{"connection_id": "conn_1"})
	req := httptest.NewRequest("POST", "/ws/disconnect", bytes.NewReader(body))
	req.Header.Set("X-Tenant-Id", "tnt_2")
	req.Header.Set("X-User-Id", "user_1")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for wrong tenant disconnect, got %d: %s", rr.Code, rr.Body.String())
	}

	// Connection should still exist.
	conns, _ := store.GetConnectionsByTask(ctx, "task_1")
	if len(conns) != 1 {
		t.Fatalf("expected connection to still exist, got %d", len(conns))
	}
}

func TestListStepsLimitCap(t *testing.T) {
	mux, store := setupTestServer()

	rr := doRequest(mux, "POST", "/tasks", map[string]string{"prompt": "test"})
	var createResp map[string]string
	json.NewDecoder(rr.Body).Decode(&createResp)

	// Create many steps.
	createStepsForResume(store, createResp["run_id"], 5)

	// Request with very large limit - should be capped at 1000.
	rr = doRequest(mux, "GET", "/tasks/"+createResp["task_id"]+"/runs/"+createResp["run_id"]+"/steps?from=0&limit=99999", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Request with negative from - should default to 0.
	rr = doRequest(mux, "GET", "/tasks/"+createResp["task_id"]+"/runs/"+createResp["run_id"]+"/steps?from=-5&limit=10", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAuthMiddleware(t *testing.T) {
	mux, _ := setupTestServer()

	req := httptest.NewRequest("POST", "/tasks", bytes.NewReader([]byte(`{"prompt":"test"}`)))
	// No X-Tenant-Id header.
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestWSConnectTaskOwnershipValidation(t *testing.T) {
	store := state.NewMemoryStore()
	q := queue.NewMemoryQueue(100)
	svc := task.NewService(store, q)

	// Create a task owned by tnt_1.
	createResp, err := svc.Create(context.Background(), &task.CreateRequest{
		TenantID: "tnt_1",
		UserID:   "user_1",
		Prompt:   "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	wsHandler := NewWSHandler(store, svc)
	mux := http.NewServeMux()
	wsHandler.RegisterRoutes(mux)

	// tnt_2 tries to subscribe to tnt_1's task — should fail.
	body, _ := json.Marshal(map[string]string{
		"connection_id": "conn_evil",
		"task_id":       createResp.TaskID,
	})
	req := httptest.NewRequest("POST", "/ws/connect", bytes.NewReader(body))
	req.Header.Set("X-Tenant-Id", "tnt_2")
	req.Header.Set("X-User-Id", "user_evil")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-tenant WS connect, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify no connection was registered.
	conns, _ := store.GetConnectionsByTask(context.Background(), createResp.TaskID)
	if len(conns) != 0 {
		t.Fatalf("expected 0 connections, got %d", len(conns))
	}

	// Same tenant but different user should also be rejected.
	body, _ = json.Marshal(map[string]string{
		"connection_id": "conn_wrong_user",
		"task_id":       createResp.TaskID,
	})
	req = httptest.NewRequest("POST", "/ws/connect", bytes.NewReader(body))
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_2")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-user WS connect, got %d: %s", rr.Code, rr.Body.String())
	}

	// tnt_1 should be able to connect.
	body, _ = json.Marshal(map[string]string{
		"connection_id": "conn_legit",
		"task_id":       createResp.TaskID,
	})
	req = httptest.NewRequest("POST", "/ws/connect", bytes.NewReader(body))
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for same-tenant WS connect, got %d: %s", rr.Code, rr.Body.String())
	}

	conns, _ = store.GetConnectionsByTask(context.Background(), createResp.TaskID)
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(conns))
	}
}

func TestWSReconnectGapReplay(t *testing.T) {
	store := state.NewMemoryStore()
	q := queue.NewMemoryQueue(100)
	svc := task.NewService(store, q)
	pusher := stream.NewMockPusher()

	createResp, err := svc.Create(context.Background(), &task.CreateRequest{
		TenantID: "tnt_1",
		UserID:   "user_1",
		Prompt:   "test replay",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Persist events seq=1..3 for replay.
	for i := int64(1); i <= 3; i++ {
		if err := store.PutEvent(context.Background(), &model.StreamEvent{
			TaskID: createResp.TaskID,
			RunID:  createResp.RunID,
			Seq:    i,
			TS:     time.Now().Unix(),
			Type:   model.StreamEventStepEnd,
		}); err != nil {
			t.Fatal(err)
		}
	}

	wsHandler := NewWSHandler(store, svc)
	wsHandler.SetReplayPusher(pusher)
	mux := http.NewServeMux()
	wsHandler.RegisterRoutes(mux)

	body, _ := json.Marshal(map[string]interface{}{
		"connection_id": "conn_replay",
		"task_id":       createResp.TaskID,
		"run_id":        createResp.RunID,
		"last_seq":      1,
	})
	req := httptest.NewRequest("POST", "/ws/connect", bytes.NewReader(body))
	req.Header.Set("X-Tenant-Id", "tnt_1")
	req.Header.Set("X-User-Id", "user_1")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	events := pusher.Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 replay events, got %d", len(events))
	}
	if events[0].Event.Seq != 2 || events[1].Event.Seq != 3 {
		t.Fatalf("expected replay seq 2,3 got %d,%d", events[0].Event.Seq, events[1].Event.Seq)
	}
}

func TestListStepsWrongRunID(t *testing.T) {
	mux, store := setupTestServer()

	// Create a task.
	rr := doRequest(mux, "POST", "/tasks", map[string]string{"prompt": "test"})
	var createResp map[string]string
	json.NewDecoder(rr.Body).Decode(&createResp)

	createStepsForResume(store, createResp["run_id"], 3)

	// Try to list steps with a non-existent run_id.
	rr = doRequest(mux, "GET", "/tasks/"+createResp["task_id"]+"/runs/nonexistent_run/steps?from=0&limit=10", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for wrong run_id, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestResumeTaskWrongTaskRunID(t *testing.T) {
	mux, store := setupTestServer()

	// Create two tasks.
	rr1 := doRequest(mux, "POST", "/tasks", map[string]string{"prompt": "task1"})
	var resp1 map[string]string
	json.NewDecoder(rr1.Body).Decode(&resp1)

	rr2 := doRequest(mux, "POST", "/tasks", map[string]string{"prompt": "task2"})
	var resp2 map[string]string
	json.NewDecoder(rr2.Body).Decode(&resp2)

	// Create steps for task1's run.
	createStepsForResume(store, resp1["run_id"], 3)

	// Try to resume task2 using task1's run_id — should fail.
	rr := doRequest(mux, "POST", "/tasks/"+resp2["task_id"]+"/resume", map[string]interface{}{
		"from_run_id":     resp1["run_id"],
		"from_step_index": 0,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for cross-task resume, got %d: %s", rr.Code, rr.Body.String())
	}
}
