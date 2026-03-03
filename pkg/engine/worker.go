package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/agentforge/agentforge/pkg/artifact"
	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/queue"
	"github.com/agentforge/agentforge/pkg/state"
	"github.com/agentforge/agentforge/pkg/stream"
	"github.com/agentforge/agentforge/pkg/util"
	"github.com/agentforge/agentforge/pkg/workspace"
)

// Worker consumes messages from the queue and executes tasks.
type Worker struct {
	store     state.Store
	artifacts artifact.Store
	q         queue.Queue
	llm       LLMClient
	tools     ToolRegistry
	pusher    stream.Pusher
	engineCfg Config
	log       *util.Logger
}

// NewWorker creates a new task worker.
func NewWorker(
	store state.Store,
	artifacts artifact.Store,
	q queue.Queue,
	llm LLMClient,
	tools ToolRegistry,
	pusher stream.Pusher,
	engineCfg Config,
) *Worker {
	return &Worker{
		store:     store,
		artifacts: artifacts,
		q:         q,
		llm:       llm,
		tools:     tools,
		pusher:    pusher,
		engineCfg: engineCfg,
		log:       util.NewLogger().With("component", "worker"),
	}
}

// Start begins consuming queue messages. Blocks until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) error {
	return w.q.StartConsumer(ctx, w.handleMessage)
}

func (w *Worker) handleMessage(ctx context.Context, msg *model.SQSMessage) error {
	log := w.log.With("task_id", msg.TaskID).With("run_id", msg.RunID).With("tenant_id", msg.TenantID).With("trace_id", util.NewID("tr_"))
	log.Info("worker: received message")

	// Step 1: Claim the run (idempotent).
	if err := w.store.ClaimRun(ctx, msg.TaskID, msg.RunID); err != nil {
		if errors.Is(err, state.ErrConflict) || errors.Is(err, state.ErrNotFound) {
			log.Info("worker: claim failed (duplicate delivery or not found), skipping", map[string]interface{}{"error": err.Error()})
			return nil // Ack — don't reprocess.
		}
		// Unexpected DB error — return error so the message is retried.
		return fmt.Errorf("worker: claim run: %w", err)
	}

	// Update task status to RUNNING only when this message run is still active.
	if err := w.store.UpdateTaskStatusForRun(ctx, msg.TaskID, msg.RunID,
		[]model.TaskStatus{model.TaskStatusQueued, model.TaskStatusRunning},
		model.TaskStatusRunning); err != nil {
		if errors.Is(err, state.ErrConflict) || errors.Is(err, state.ErrNotFound) {
			log.Info("worker: task claim failed (run not active), skipping", map[string]interface{}{"error": err.Error()})
			if completeErr := w.store.CompleteRun(ctx, msg.TaskID, msg.RunID, model.RunStatusAborted); completeErr != nil {
				log.Error("worker: failed to complete stale run", map[string]interface{}{"error": completeErr.Error()})
			}
			return nil
		}
		if completeErr := w.store.CompleteRun(ctx, msg.TaskID, msg.RunID, model.RunStatusFailed); completeErr != nil {
			log.Error("worker: failed to complete run after status update failure", map[string]interface{}{"error": completeErr.Error()})
		}
		return fmt.Errorf("worker: update task status to RUNNING: %w", err)
	}

	// Step 2: Load task and run.
	task, err := w.store.GetTask(ctx, msg.TaskID)
	if err != nil {
		if completeErr := w.store.CompleteRun(ctx, msg.TaskID, msg.RunID, model.RunStatusFailed); completeErr != nil {
			log.Error("worker: failed to complete run after task load failure", map[string]interface{}{"error": completeErr.Error()})
		}
		return fmt.Errorf("worker: get task: %w", err)
	}
	if task.TenantID != msg.TenantID {
		log.Error("worker: tenant mismatch in message", map[string]interface{}{
			"task_tenant_id": task.TenantID,
			"msg_tenant_id":  msg.TenantID,
		})
		if completeErr := w.store.CompleteRun(ctx, msg.TaskID, msg.RunID, model.RunStatusFailed); completeErr != nil {
			log.Error("worker: failed to complete run on tenant mismatch", map[string]interface{}{"error": completeErr.Error()})
		}
		if updateErr := w.store.UpdateTaskStatusForRun(ctx, msg.TaskID, msg.RunID, []model.TaskStatus{model.TaskStatusRunning}, model.TaskStatusFailed); updateErr != nil {
			log.Error("worker: failed to update task status on tenant mismatch", map[string]interface{}{"error": updateErr.Error()})
		}
		return nil
	}
	run, err := w.store.GetRun(ctx, msg.TaskID, msg.RunID)
	if err != nil {
		if completeErr := w.store.CompleteRun(ctx, msg.TaskID, msg.RunID, model.RunStatusFailed); completeErr != nil {
			log.Error("worker: failed to complete run after run load failure", map[string]interface{}{"error": completeErr.Error()})
		}
		if updateErr := w.store.UpdateTaskStatusForRun(ctx, msg.TaskID, msg.RunID, []model.TaskStatus{model.TaskStatusRunning}, model.TaskStatusFailed); updateErr != nil {
			log.Error("worker: failed to update task status after run load failure", map[string]interface{}{"error": updateErr.Error()})
		}
		return fmt.Errorf("worker: get run: %w", err)
	}

	// Step 3: Create workspace.
	wsCfg := workspace.DefaultConfig(msg.RunID)
	ws, err := workspace.NewLocalManager(wsCfg)
	if err != nil {
		return fmt.Errorf("worker: create workspace: %w", err)
	}
	defer func() {
		if err := ws.Cleanup(); err != nil {
			log.Error("worker: workspace cleanup failed", map[string]interface{}{"error": err.Error()})
		}
	}()

	// Step 4: Register tools with workspace (including fs.export with artifact store).
	registry := NewRegistry()
	exportPrefix := fmt.Sprintf("exports/%s/%s/%s", msg.TenantID, msg.TaskID, msg.RunID)
	for _, tool := range NewFSToolsWithArtifactsPrefix(ws, w.artifacts, exportPrefix) {
		registry.Register(tool)
	}
	// Register caller-provided tools after built-ins so custom tools can override.
	if w.tools != nil {
		for _, name := range w.tools.List() {
			if tool := w.tools.Get(name); tool != nil {
				if registry.Get(name) != nil {
					log.Info("worker: overriding built-in tool", map[string]interface{}{"tool": name})
				}
				registry.Register(tool)
			}
		}
	}

	// Step 5: Run the engine.
	eng := NewEngine(w.engineCfg, w.store, w.artifacts, w.llm, registry, w.pusher)
	result, err := eng.Execute(ctx, task, run, ws)
	if err != nil {
		log.Error("worker: engine error", map[string]interface{}{"error": err.Error()})
		if completeErr := w.store.CompleteRun(ctx, msg.TaskID, msg.RunID, model.RunStatusFailed); completeErr != nil {
			log.Error("worker: failed to complete run after engine error", map[string]interface{}{"error": completeErr.Error()})
		}
		if updateErr := w.store.UpdateTaskStatusForRun(ctx, msg.TaskID, msg.RunID,
			[]model.TaskStatus{model.TaskStatusRunning},
			model.TaskStatusFailed); updateErr != nil {
			log.Error("worker: failed to update task status after engine error", map[string]interface{}{"error": updateErr.Error()})
		}
		return nil // Ack — don't retry engine failures.
	}

	// Step 6: Finalize.
	if completeErr := w.store.CompleteRun(ctx, msg.TaskID, msg.RunID, result.Status); completeErr != nil {
		log.Error("worker: failed to complete run", map[string]interface{}{"error": completeErr.Error()})
	}

	taskStatus := model.TaskStatusSucceeded
	switch result.Status {
	case model.RunStatusFailed:
		taskStatus = model.TaskStatusFailed
	case model.RunStatusAborted:
		taskStatus = model.TaskStatusAborted
	}
	if updateErr := w.store.UpdateTaskStatusForRun(ctx, msg.TaskID, msg.RunID,
		[]model.TaskStatus{model.TaskStatusRunning},
		taskStatus); updateErr != nil {
		log.Error("worker: failed to update task status on finalize", map[string]interface{}{"error": updateErr.Error()})
	}

	log.Info("worker: execution complete", map[string]interface{}{
		"status":    string(result.Status),
		"last_step": result.LastStep,
	})

	return nil
}
