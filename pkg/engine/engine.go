package engine

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
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
	cfg        Config
	store      state.Store
	artifacts  artifact.Store
	llm        LLMClient
	tools      ToolRegistry
	pusher     stream.Pusher
	memorySn   *memory.Snapshotter
	log        *util.Logger
	seqCounter atomic.Int64
	metrics    *Metrics
	tenantID   string // set during Execute for tenant-scoped operations
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
}

// Execute runs the ReAct loop for a given task and run.
func (e *Engine) Execute(ctx context.Context, task *model.Task, run *model.Run, ws workspace.Manager) (*RunResult, error) {
	log := e.log.With("task_id", task.TaskID).With("run_id", run.RunID).With("tenant_id", task.TenantID).With("trace_id", util.NewID("tr_"))

	e.tenantID = task.TenantID

	log.Info("engine: starting execution")
	e.metrics.TaskStarted()

	// Initialize memory from prompt or restored state.
	mem := &model.MemorySnapshot{
		RunID:     run.RunID,
		Messages:  []model.MemoryMessage{},
		ToolState: make(map[string]interface{}),
	}

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
			Role:    "system",
			Content: "You are an AI agent. Use the available tools to complete the task. When done, provide a final answer.",
		})
		mem.Messages = append(mem.Messages, model.MemoryMessage{
			Role:    "user",
			Content: task.Prompt,
		})
	}

	// ReAct loop.
	for stepIdx := startStep; stepIdx < startStep+e.cfg.MaxSteps; stepIdx++ {
		// Check abort before each step.
		currentTask, err := e.store.GetTask(ctx, task.TaskID)
		if err != nil {
			return nil, fmt.Errorf("engine: check abort: %w", err)
		}
		if currentTask.AbortRequested {
			log.Info("engine: abort requested, stopping")
			now := time.Now().UTC()
			abortStep := &model.Step{
				RunID:        run.RunID,
				StepIndex:    stepIdx,
				Type:         model.StepTypeFinal,
				Status:       model.StepStatusOK,
				Input:        "abort_requested",
				Output:       currentTask.AbortReason,
				TSStart:      now,
				TSEnd:        now,
				LatencyMS:    0,
				ErrorCode:    "ABORTED",
				ErrorMessage: currentTask.AbortReason,
			}
			if err := e.store.PutStep(ctx, abortStep); err != nil && !errors.Is(err, state.ErrConflict) {
				log.Error("engine: write abort step failed", map[string]interface{}{"error": err.Error()})
			}
			if err := e.store.UpdateLastStepIndex(ctx, task.TaskID, run.RunID, stepIdx); err != nil {
				log.Error("engine: update last step index failed", map[string]interface{}{"error": err.Error(), "step_index": stepIdx})
			}
			e.pushEvent(ctx, task.TaskID, run.RunID, model.StreamEventComplete, map[string]interface{}{
				"status": "aborted",
				"reason": currentTask.AbortReason,
			})
			e.metrics.TaskAborted()
			return &RunResult{
				Status:   model.RunStatusAborted,
				LastStep: stepIdx,
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
			}, nil
		default:
		}

		// Push step_start event.
		e.pushEvent(ctx, task.TaskID, run.RunID, model.StreamEventStepStart, map[string]interface{}{
			"step_index": stepIdx,
		})

		stepStart := time.Now().UTC()

		// LLM call.
		llmResp, err := e.llm.Chat(ctx, &LLMRequest{
			Messages:    mem.Messages,
			ModelConfig: run.ModelConfig,
		})
		if err != nil {
			e.writeErrorStep(ctx, run.RunID, stepIdx, stepStart, err)
			e.pushEvent(ctx, task.TaskID, run.RunID, model.StreamEventError, map[string]interface{}{
				"error_code": "LLM_ERROR",
				"message":    err.Error(),
			})
			e.metrics.TaskFailed()
			return &RunResult{
				Status:       model.RunStatusFailed,
				LastStep:     stepIdx,
				ErrorMessage: err.Error(),
			}, nil
		}

		// Append assistant message.
		mem.Messages = append(mem.Messages, model.MemoryMessage{
			Role:    "assistant",
			Content: llmResp.Content,
		})

		// Determine step type and execute tools if needed.
		stepType := model.StepTypeLLMCall
		var toolOutput string

		if len(llmResp.ToolCalls) > 0 {
			stepType = model.StepTypeToolCall
			for _, tc := range llmResp.ToolCalls {
				e.pushEvent(ctx, task.TaskID, run.RunID, model.StreamEventToolCall, map[string]interface{}{
					"tool": tc.Name, "args": tc.Args,
				})

				tool := e.tools.Get(tc.Name)
				if tool == nil {
					toolOutput = fmt.Sprintf("error: unknown tool %q", tc.Name)
				} else {
					result, err := tool.Execute(ctx, tc.Args)
					if err != nil {
						toolOutput = fmt.Sprintf("error: %v", err)
					} else if result.Error != "" {
						toolOutput = fmt.Sprintf("error: %s", result.Error)
					} else {
						toolOutput = result.Output
					}
				}

				e.pushEvent(ctx, task.TaskID, run.RunID, model.StreamEventToolResult, map[string]interface{}{
					"tool": tc.Name, "output": toolOutput,
				})

				// Append tool result to memory.
				mem.Messages = append(mem.Messages, model.MemoryMessage{
					Role:    "user",
					Content: fmt.Sprintf("[Tool %s result]: %s", tc.Name, toolOutput),
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
				}, nil
			}
			// ErrConflict means the step was already written (idempotent retry) — continue.
		}

		// Update run's last step index.
		if err := e.store.UpdateLastStepIndex(ctx, task.TaskID, run.RunID, stepIdx); err != nil {
			log.Error("engine: update last step index failed", map[string]interface{}{"error": err.Error(), "step_index": stepIdx})
		}

		// Push step_end event.
		e.pushEvent(ctx, task.TaskID, run.RunID, model.StreamEventStepEnd, map[string]interface{}{
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

			e.pushEvent(ctx, task.TaskID, run.RunID, model.StreamEventComplete, map[string]interface{}{
				"final_answer": llmResp.Content,
			})

			e.metrics.TaskSucceeded()
			return &RunResult{
				Status:   model.RunStatusSucceeded,
				LastStep: stepIdx + 1,
			}, nil
		}
	}

	// Max steps reached.
	e.pushEvent(ctx, task.TaskID, run.RunID, model.StreamEventError, map[string]interface{}{
		"error_code": "MAX_STEPS",
		"message":    "max steps reached",
	})
	e.metrics.TaskFailed()
	return &RunResult{
		Status:       model.RunStatusFailed,
		LastStep:     startStep + e.cfg.MaxSteps - 1,
		ErrorMessage: "max steps reached",
	}, nil
}

func (e *Engine) writeErrorStep(ctx context.Context, runID string, stepIdx int, start time.Time, stepErr error) {
	now := time.Now().UTC()
	step := &model.Step{
		RunID:        runID,
		StepIndex:    stepIdx,
		Type:         model.StepTypeLLMCall,
		Status:       model.StepStatusError,
		TSStart:      start,
		TSEnd:        now,
		LatencyMS:    now.Sub(start).Milliseconds(),
		ErrorCode:    "LLM_ERROR",
		ErrorMessage: stepErr.Error(),
	}
	if err := e.store.PutStep(ctx, step); err != nil {
		e.log.Error("engine: write error step failed", map[string]interface{}{"error": err.Error(), "step_index": stepIdx})
	}
}

func (e *Engine) pushEvent(ctx context.Context, taskID, runID string, eventType model.StreamEventType, data interface{}) {
	if e.pusher == nil {
		return
	}
	seq := e.seqCounter.Add(1)
	event := &model.StreamEvent{
		TaskID: taskID,
		RunID:  runID,
		Seq:    seq,
		TS:     time.Now().Unix(),
		Type:   eventType,
		Data:   data,
	}
	// Get connections for this task and push.
	conns, err := e.store.GetConnectionsByTask(ctx, taskID)
	if err != nil || len(conns) == 0 {
		return
	}
	for _, conn := range conns {
		// Tenant isolation: skip connections belonging to a different tenant.
		if e.tenantID != "" && conn.TenantID != e.tenantID {
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
