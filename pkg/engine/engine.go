package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/agentforge/agentforge/pkg/artifact"
	"github.com/agentforge/agentforge/pkg/memory"
	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/state"
	"github.com/agentforge/agentforge/pkg/stream"
	"github.com/agentforge/agentforge/pkg/util"
	"github.com/agentforge/agentforge/pkg/workspace"
)

// Config holds engine configuration.
type Config struct {
	MaxSteps int // Maximum steps per run (safety limit). Default 50.
}

// DefaultEngineConfig returns sensible defaults.
func DefaultEngineConfig() Config {
	return Config{MaxSteps: 50}
}

// Engine orchestrates the ReAct execution loop.
type Engine struct {
	cfg       Config
	store     state.Store
	artifacts artifact.Store
	llm       LLMClient
	tools     ToolRegistry
	pusher    stream.Pusher
	memorySn  *memory.Snapshotter
	log       *util.Logger
	metrics   *Metrics
}

// NewEngine creates a new execution engine.
func NewEngine(
	cfg Config,
	store state.Store,
	artifacts artifact.Store,
	llm LLMClient,
	tools ToolRegistry,
	pusher stream.Pusher,
) *Engine {
	return &Engine{
		cfg:       cfg,
		store:     store,
		artifacts: artifacts,
		llm:       llm,
		tools:     tools,
		pusher:    pusher,
		memorySn:  memory.NewSnapshotter(artifacts),
		log:       util.NewLogger(),
		metrics:   NewMetrics(),
	}
}

// RunResult holds the final outcome of an engine run.
type RunResult struct {
	Status       model.RunStatus
	LastStep     int
	ErrorMessage string
	TotalTokens  int
	TotalCostUSD float64
}

// Execute runs the ReAct loop for a given task and run.
func (e *Engine) Execute(ctx context.Context, task *model.Task, run *model.Run, ws workspace.Manager) (*RunResult, error) {
	log := e.log.With("task_id", task.TaskID).With("run_id", run.RunID).With("tenant_id", task.TenantID).With("trace_id", util.NewID("tr_"))

	log.Info("engine: starting execution")
	e.metrics.TaskStarted()
	var eventSeq int64

	// Initialize memory from prompt or restored state.
	mem := &model.MemorySnapshot{
		RunID:     run.RunID,
		Messages:  []model.MemoryMessage{},
		ToolState: make(map[string]interface{}),
	}
	taskType := classifyTaskPrompt(task.Prompt)
	var totalTokens int
	var totalCost float64

	startStep := 0

	// If resuming, restore from checkpoint.
	if run.ResumeFromStepIndex != nil {
		stepIdx := *run.ResumeFromStepIndex
		log.Info("engine: resuming from checkpoint", map[string]interface{}{"step_index": stepIdx})

		// Load step to get checkpoint ref.
		step, err := e.store.GetStep(ctx, run.ParentRunID, stepIdx)
		if err != nil {
			return nil, fmt.Errorf("engine: load checkpoint step: %w", err)
		}

		if step.CheckpointRef != nil {
			// Restore memory.
			if step.CheckpointRef.Memory != nil {
				restored, err := e.memorySn.Load(ctx, step.CheckpointRef.Memory)
				if err != nil {
					return nil, fmt.Errorf("engine: restore memory: %w", err)
				}
				mem = restored
				mem.RunID = run.RunID // Update to new run.
			}
			// Restore workspace.
			if step.CheckpointRef.Workspace != nil {
				if err := RestoreWorkspace(ctx, ws, e.artifacts, step.CheckpointRef.Workspace); err != nil {
					return nil, fmt.Errorf("engine: restore workspace: %w", err)
				}
			}
		}
		startStep = stepIdx + 1
	}

	// Add system prompt.
	if len(mem.Messages) == 0 {
		mem.Messages = append(mem.Messages, model.MemoryMessage{
			Role:    model.MessageRoleSystem,
			Content: "You are an AI agent. Use the available tools to complete the task. When done, provide a final answer.",
		})
		mem.Messages = append(mem.Messages, model.MemoryMessage{
			Role:    model.MessageRoleUser,
			Content: task.Prompt,
		})
	}

	// ReAct loop.
	for stepIdx := startStep; stepIdx < startStep+e.cfg.MaxSteps; stepIdx++ {
		// Check abort before each step.
		abortRequested, abortReason, err := e.store.IsAbortRequested(ctx, task.TaskID)
		if err != nil {
			return nil, fmt.Errorf("engine: check abort: %w", err)
		}
		if abortRequested {
			log.Info("engine: abort requested, stopping")
			now := time.Now().UTC()
			abortStep := &model.Step{
				RunID:        run.RunID,
				StepIndex:    stepIdx,
				Type:         model.StepTypeFinal,
				Status:       model.StepStatusOK,
				Input:        "abort_requested",
				Output:       abortReason,
				TSStart:      now,
				TSEnd:        now,
				LatencyMS:    0,
				ErrorCode:    "ABORTED",
				ErrorMessage: abortReason,
			}
			if err := e.store.PutStep(ctx, abortStep); err != nil && !errors.Is(err, state.ErrConflict) {
				log.Error("engine: write abort step failed", map[string]interface{}{"error": err.Error()})
			}
			if err := e.store.UpdateLastStepIndex(ctx, task.TaskID, run.RunID, stepIdx); err != nil {
				log.Error("engine: update last step index failed", map[string]interface{}{"error": err.Error(), "step_index": stepIdx})
			}
			e.pushEvent(ctx, task.TenantID, task.TaskID, run.RunID, &eventSeq, model.StreamEventComplete, map[string]interface{}{
				"status": "aborted",
				"reason": abortReason,
			})
			e.metrics.TaskAborted()
			return &RunResult{
				Status:       model.RunStatusAborted,
				LastStep:     stepIdx,
				TotalTokens:  totalTokens,
				TotalCostUSD: totalCost,
			}, nil
		}

		// Check context cancellation.
		select {
		case <-ctx.Done():
			e.metrics.TaskFailed()
			return &RunResult{
				Status:       model.RunStatusFailed,
				LastStep:     stepIdx - 1,
				ErrorMessage: "context cancelled",
				TotalTokens:  totalTokens,
				TotalCostUSD: totalCost,
			}, nil
		default:
		}

		// Push step_start event.
		e.pushEvent(ctx, task.TenantID, task.TaskID, run.RunID, &eventSeq, model.StreamEventStepStart, map[string]interface{}{
			"step_index": stepIdx,
		})

		stepStart := time.Now().UTC()

		// LLM call (streaming if supported).
		llmReq := &LLMRequest{
			Messages:    mem.Messages,
			ModelConfig: run.ModelConfig,
			Tools:       collectToolSpecs(e.tools),
			TaskType:    taskType,
		}
		var llmResp *LLMResponse
		var llmErr error
		if sllm, ok := e.llm.(StreamingLLMClient); ok {
			llmResp, llmErr = sllm.ChatStream(ctx, llmReq, func(token string) {
				if token == "" {
					return
				}
				e.pushEvent(ctx, task.TenantID, task.TaskID, run.RunID, &eventSeq, model.StreamEventTokenChunk, map[string]interface{}{
					"step_index": stepIdx,
					"token":      token,
				})
			})
		} else {
			llmResp, llmErr = e.llm.Chat(ctx, llmReq)
		}
		if llmErr != nil {
			e.writeErrorStep(ctx, run.RunID, stepIdx, stepStart, "LLM_ERROR", llmErr)
			e.pushEvent(ctx, task.TenantID, task.TaskID, run.RunID, &eventSeq, model.StreamEventError, map[string]interface{}{
				"error_code": "LLM_ERROR",
				"message":    llmErr.Error(),
			})
			e.metrics.TaskFailed()
			return &RunResult{
				Status:       model.RunStatusFailed,
				LastStep:     stepIdx,
				ErrorMessage: llmErr.Error(),
				TotalTokens:  totalTokens,
				TotalCostUSD: totalCost,
			}, nil
		}

		stepCost := EstimateUsageCostUSD(llmResp.ModelID, llmResp.TokenUsage)
		if llmResp.TokenUsage != nil {
			totalTokens += llmResp.TokenUsage.Total
		}
		totalCost += stepCost
		if err := e.store.AddRunUsage(ctx, task.TaskID, run.RunID, llmResp.TokenUsage, stepCost); err != nil {
			log.Error("engine: add run usage failed", map[string]interface{}{"error": err.Error()})
		}

		assistantToolCalls := make([]model.MemoryToolCall, 0, len(llmResp.ToolCalls))
		for idx, tc := range llmResp.ToolCalls {
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("call_%d_%d", stepIdx, idx)
				llmResp.ToolCalls[idx].ID = id
			}
			assistantToolCalls = append(assistantToolCalls, model.MemoryToolCall{
				ID:   id,
				Name: tc.Name,
				Args: tc.Args,
			})
		}

		// Append assistant message.
		mem.Messages = append(mem.Messages, model.MemoryMessage{
			Role:      model.MessageRoleAssistant,
			Content:   llmResp.Content,
			ToolCalls: assistantToolCalls,
		})

		// Determine step type and execute tools if needed.
		stepType := model.StepTypeLLMCall
		var toolOutput string

		if len(llmResp.ToolCalls) > 0 {
			stepType = model.StepTypeToolCall
			for _, tc := range llmResp.ToolCalls {
				e.pushEvent(ctx, task.TenantID, task.TaskID, run.RunID, &eventSeq, model.StreamEventToolCall, map[string]interface{}{
					"tool": tc.Name, "args": tc.Args,
				})

				tool := e.tools.Get(tc.Name)
				if tool == nil {
					toolOutput = fmt.Sprintf("error: unknown tool %q", tc.Name)
				} else {
					result, err := tool.Execute(ctx, tc.Args)
					if err != nil {
						e.writeErrorStep(ctx, run.RunID, stepIdx, stepStart, "TOOL_EXEC_ERROR", err)
						e.pushEvent(ctx, task.TenantID, task.TaskID, run.RunID, &eventSeq, model.StreamEventError, map[string]interface{}{
							"error_code": "TOOL_EXEC_ERROR",
							"tool":       tc.Name,
							"message":    err.Error(),
						})
						e.metrics.TaskFailed()
						return &RunResult{
							Status:       model.RunStatusFailed,
							LastStep:     stepIdx,
							ErrorMessage: fmt.Sprintf("tool %s execution failed: %v", tc.Name, err),
							TotalTokens:  totalTokens,
							TotalCostUSD: totalCost,
						}, nil
					} else if result.Error != "" {
						toolOutput = fmt.Sprintf("error: %s", result.Error)
					} else {
						toolOutput = result.Output
					}
				}

				e.pushEvent(ctx, task.TenantID, task.TaskID, run.RunID, &eventSeq, model.StreamEventToolResult, map[string]interface{}{
					"tool": tc.Name, "output": toolOutput,
				})

				// Append tool result to memory.
				mem.Messages = append(mem.Messages, model.MemoryMessage{
					Role:       model.MessageRoleTool,
					ToolCallID: tc.ID,
					Content:    toolOutput,
				})
			}
		}

		stepEnd := time.Now().UTC()
		latency := stepEnd.Sub(stepStart).Milliseconds()

		// Checkpoint: save memory + workspace.
		mem.StepIndex = stepIdx
		memRef, err := e.memorySn.Save(ctx, task.TenantID, task.TaskID, mem)
		if err != nil {
			log.Error("engine: save memory checkpoint failed", map[string]interface{}{"error": err.Error()})
			e.metrics.TaskFailed()
			return &RunResult{
				Status:       model.RunStatusFailed,
				LastStep:     stepIdx,
				ErrorMessage: fmt.Sprintf("memory checkpoint failed: %v", err),
				TotalTokens:  totalTokens,
				TotalCostUSD: totalCost,
			}, nil
		}

		wsRef, err := SnapshotWorkspace(ctx, ws, e.artifacts, task.TenantID, task.TaskID, run.RunID, stepIdx)
		if err != nil {
			log.Error("engine: save workspace checkpoint failed", map[string]interface{}{"error": err.Error()})
			e.metrics.TaskFailed()
			return &RunResult{
				Status:       model.RunStatusFailed,
				LastStep:     stepIdx,
				ErrorMessage: fmt.Sprintf("workspace checkpoint failed: %v", err),
				TotalTokens:  totalTokens,
				TotalCostUSD: totalCost,
			}, nil
		}

		checkpointRef := &model.CheckpointRef{
			Memory:    memRef,
			Workspace: wsRef,
		}

		// Write step metadata (idempotent).
		step := &model.Step{
			RunID:         run.RunID,
			StepIndex:     stepIdx,
			Type:          stepType,
			Status:        model.StepStatusOK,
			Input:         llmResp.Content,
			Output:        toolOutput,
			TSStart:       stepStart,
			TSEnd:         stepEnd,
			LatencyMS:     latency,
			TokenUsage:    llmResp.TokenUsage,
			CheckpointRef: checkpointRef,
		}
		if err := e.store.PutStep(ctx, step); err != nil {
			if !errors.Is(err, state.ErrConflict) {
				// Non-idempotent failure — step was not persisted.
				log.Error("engine: write step failed", map[string]interface{}{"error": err.Error()})
				e.metrics.TaskFailed()
				return &RunResult{
					Status:       model.RunStatusFailed,
					LastStep:     stepIdx,
					ErrorMessage: fmt.Sprintf("write step failed: %v", err),
					TotalTokens:  totalTokens,
					TotalCostUSD: totalCost,
				}, nil
			}
			// ErrConflict means the step was already written (idempotent retry) — continue.
		}

		// Update run's last step index.
		if err := e.store.UpdateLastStepIndex(ctx, task.TaskID, run.RunID, stepIdx); err != nil {
			log.Error("engine: update last step index failed", map[string]interface{}{"error": err.Error(), "step_index": stepIdx})
		}

		// Push step_end event.
		e.pushEvent(ctx, task.TenantID, task.TaskID, run.RunID, &eventSeq, model.StreamEventStepEnd, map[string]interface{}{
			"step_index": stepIdx,
			"type":       string(stepType),
			"latency_ms": latency,
		})

		e.metrics.StepLatency(latency)

		log.Info("engine: step completed", map[string]interface{}{
			"step_index": stepIdx,
			"type":       string(stepType),
			"latency_ms": latency,
		})

		// Check if this is a final answer (no tool calls and finish_reason == "stop").
		if len(llmResp.ToolCalls) == 0 && llmResp.FinishReason == "stop" {
			// Write final step with checkpoint so it can be used for resume.
			finalStep := &model.Step{
				RunID:         run.RunID,
				StepIndex:     stepIdx + 1,
				Type:          model.StepTypeFinal,
				Status:        model.StepStatusOK,
				Input:         llmResp.Content,
				TSStart:       stepEnd,
				TSEnd:         stepEnd,
				CheckpointRef: checkpointRef,
			}
			if err := e.store.PutStep(ctx, finalStep); err != nil {
				log.Error("engine: write final step failed", map[string]interface{}{"error": err.Error(), "step_index": stepIdx + 1})
			}
			if err := e.store.UpdateLastStepIndex(ctx, task.TaskID, run.RunID, stepIdx+1); err != nil {
				log.Error("engine: update last step index failed", map[string]interface{}{"error": err.Error(), "step_index": stepIdx + 1})
			}

			e.pushEvent(ctx, task.TenantID, task.TaskID, run.RunID, &eventSeq, model.StreamEventComplete, map[string]interface{}{
				"final_answer": llmResp.Content,
			})

			e.metrics.TaskSucceeded()
			return &RunResult{
				Status:       model.RunStatusSucceeded,
				LastStep:     stepIdx + 1,
				TotalTokens:  totalTokens,
				TotalCostUSD: totalCost,
			}, nil
		}
	}

	// Max steps reached.
	e.pushEvent(ctx, task.TenantID, task.TaskID, run.RunID, &eventSeq, model.StreamEventError, map[string]interface{}{
		"error_code": "MAX_STEPS",
		"message":    "max steps reached",
	})
	e.metrics.TaskFailed()
	return &RunResult{
		Status:       model.RunStatusFailed,
		LastStep:     startStep + e.cfg.MaxSteps - 1,
		ErrorMessage: "max steps reached",
		TotalTokens:  totalTokens,
		TotalCostUSD: totalCost,
	}, nil
}

func classifyTaskPrompt(prompt string) string {
	p := strings.ToLower(prompt)
	switch {
	case strings.Contains(p, "code"), strings.Contains(p, "bug"), strings.Contains(p, "test"), strings.Contains(p, "refactor"):
		return "coding"
	case strings.Contains(p, "analy"), strings.Contains(p, "report"), strings.Contains(p, "sql"), strings.Contains(p, "metric"):
		return "analysis"
	case strings.Contains(p, "summar"), strings.Contains(p, "rewrite"), strings.Contains(p, "translate"):
		return "writing"
	default:
		return "general"
	}
}

func collectToolSpecs(reg ToolRegistry) []ToolSpec {
	if reg == nil {
		return nil
	}
	names := reg.List()
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	specs := make([]ToolSpec, 0, len(names))
	for _, name := range names {
		t := reg.Get(name)
		if t == nil {
			continue
		}
		specs = append(specs, ToolSpec{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema(),
		})
	}
	if len(specs) == 0 {
		return nil
	}
	return specs
}

func (e *Engine) writeErrorStep(ctx context.Context, runID string, stepIdx int, start time.Time, errorCode string, stepErr error) {
	now := time.Now().UTC()
	if errorCode == "" {
		errorCode = "STEP_ERROR"
	}
	step := &model.Step{
		RunID:        runID,
		StepIndex:    stepIdx,
		Type:         model.StepTypeLLMCall,
		Status:       model.StepStatusError,
		TSStart:      start,
		TSEnd:        now,
		LatencyMS:    now.Sub(start).Milliseconds(),
		ErrorCode:    errorCode,
		ErrorMessage: stepErr.Error(),
	}
	if err := e.store.PutStep(ctx, step); err != nil {
		e.log.Error("engine: write error step failed", map[string]interface{}{"error": err.Error(), "step_index": stepIdx})
	}
}

func (e *Engine) pushEvent(ctx context.Context, tenantID, taskID, runID string, seqCounter *int64, eventType model.StreamEventType, data interface{}) {
	if e.pusher == nil {
		return
	}
	var seq int64 = 1
	if seqCounter != nil {
		*seqCounter = *seqCounter + 1
		seq = *seqCounter
	}
	event := &model.StreamEvent{
		TaskID: taskID,
		RunID:  runID,
		Seq:    seq,
		TS:     time.Now().Unix(),
		Type:   eventType,
		Data:   data,
	}
	if err := e.store.PutEvent(ctx, event); err != nil {
		e.log.Error("engine: persist stream event failed", map[string]interface{}{"error": err.Error(), "type": string(eventType)})
	}
	// Get connections for this task and push.
	conns, err := e.store.GetConnectionsByTask(ctx, taskID)
	if err != nil || len(conns) == 0 {
		return
	}
	for _, conn := range conns {
		// Tenant isolation: skip connections belonging to a different tenant.
		if tenantID != "" && conn.TenantID != tenantID {
			continue
		}
		alive, pushErr := e.pusher.Push(ctx, conn.ConnectionID, event)
		if pushErr != nil {
			e.metrics.StreamPushError()
		}
		if !alive {
			// Connection gone (410) — clean up.
			e.store.DeleteConnection(ctx, conn.ConnectionID)
		}
	}
}
