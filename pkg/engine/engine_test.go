package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	artstore "github.com/agentforge/agentforge/pkg/artifact"
	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/state"
	"github.com/agentforge/agentforge/pkg/stream"
	"github.com/agentforge/agentforge/pkg/workspace"
)

type testHarness struct {
	store     *state.MemoryStore
	artifacts *artstore.MemoryStore
	pusher    *stream.MockPusher
	task      *model.Task
	run       *model.Run
	ws        *workspace.LocalManager
	cleanup   func()
}

func setupHarness(t *testing.T) *testHarness {
	t.Helper()
	store := state.NewMemoryStore()
	artifacts := artstore.NewMemoryStore()
	pusher := stream.NewMockPusher()

	task := &model.Task{
		TaskID:    "task_test",
		TenantID:  "tnt_test",
		UserID:    "user_test",
		Status:    model.TaskStatusRunning,
		Prompt:    "Write a hello world file",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	store.PutTask(context.Background(), task)

	run := &model.Run{
		TaskID:   "task_test",
		RunID:    "run_test",
		TenantID: "tnt_test",
		Status:   model.RunStatusRunning,
	}
	store.PutRun(context.Background(), run)

	dir := t.TempDir()
	wsCfg := workspace.Config{Root: dir, MaxTotalBytes: 50 * 1024 * 1024, MaxFileCount: 2000}
	ws, _ := workspace.NewLocalManager(wsCfg)

	return &testHarness{
		store:     store,
		artifacts: artifacts,
		pusher:    pusher,
		task:      task,
		run:       run,
		ws:        ws,
		cleanup:   func() { ws.Cleanup() },
	}
}

func TestEngineBasicExecution(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	llm := NewMockLLMClient(2) // 2 tool calls + 1 final answer
	registry := NewRegistry()
	for _, tool := range NewFSTools(h.ws) {
		registry.Register(tool)
	}

	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)

	result, err := eng.Execute(context.Background(), h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}

	if result.Status != model.RunStatusSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s", result.Status)
	}

	// Verify steps were written.
	steps, err := h.store.ListSteps(context.Background(), "run_test", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	// 2 tool_call steps + 1 llm_call (final decision) + 1 final step = at least 3-4
	if len(steps) < 3 {
		t.Fatalf("expected at least 3 steps, got %d", len(steps))
	}

	// Verify workspace files were created by tools.
	data, err := h.ws.Read(context.Background(), "step_1.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "output from step 1" {
		t.Fatalf("unexpected file content: %q", data)
	}

	data, err = h.ws.Read(context.Background(), "step_2.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "output from step 2" {
		t.Fatalf("unexpected file content: %q", data)
	}

	// Verify checkpoints exist in artifact store.
	exists, _ := h.artifacts.Exists(context.Background(), WorkspaceS3Key("tnt_test", "task_test", "run_test", 0))
	if !exists {
		t.Fatal("expected workspace snapshot for step 0")
	}
}

func TestEngineAbort(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	// Set abort before execution.
	h.store.SetAbortRequested(context.Background(), "task_test", "user requested abort")

	llm := NewMockLLMClient(5) // Would do 5 steps, but should abort
	registry := NewRegistry()
	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)

	result, err := eng.Execute(context.Background(), h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}

	if result.Status != model.RunStatusAborted {
		t.Fatalf("expected ABORTED, got %s", result.Status)
	}
}

func TestEngineResume(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	// First run: execute 2 steps.
	llm := NewMockLLMClient(2)
	registry := NewRegistry()
	for _, tool := range NewFSTools(h.ws) {
		registry.Register(tool)
	}

	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)
	result, err := eng.Execute(context.Background(), h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusSucceeded {
		t.Fatalf("first run: expected SUCCEEDED, got %s", result.Status)
	}

	// Create a new run that resumes from step 1.
	stepIdx := 1
	resumeRun := &model.Run{
		TaskID:              "task_test",
		RunID:               "run_resume",
		TenantID:            "tnt_test",
		Status:              model.RunStatusRunning,
		ParentRunID:         "run_test",
		ResumeFromStepIndex: &stepIdx,
	}
	h.store.PutRun(context.Background(), resumeRun)

	// New workspace for resumed run.
	dir2 := t.TempDir()
	wsCfg2 := workspace.Config{Root: dir2, MaxTotalBytes: 50 * 1024 * 1024, MaxFileCount: 2000}
	ws2, _ := workspace.NewLocalManager(wsCfg2)
	defer ws2.Cleanup()

	llm2 := NewMockLLMClient(1) // 1 more tool call + final
	registry2 := NewRegistry()
	for _, tool := range NewFSTools(ws2) {
		registry2.Register(tool)
	}

	eng2 := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm2, registry2, h.pusher)
	result2, err := eng2.Execute(context.Background(), h.task, resumeRun, ws2)
	if err != nil {
		t.Fatal(err)
	}
	if result2.Status != model.RunStatusSucceeded {
		t.Fatalf("resume run: expected SUCCEEDED, got %s", result2.Status)
	}

	// Verify that the restored workspace has the file from step 1.
	data, err := ws2.Read(context.Background(), "step_1.txt")
	if err != nil {
		t.Fatal("restored workspace should have step_1.txt:", err)
	}
	if string(data) != "output from step 1" {
		t.Fatalf("unexpected restored content: %q", data)
	}

	// Verify new steps were written for the resume run.
	steps, err := h.store.ListSteps(context.Background(), "run_resume", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) < 2 {
		t.Fatalf("expected at least 2 steps in resume run, got %d", len(steps))
	}
}

func TestFSExportTool(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()
	ctx := context.Background()

	// Write some files.
	h.ws.Write(ctx, "a.txt", []byte("alpha"))
	h.ws.Write(ctx, "b.txt", []byte("beta"))

	// Create export tool.
	tools := NewFSToolsWithArtifacts(h.ws, h.artifacts)
	var exportTool Tool
	for _, tool := range tools {
		if tool.Name() == "fs.export" {
			exportTool = tool
			break
		}
	}
	if exportTool == nil {
		t.Fatal("fs.export tool not found")
	}

	result, err := exportTool.Execute(ctx, `{"paths":["a.txt","b.txt"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("fs.export error: %s", result.Error)
	}
	if result.Output == "" {
		t.Fatal("expected non-empty output")
	}
	// Output should contain s3_key.
	if !strings.Contains(result.Output, "s3_key") {
		t.Fatalf("expected s3_key in output: %s", result.Output)
	}
}

func TestFSExportToolMissingPaths(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	tools := NewFSToolsWithArtifacts(h.ws, h.artifacts)
	var exportTool Tool
	for _, tool := range tools {
		if tool.Name() == "fs.export" {
			exportTool = tool
			break
		}
	}

	result, err := exportTool.Execute(context.Background(), `{"paths":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == "" {
		t.Fatal("expected error for empty paths")
	}
}

func TestEngineResumeAfterAbort(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	// Set abort flag.
	h.store.SetAbortRequested(context.Background(), "task_test", "user abort")

	// Verify abort flag is set.
	taskObj, _ := h.store.GetTask(context.Background(), "task_test")
	if !taskObj.AbortRequested {
		t.Fatal("expected abort_requested=true")
	}

	// Clear abort (simulating what Resume service does).
	h.store.ClearAbortRequested(context.Background(), "task_test")

	taskObj, _ = h.store.GetTask(context.Background(), "task_test")
	if taskObj.AbortRequested {
		t.Fatal("expected abort_requested=false after clear")
	}
	if taskObj.AbortReason != "" {
		t.Fatal("expected empty abort_reason after clear")
	}

	// Engine should now execute normally.
	llm := NewMockLLMClient(1)
	registry := NewRegistry()
	for _, tool := range NewFSTools(h.ws) {
		registry.Register(tool)
	}

	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)
	result, err := eng.Execute(context.Background(), taskObj, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusSucceeded {
		t.Fatalf("expected SUCCEEDED after abort clear, got %s", result.Status)
	}
}

func TestEngineContextCancellation(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	llm := NewMockLLMClient(5)
	registry := NewRegistry()
	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)

	result, err := eng.Execute(ctx, h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusFailed {
		t.Fatalf("expected FAILED on ctx cancel, got %s", result.Status)
	}
}

// errorLLMClient returns an error on every call.
type errorLLMClient struct {
	err error
}

func (c *errorLLMClient) Chat(_ context.Context, _ *LLMRequest) (*LLMResponse, error) {
	return nil, c.err
}

func TestEngineLLMFailure(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	llm := &errorLLMClient{err: errors.New("upstream model timeout")}
	registry := NewRegistry()
	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)

	result, err := eng.Execute(context.Background(), h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err) // Engine should not return error; failure is in result.
	}
	if result.Status != model.RunStatusFailed {
		t.Fatalf("expected FAILED, got %s", result.Status)
	}
	if result.ErrorMessage != "upstream model timeout" {
		t.Fatalf("expected error message, got %q", result.ErrorMessage)
	}

	// Verify error step was written.
	step, err := h.store.GetStep(context.Background(), "run_test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != model.StepStatusError {
		t.Fatalf("expected step ERROR status, got %s", step.Status)
	}
	if step.ErrorCode != "LLM_ERROR" {
		t.Fatalf("expected LLM_ERROR code, got %q", step.ErrorCode)
	}
}

func TestEngineLLMFailureEmitsErrorEvent(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	ctx := context.Background()

	// Register a connection so events are pushed.
	conn := &model.Connection{
		ConnectionID: "conn_err",
		TenantID:     "tnt_test",
		TaskID:       "task_test",
		RunID:        "run_test",
		ConnectedAt:  time.Now().UTC(),
		TTL:          time.Now().Add(2 * time.Hour).Unix(),
	}
	h.store.PutConnection(ctx, conn)

	llm := &errorLLMClient{err: errors.New("upstream 500")}
	registry := NewRegistry()
	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)
	eng.Execute(ctx, h.task, h.run, h.ws)

	// Verify an error stream event was pushed.
	events := h.pusher.Events()
	foundError := false
	for _, e := range events {
		if e.Event.Type == model.StreamEventError {
			foundError = true
			data, ok := e.Event.Data.(map[string]interface{})
			if !ok {
				t.Fatal("error event data should be a map")
			}
			if data["error_code"] != "LLM_ERROR" {
				t.Fatalf("expected LLM_ERROR error_code, got %v", data["error_code"])
			}
		}
	}
	if !foundError {
		t.Fatal("expected an error stream event to be pushed on LLM failure")
	}
}

func TestEngineMaxSteps(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	// LLM always returns tool calls, never a final answer.
	llm := NewMockLLMClient(1000) // Way more than MaxSteps.
	registry := NewRegistry()
	for _, tool := range NewFSTools(h.ws) {
		registry.Register(tool)
	}

	cfg := Config{MaxSteps: 3}
	eng := NewEngine(cfg, h.store, h.artifacts, llm, registry, h.pusher)

	result, err := eng.Execute(context.Background(), h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusFailed {
		t.Fatalf("expected FAILED (max steps), got %s", result.Status)
	}
	if result.ErrorMessage != "max steps reached" {
		t.Fatalf("expected 'max steps reached', got %q", result.ErrorMessage)
	}
}

func TestEngineStreamPushGoneConnectionCleanup(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	ctx := context.Background()

	// Register a connection for the task.
	conn := &model.Connection{
		ConnectionID: "conn_1",
		TenantID:     "tnt_test",
		TaskID:       "task_test",
		RunID:        "run_test",
		ConnectedAt:  time.Now().UTC(),
		TTL:          time.Now().Add(2 * time.Hour).Unix(),
	}
	h.store.PutConnection(ctx, conn)

	// Mark connection as gone (simulates 410 response from API Gateway).
	h.pusher.GoneConnections["conn_1"] = true

	llm := NewMockLLMClient(1)
	registry := NewRegistry()
	for _, tool := range NewFSTools(h.ws) {
		registry.Register(tool)
	}

	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)
	result, err := eng.Execute(ctx, h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s", result.Status)
	}

	// Verify the gone connection was cleaned up.
	conns, err := h.store.GetConnectionsByTask(ctx, "task_test")
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 0 {
		t.Fatalf("expected 0 connections after cleanup, got %d", len(conns))
	}
}

func TestEngineStreamPushLiveConnection(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	ctx := context.Background()

	// Register a live connection.
	conn := &model.Connection{
		ConnectionID: "conn_live",
		TenantID:     "tnt_test",
		TaskID:       "task_test",
		RunID:        "run_test",
		ConnectedAt:  time.Now().UTC(),
		TTL:          time.Now().Add(2 * time.Hour).Unix(),
	}
	h.store.PutConnection(ctx, conn)

	llm := NewMockLLMClient(1)
	registry := NewRegistry()
	for _, tool := range NewFSTools(h.ws) {
		registry.Register(tool)
	}

	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)
	result, err := eng.Execute(ctx, h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s", result.Status)
	}

	// Verify events were pushed.
	events := h.pusher.Events()
	if len(events) == 0 {
		t.Fatal("expected pushed events")
	}

	// All events should be to conn_live.
	for _, ev := range events {
		if ev.ConnectionID != "conn_live" {
			t.Fatalf("expected conn_live, got %s", ev.ConnectionID)
		}
	}

	// Connection should still exist (not cleaned up).
	conns, _ := h.store.GetConnectionsByTask(ctx, "task_test")
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection still alive, got %d", len(conns))
	}

	// Verify we got step_start, step_end, tool_call, tool_result, complete events.
	typeCount := make(map[model.StreamEventType]int)
	for _, ev := range events {
		typeCount[ev.Event.Type]++
	}
	if typeCount[model.StreamEventStepStart] == 0 {
		t.Fatal("expected step_start events")
	}
	if typeCount[model.StreamEventStepEnd] == 0 {
		t.Fatal("expected step_end events")
	}
	if typeCount[model.StreamEventComplete] == 0 {
		t.Fatal("expected complete event")
	}
}

func TestEngineCheckpointIntegrity(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	llm := NewMockLLMClient(2)
	registry := NewRegistry()
	for _, tool := range NewFSTools(h.ws) {
		registry.Register(tool)
	}

	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)
	result, err := eng.Execute(context.Background(), h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s", result.Status)
	}

	// Verify each step has a checkpoint_ref with both memory and workspace.
	steps, _ := h.store.ListSteps(context.Background(), "run_test", 0, 100)
	for _, step := range steps {
		if step.CheckpointRef == nil {
			t.Fatalf("step %d missing checkpoint_ref", step.StepIndex)
		}
		if step.CheckpointRef.Memory == nil {
			t.Fatalf("step %d missing memory ref", step.StepIndex)
		}
		if step.CheckpointRef.Memory.S3Key == "" {
			t.Fatalf("step %d memory ref has empty S3Key", step.StepIndex)
		}
		if step.CheckpointRef.Memory.SHA256 == "" {
			t.Fatalf("step %d memory ref has empty SHA256", step.StepIndex)
		}
		if step.CheckpointRef.Workspace == nil {
			t.Fatalf("step %d missing workspace ref", step.StepIndex)
		}
		if step.CheckpointRef.Workspace.S3Key == "" {
			t.Fatalf("step %d workspace ref has empty S3Key", step.StepIndex)
		}

		// Verify the artifacts actually exist in the store.
		exists, _ := h.artifacts.Exists(context.Background(), step.CheckpointRef.Memory.S3Key)
		if !exists {
			t.Fatalf("step %d memory artifact not found: %s", step.StepIndex, step.CheckpointRef.Memory.S3Key)
		}
		exists, _ = h.artifacts.Exists(context.Background(), step.CheckpointRef.Workspace.S3Key)
		if !exists {
			t.Fatalf("step %d workspace artifact not found: %s", step.StepIndex, step.CheckpointRef.Workspace.S3Key)
		}
	}
}

func TestRegistryListTools(t *testing.T) {
	registry := NewRegistry()
	ws, _ := workspace.NewLocalManager(workspace.Config{Root: t.TempDir(), MaxTotalBytes: 1024, MaxFileCount: 10})
	defer ws.Cleanup()

	for _, tool := range NewFSTools(ws) {
		registry.Register(tool)
	}

	names := registry.List()
	if len(names) != 5 {
		t.Fatalf("expected 5 tools, got %d: %v", len(names), names)
	}

	// Verify specific tool names exist.
	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	expected := []string{"fs.write", "fs.read", "fs.list", "fs.stat", "fs.delete"}
	for _, exp := range expected {
		if !nameSet[exp] {
			t.Fatalf("missing tool: %s", exp)
		}
	}
}

func TestEngineUnknownToolHandling(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	// Custom LLM that returns a call to an unknown tool.
	llm := &unknownToolLLM{}
	registry := NewRegistry()
	// Don't register any tools.

	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)
	result, err := eng.Execute(context.Background(), h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	// Should succeed (unknown tool returns error message which the LLM uses to give final answer).
	if result.Status != model.RunStatusSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s (error: %s)", result.Status, result.ErrorMessage)
	}
}

func TestEngineStreamPushTenantIsolation(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	ctx := context.Background()

	// Register a connection for a DIFFERENT tenant.
	conn := &model.Connection{
		ConnectionID: "conn_other_tenant",
		TenantID:     "tnt_other", // Different from task's tnt_test
		TaskID:       "task_test",
		RunID:        "run_test",
		ConnectedAt:  time.Now().UTC(),
		TTL:          time.Now().Add(2 * time.Hour).Unix(),
	}
	h.store.PutConnection(ctx, conn)

	// Register a connection for the SAME tenant.
	conn2 := &model.Connection{
		ConnectionID: "conn_same_tenant",
		TenantID:     "tnt_test", // Same as task's tenant
		TaskID:       "task_test",
		RunID:        "run_test",
		ConnectedAt:  time.Now().UTC(),
		TTL:          time.Now().Add(2 * time.Hour).Unix(),
	}
	h.store.PutConnection(ctx, conn2)

	llm := NewMockLLMClient(1)
	registry := NewRegistry()
	for _, tool := range NewFSTools(h.ws) {
		registry.Register(tool)
	}

	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)
	result, err := eng.Execute(ctx, h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s", result.Status)
	}

	// Verify only the same-tenant connection received events.
	events := h.pusher.Events()
	for _, ev := range events {
		if ev.ConnectionID == "conn_other_tenant" {
			t.Fatal("cross-tenant connection should NOT have received events")
		}
	}

	// Verify same-tenant connection received events.
	found := false
	for _, ev := range events {
		if ev.ConnectionID == "conn_same_tenant" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("same-tenant connection should have received events")
	}
}

// unknownToolLLM returns a call to an unknown tool on first call, then a final answer.
type unknownToolLLM struct {
	calls int
}

func (l *unknownToolLLM) Chat(_ context.Context, _ *LLMRequest) (*LLMResponse, error) {
	l.calls++
	if l.calls == 1 {
		return &LLMResponse{
			Content:      "Let me try a tool",
			ToolCalls:    []ToolCall{{Name: "nonexistent.tool", Args: `{}`}},
			TokenUsage:   &model.TokenUsage{Input: 10, Output: 5, Total: 15},
			FinishReason: "tool_calls",
		}, nil
	}
	return &LLMResponse{
		Content:      "Done",
		TokenUsage:   &model.TokenUsage{Input: 10, Output: 5, Total: 15},
		FinishReason: "stop",
	}, nil
}
