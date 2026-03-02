package engine

import (
	"context"
	"fmt"

	"errors"

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

	// Update task status to RUNNING.
	if err := w.store.UpdateTaskStatus(ctx, msg.TaskID,
		[]model.TaskStatus{model.TaskStatusQueued, model.TaskStatusRunning},
		model.TaskStatusRunning); err != nil && !errors.Is(err, state.ErrConflict) {
		log.Error("worker: update task status to RUNNING failed", map[string]interface{}{"error": err.Error()})
	}

	// Step 2: Load task and run.
	task, err := w.store.GetTask(ctx, msg.TaskID)
	if err != nil {
		return fmt.Errorf("worker: get task: %w", err)
	}
	run, err := w.store.GetRun(ctx, msg.TaskID, msg.RunID)
	if err != nil {
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
	for _, tool := range NewFSToolsWithArtifacts(ws, w.artifacts) {
		registry.Register(tool)
	}

	// Step 5: Run the engine.
	eng := NewEngine(w.engineCfg, w.store, w.artifacts, w.llm, registry, w.pusher)
	result, err := eng.Execute(ctx, task, run, ws)
	if err != nil {
		log.Error("worker: engine error", map[string]interface{}{"error": err.Error()})
		w.store.CompleteRun(ctx, msg.TaskID, msg.RunID, model.RunStatusFailed)
		w.store.UpdateTaskStatus(ctx, msg.TaskID,
			[]model.TaskStatus{model.TaskStatusRunning},
			model.TaskStatusFailed)
		return nil // Ack — don't retry engine failures.
	}

	// Step 6: Finalize.
	w.store.CompleteRun(ctx, msg.TaskID, msg.RunID, result.Status)

	taskStatus := model.TaskStatusSucceeded
	switch result.Status {
	case model.RunStatusFailed:
		taskStatus = model.TaskStatusFailed
	case model.RunStatusAborted:
		taskStatus = model.TaskStatusAborted
	}
	w.store.UpdateTaskStatus(ctx, msg.TaskID,
		[]model.TaskStatus{model.TaskStatusRunning},
		taskStatus)

	log.Info("worker: execution complete", map[string]interface{}{
		"status":    string(result.Status),
		"last_step": result.LastStep,
	})

	return nil
}
