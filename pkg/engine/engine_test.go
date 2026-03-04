package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
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

func TestEnginePromptPolicyRejectsTooLongPrompt(t *testing.T) {
	t.Setenv("AGENTFORGE_PROMPT_MAX_CHARS", "8")
	h := setupHarness(t)
	defer h.cleanup()
	h.task.Prompt = "this prompt is too long"

	llm := NewMockLLMClient(1)
	registry := NewRegistry()
	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)

	result, err := eng.Execute(context.Background(), h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusFailed {
		t.Fatalf("expected FAILED, got %s", result.Status)
	}
	if !strings.Contains(result.ErrorMessage, "maximum length") {
		t.Fatalf("unexpected error message: %q", result.ErrorMessage)
	}
}

func TestEngineAbortEmitsCompleteEvent(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()
	ctx := context.Background()

	h.store.PutConnection(ctx, &model.Connection{
		ConnectionID: "conn_abort",
		TenantID:     "tnt_test",
		TaskID:       "task_test",
		RunID:        "run_test",
		ConnectedAt:  time.Now().UTC(),
		TTL:          time.Now().Add(2 * time.Hour).Unix(),
	})
	h.store.SetAbortRequested(ctx, "task_test", "user requested abort")

	llm := NewMockLLMClient(2)
	registry := NewRegistry()
	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)

	result, err := eng.Execute(ctx, h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusAborted {
		t.Fatalf("expected ABORTED, got %s", result.Status)
	}

	events := h.pusher.Events()
	found := false
	for _, ev := range events {
		if ev.Event.Type == model.StreamEventComplete {
			data, ok := ev.Event.Data.(map[string]interface{})
			if !ok {
				t.Fatal("complete event data should be a map")
			}
			if data["status"] == "aborted" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatal("expected an aborted complete event")
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

func TestFSExportToolPrefix(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()
	ctx := context.Background()

	if err := h.ws.Write(ctx, "a.txt", []byte("alpha")); err != nil {
		t.Fatal(err)
	}

	tools := NewFSToolsWithArtifactsPrefix(h.ws, h.artifacts, "exports/tnt_test/task_test/run_test")
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

	result, err := exportTool.Execute(ctx, `{"paths":["a.txt"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("fs.export error: %s", result.Error)
	}

	var payload struct {
		S3Key  string `json:"s3_key"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("invalid fs.export output: %v", err)
	}
	if !strings.HasPrefix(payload.S3Key, "exports/tnt_test/task_test/run_test/") {
		t.Fatalf("expected tenant/task/run prefix in s3_key, got %s", payload.S3Key)
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

func TestEngineStreamingCancellationDuringResponse(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	llm := &slowStreamingLLM{chunks: 500, delay: 5 * time.Millisecond}
	registry := NewRegistry()
	for _, tool := range NewFSTools(h.ws) {
		registry.Register(tool)
	}

	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	result, err := eng.Execute(ctx, h.task, h.run, h.ws)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusFailed {
		t.Fatalf("expected FAILED on mid-stream cancellation, got %s", result.Status)
	}
	if !strings.Contains(strings.ToLower(result.ErrorMessage), "context canceled") {
		t.Fatalf("expected context cancellation message, got %q", result.ErrorMessage)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("expected prompt cancellation handling, elapsed=%s", elapsed)
	}
}

func TestEngineExternalizesLargeStepFields(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	huge := strings.Repeat("x", maxInlineStepFieldBytes+2048)
	llm := &hugeFinalLLM{content: huge}
	registry := NewRegistry()

	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)
	result, err := eng.Execute(context.Background(), h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s", result.Status)
	}

	steps, err := h.store.ListSteps(context.Background(), h.run.RunID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) < 2 {
		t.Fatalf("expected at least 2 steps, got %d", len(steps))
	}

	for _, step := range steps {
		if step.Input == "" || !strings.HasPrefix(step.Input, externalizedStepFieldTag) {
			continue
		}
		raw := strings.TrimPrefix(step.Input, externalizedStepFieldTag)
		var meta externalizedStepField
		if err := json.Unmarshal([]byte(raw), &meta); err != nil {
			t.Fatalf("unmarshal externalized metadata: %v", err)
		}
		if meta.Artifact.S3Key == "" {
			t.Fatal("expected artifact s3_key in externalized metadata")
		}
		exists, err := h.artifacts.Exists(context.Background(), meta.Artifact.S3Key)
		if err != nil {
			t.Fatalf("artifact exists check failed: %v", err)
		}
		if !exists {
			t.Fatalf("expected externalized artifact to exist: %s", meta.Artifact.S3Key)
		}
		if meta.OriginalBytes <= maxInlineStepFieldBytes {
			t.Fatalf("expected original bytes above threshold, got %d", meta.OriginalBytes)
		}
		return
	}

	t.Fatal("expected at least one step with externalized input metadata")
}

// errorLLMClient returns an error on every call.
type errorLLMClient struct {
	err error
}

func (c *errorLLMClient) Chat(_ context.Context, _ *LLMRequest) (*LLMResponse, error) {
	return nil, c.err
}

type slowStreamingLLM struct {
	chunks int
	delay  time.Duration
}

func (l *slowStreamingLLM) Chat(ctx context.Context, _ *LLMRequest) (*LLMResponse, error) {
	return l.ChatStream(ctx, nil, nil)
}

func (l *slowStreamingLLM) ChatStream(ctx context.Context, _ *LLMRequest, onToken func(token string)) (*LLMResponse, error) {
	chunks := l.chunks
	if chunks <= 0 {
		chunks = 100
	}
	delay := l.delay
	if delay <= 0 {
		delay = 10 * time.Millisecond
	}
	for i := 0; i < chunks; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if onToken != nil {
			onToken("x")
		}
	}
	return &LLMResponse{
		Content:      "done",
		FinishReason: "stop",
		TokenUsage:   &model.TokenUsage{Input: 1, Output: 1, Total: 2},
		ModelID:      "mock-stream",
		Provider:     "mock",
	}, nil
}

type hugeFinalLLM struct {
	content string
}

func (l *hugeFinalLLM) Chat(_ context.Context, _ *LLMRequest) (*LLMResponse, error) {
	content := l.content
	if content == "" {
		content = strings.Repeat("x", maxInlineStepFieldBytes+1024)
	}
	return &LLMResponse{
		Content:      content,
		FinishReason: "stop",
		TokenUsage:   &model.TokenUsage{Input: 1, Output: 1, Total: 2},
		ModelID:      "mock-huge",
		Provider:     "mock",
	}, nil
}

type concurrencyProbePusher struct {
	delay       time.Duration
	inFlight    atomic.Int64
	maxInFlight atomic.Int64
	calls       atomic.Int64
}

func (p *concurrencyProbePusher) Push(ctx context.Context, _ string, _ *model.StreamEvent) (bool, error) {
	curr := p.inFlight.Add(1)
	for {
		prev := p.maxInFlight.Load()
		if curr <= prev {
			break
		}
		if p.maxInFlight.CompareAndSwap(prev, curr) {
			break
		}
	}
	p.calls.Add(1)
	defer p.inFlight.Add(-1)

	select {
	case <-ctx.Done():
		return true, ctx.Err()
	case <-time.After(p.delay):
		return true, nil
	}
}

type lengthThenStopLLM struct {
	lengthResponses   int
	calls             int
	sawContinuePrompt bool
}

func (l *lengthThenStopLLM) Chat(_ context.Context, req *LLMRequest) (*LLMResponse, error) {
	l.calls++
	if len(req.Messages) > 0 {
		last := req.Messages[len(req.Messages)-1]
		if last.Role == model.MessageRoleUser && strings.Contains(last.Content, "truncated") {
			l.sawContinuePrompt = true
		}
	}
	if l.calls <= l.lengthResponses {
		return &LLMResponse{
			Content:      "partial answer chunk",
			TokenUsage:   &model.TokenUsage{Input: 10, Output: 10, Total: 20},
			FinishReason: "length",
		}, nil
	}
	return &LLMResponse{
		Content:      "final answer",
		TokenUsage:   &model.TokenUsage{Input: 10, Output: 5, Total: 15},
		FinishReason: "stop",
	}, nil
}

type alwaysLengthLLM struct{}

func (l *alwaysLengthLLM) Chat(_ context.Context, _ *LLMRequest) (*LLMResponse, error) {
	return &LLMResponse{
		Content:      "still truncated",
		TokenUsage:   &model.TokenUsage{Input: 10, Output: 10, Total: 20},
		FinishReason: "length",
	}, nil
}

type recordingLLM struct {
	inner   LLMClient
	maxSeen int
}

func (l *recordingLLM) Chat(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	if n := len(req.Messages); n > l.maxSeen {
		l.maxSeen = n
	}
	return l.inner.Chat(ctx, req)
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

func TestEngineHandlesLengthFinishReasonWithContinuation(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	llm := &lengthThenStopLLM{lengthResponses: 1}
	registry := NewRegistry()
	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, registry, h.pusher)

	result, err := eng.Execute(context.Background(), h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s (error: %s)", result.Status, result.ErrorMessage)
	}
	if !llm.sawContinuePrompt {
		t.Fatal("expected continuation prompt after length finish_reason")
	}
}

func TestEngineFailsOnRepeatedLengthFinishReason(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	cfg := DefaultEngineConfig()
	cfg.MaxSteps = 10
	llm := &alwaysLengthLLM{}
	registry := NewRegistry()
	eng := NewEngine(cfg, h.store, h.artifacts, llm, registry, h.pusher)

	result, err := eng.Execute(context.Background(), h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusFailed {
		t.Fatalf("expected FAILED, got %s", result.Status)
	}
	if !strings.Contains(result.ErrorMessage, "truncated") {
		t.Fatalf("expected truncated error message, got %q", result.ErrorMessage)
	}
}

func TestEngineTrimsMemoryWindowBeforeLLMCall(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	rec := &recordingLLM{inner: NewMockLLMClient(40)}
	registry := NewRegistry()
	for _, tool := range NewFSTools(h.ws) {
		registry.Register(tool)
	}

	cfg := DefaultEngineConfig()
	cfg.MaxSteps = 60
	eng := NewEngine(cfg, h.store, h.artifacts, rec, registry, h.pusher)

	result, err := eng.Execute(context.Background(), h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s (error: %s)", result.Status, result.ErrorMessage)
	}
	if rec.maxSeen > maxEngineMemoryMessages {
		t.Fatalf("expected llm message window <= %d, got %d", maxEngineMemoryMessages, rec.maxSeen)
	}
}

func TestEngineFailsWhenTokenCapExceeded(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	h.run.ModelConfig = &model.ModelConfig{
		ModelID:   "gpt-4o-mini",
		MaxTokens: 10,
	}
	llm := &fixedUsageLLM{
		response: &LLMResponse{
			Content:      "short answer",
			FinishReason: "stop",
			ModelID:      "gpt-4o-mini",
			TokenUsage:   &model.TokenUsage{Input: 8, Output: 7, Total: 15},
		},
	}
	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, NewRegistry(), h.pusher)

	result, err := eng.Execute(context.Background(), h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusFailed {
		t.Fatalf("expected FAILED, got %s", result.Status)
	}
	if !strings.Contains(result.ErrorMessage, "token cap exceeded") {
		t.Fatalf("expected token cap error, got %q", result.ErrorMessage)
	}

	step, err := h.store.GetStep(context.Background(), h.run.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if step.ErrorCode != "TOKEN_CAP_EXCEEDED" {
		t.Fatalf("expected TOKEN_CAP_EXCEEDED, got %q", step.ErrorCode)
	}
}

func TestEngineFailsWhenCostCapExceeded(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	h.run.ModelConfig = &model.ModelConfig{
		ModelID:    "gpt-4o",
		CostCapUSD: 0.0001,
	}
	llm := &fixedUsageLLM{
		response: &LLMResponse{
			Content:      "costly answer",
			FinishReason: "stop",
			ModelID:      "gpt-4o",
			TokenUsage:   &model.TokenUsage{Input: 1000, Output: 1000, Total: 2000},
		},
	}
	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, llm, NewRegistry(), h.pusher)

	result, err := eng.Execute(context.Background(), h.task, h.run, h.ws)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.RunStatusFailed {
		t.Fatalf("expected FAILED, got %s", result.Status)
	}
	if !strings.Contains(result.ErrorMessage, "cost cap exceeded") {
		t.Fatalf("expected cost cap error, got %q", result.ErrorMessage)
	}

	step, err := h.store.GetStep(context.Background(), h.run.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if step.ErrorCode != "COST_CAP_EXCEEDED" {
		t.Fatalf("expected COST_CAP_EXCEEDED, got %q", step.ErrorCode)
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
	if typeCount[model.StreamEventTokenChunk] == 0 {
		t.Fatal("expected token_chunk events from streaming LLM output")
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

func TestEnginePersistsAllToolOutputsForMultiToolStep(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	llm := &multiToolLLM{}
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
		t.Fatalf("expected SUCCEEDED, got %s (error: %s)", result.Status, result.ErrorMessage)
	}

	step, err := h.store.GetStep(context.Background(), h.run.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if step.Type != model.StepTypeToolCall {
		t.Fatalf("expected first step to be tool_call, got %s", step.Type)
	}

	var outputs []persistedToolOutput
	if err := json.Unmarshal([]byte(step.Output), &outputs); err != nil {
		t.Fatalf("expected JSON array of tool outputs, got %q: %v", step.Output, err)
	}
	if len(outputs) != 2 {
		t.Fatalf("expected 2 persisted tool outputs, got %d", len(outputs))
	}

	var sawA, sawB bool
	for _, out := range outputs {
		if out.Tool != "fs.write" {
			t.Fatalf("expected tool fs.write, got %q", out.Tool)
		}
		if out.ToolCallID == "" {
			t.Fatal("expected tool_call_id to be persisted")
		}
		if strings.Contains(out.Output, "a.txt") {
			sawA = true
		}
		if strings.Contains(out.Output, "b.txt") {
			sawB = true
		}
	}
	if !sawA || !sawB {
		t.Fatalf("expected outputs for both writes, got %+v", outputs)
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

func TestSanitizeEventTextRedactsSecretsAndTruncates(t *testing.T) {
	rawJSON := `{"api_key":"sk-live-123","nested":{"token":"tok_secret_value_12345"},"ok":"value"}`
	sanitized := sanitizeEventText(rawJSON)
	if !strings.Contains(sanitized, eventTextRedacted) {
		t.Fatalf("expected redacted marker in sanitized output: %s", sanitized)
	}
	if strings.Contains(sanitized, "sk-live-123") || strings.Contains(sanitized, "tok_secret_value_12345") {
		t.Fatalf("sensitive values should be redacted: %s", sanitized)
	}

	longText := "Authorization: Bearer very-secret-token " + strings.Repeat("x", maxSanitizedEventTextRunes+64)
	sanitizedLong := sanitizeEventText(longText)
	if strings.Contains(sanitizedLong, "very-secret-token") {
		t.Fatalf("bearer token should be redacted: %s", sanitizedLong)
	}
	if !strings.Contains(sanitizedLong, eventTextTruncatedSuffix) {
		t.Fatalf("expected truncation suffix, got: %s", sanitizedLong)
	}
	if got := len([]rune(sanitizedLong)); got > maxSanitizedEventTextRunes {
		t.Fatalf("expected <= %d runes, got %d", maxSanitizedEventTextRunes, got)
	}
}

func TestEnginePushEventSanitizesSensitivePayloads(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	ctx := context.Background()
	if err := h.store.PutConnection(ctx, &model.Connection{
		ConnectionID: "conn_sensitive",
		TenantID:     h.task.TenantID,
		TaskID:       h.task.TaskID,
		RunID:        h.run.RunID,
		ConnectedAt:  time.Now().UTC(),
		TTL:          time.Now().Add(2 * time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, nil, nil, h.pusher)
	var seq int64
	eng.pushEvent(ctx, h.task.TenantID, h.task.TaskID, h.run.RunID, &seq, model.StreamEventToolCall, map[string]interface{}{
		"tool": "fs.write",
		"args": `{"api_key":"sk-live-xyz","path":"a.txt"}`,
	})
	eng.pushEvent(ctx, h.task.TenantID, h.task.TaskID, h.run.RunID, &seq, model.StreamEventToolResult, map[string]interface{}{
		"tool":   "fs.write",
		"output": "Authorization: Bearer super-secret-token",
	})
	eng.pushEvent(ctx, h.task.TenantID, h.task.TaskID, h.run.RunID, &seq, model.StreamEventComplete, map[string]interface{}{
		"final_answer": "token=final_secret " + strings.Repeat("x", maxSanitizedEventTextRunes+32),
	})

	pushed := h.pusher.Events()
	if len(pushed) != 3 {
		t.Fatalf("expected 3 pushed events, got %d", len(pushed))
	}

	toolCallData, ok := pushed[0].Event.Data.(map[string]interface{})
	if !ok {
		t.Fatal("tool_call data should be a map")
	}
	args, _ := toolCallData["args"].(string)
	if strings.Contains(args, "sk-live-xyz") || !strings.Contains(args, eventTextRedacted) {
		t.Fatalf("tool_call args not sanitized: %s", args)
	}

	toolResultData, ok := pushed[1].Event.Data.(map[string]interface{})
	if !ok {
		t.Fatal("tool_result data should be a map")
	}
	output, _ := toolResultData["output"].(string)
	if strings.Contains(output, "super-secret-token") || !strings.Contains(output, eventTextRedacted) {
		t.Fatalf("tool_result output not sanitized: %s", output)
	}

	completeData, ok := pushed[2].Event.Data.(map[string]interface{})
	if !ok {
		t.Fatal("complete data should be a map")
	}
	finalAnswer, _ := completeData["final_answer"].(string)
	if strings.Contains(finalAnswer, "final_secret") {
		t.Fatalf("final answer not sanitized: %s", finalAnswer)
	}
	if !strings.Contains(finalAnswer, eventTextTruncatedSuffix) {
		t.Fatalf("expected truncated final answer, got: %s", finalAnswer)
	}

	stored, err := h.store.ReplayEvents(ctx, h.task.TaskID, h.run.RunID, 0, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 3 {
		t.Fatalf("expected 3 stored events, got %d", len(stored))
	}
	storedToolCall, ok := stored[0].Data.(map[string]interface{})
	if !ok {
		t.Fatal("stored tool_call data should be a map")
	}
	storedArgs, _ := storedToolCall["args"].(string)
	if strings.Contains(storedArgs, "sk-live-xyz") || !strings.Contains(storedArgs, eventTextRedacted) {
		t.Fatalf("stored tool_call args not sanitized: %s", storedArgs)
	}
}

func TestEnginePushEventUsesBoundedConcurrency(t *testing.T) {
	h := setupHarness(t)
	defer h.cleanup()

	const connectionCount = streamPushMaxConcurrency*3 + 5
	for i := 0; i < connectionCount; i++ {
		if err := h.store.PutConnection(context.Background(), &model.Connection{
			ConnectionID: fmt.Sprintf("conn_%d", i),
			TenantID:     h.task.TenantID,
			UserID:       h.task.UserID,
			TaskID:       h.task.TaskID,
			RunID:        h.run.RunID,
			ConnectedAt:  time.Now().UTC(),
			TTL:          time.Now().Add(2 * time.Hour).Unix(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	pusher := &concurrencyProbePusher{delay: 50 * time.Millisecond}
	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, nil, nil, pusher)

	var seq int64
	eng.pushEvent(context.Background(), h.task.TenantID, h.task.TaskID, h.run.RunID, &seq, model.StreamEventTokenChunk, map[string]interface{}{
		"token": "x",
	})

	if got := pusher.calls.Load(); got != int64(connectionCount) {
		t.Fatalf("expected %d push calls, got %d", connectionCount, got)
	}
	if got := pusher.maxInFlight.Load(); got > int64(streamPushMaxConcurrency) {
		t.Fatalf("expected max in-flight <= %d, got %d", streamPushMaxConcurrency, got)
	}
	if got := pusher.maxInFlight.Load(); got < 2 {
		t.Fatalf("expected concurrent pushes, max in-flight was %d", got)
	}
}

func TestEnginePushEventRateLimitedAfterBurst(t *testing.T) {
	if streamPushRatePerSecond <= 0 {
		t.Skip("stream push rate limit disabled")
	}

	h := setupHarness(t)
	defer h.cleanup()

	extraConnections := 20
	connectionCount := streamPushMaxConcurrency + extraConnections
	for i := 0; i < connectionCount; i++ {
		if err := h.store.PutConnection(context.Background(), &model.Connection{
			ConnectionID: fmt.Sprintf("rate_conn_%d", i),
			TenantID:     h.task.TenantID,
			UserID:       h.task.UserID,
			TaskID:       h.task.TaskID,
			RunID:        h.run.RunID,
			ConnectedAt:  time.Now().UTC(),
			TTL:          time.Now().Add(2 * time.Hour).Unix(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	pusher := &concurrencyProbePusher{}
	eng := NewEngine(DefaultEngineConfig(), h.store, h.artifacts, nil, nil, pusher)

	var seq int64
	start := time.Now()
	eng.pushEvent(context.Background(), h.task.TenantID, h.task.TaskID, h.run.RunID, &seq, model.StreamEventTokenChunk, map[string]interface{}{
		"token": "x",
	})
	elapsed := time.Since(start)

	if got := pusher.calls.Load(); got != int64(connectionCount) {
		t.Fatalf("expected %d push calls, got %d", connectionCount, got)
	}

	interval := time.Second / time.Duration(streamPushRatePerSecond)
	expectedMin := time.Duration(extraConnections) * interval
	tolerance := expectedMin / 3
	if elapsed+tolerance < expectedMin {
		t.Fatalf("expected push dispatch to be rate-limited (elapsed=%s, expected_min~%s)", elapsed, expectedMin)
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

type multiToolLLM struct {
	calls int
}

func (l *multiToolLLM) Chat(_ context.Context, _ *LLMRequest) (*LLMResponse, error) {
	l.calls++
	if l.calls == 1 {
		return &LLMResponse{
			Content: "Running multiple file writes",
			ToolCalls: []ToolCall{
				{ID: "call_a", Name: "fs.write", Args: `{"path":"a.txt","content":"A"}`},
				{ID: "call_b", Name: "fs.write", Args: `{"path":"b.txt","content":"B"}`},
			},
			TokenUsage:   &model.TokenUsage{Input: 12, Output: 8, Total: 20},
			FinishReason: "tool_calls",
		}, nil
	}
	return &LLMResponse{
		Content:      "Done",
		TokenUsage:   &model.TokenUsage{Input: 4, Output: 2, Total: 6},
		FinishReason: "stop",
	}, nil
}

type fixedUsageLLM struct {
	response *LLMResponse
	err      error
}

func (l *fixedUsageLLM) Chat(_ context.Context, _ *LLMRequest) (*LLMResponse, error) {
	if l.err != nil {
		return nil, l.err
	}
	if l.response == nil {
		return &LLMResponse{
			Content:      "ok",
			FinishReason: "stop",
		}, nil
	}
	cp := *l.response
	if l.response.TokenUsage != nil {
		usage := *l.response.TokenUsage
		cp.TokenUsage = &usage
	}
	if len(l.response.ToolCalls) > 0 {
		cp.ToolCalls = append([]ToolCall(nil), l.response.ToolCalls...)
	}
	return &cp, nil
}
