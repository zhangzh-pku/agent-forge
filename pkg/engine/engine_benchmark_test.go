package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	artstore "github.com/agentforge/agentforge/pkg/artifact"
	"github.com/agentforge/agentforge/pkg/model"
	"github.com/agentforge/agentforge/pkg/state"
	"github.com/agentforge/agentforge/pkg/stream"
	"github.com/agentforge/agentforge/pkg/workspace"
)

func BenchmarkEngineExecute(b *testing.B) {
	ctx := context.Background()
	baseDir := b.TempDir()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store := state.NewMemoryStore()
		artifacts := artstore.NewMemoryStore()
		pusher := stream.NewMockPusher()

		taskID := fmt.Sprintf("task_bench_%d", i)
		runID := fmt.Sprintf("run_bench_%d", i)
		now := time.Now().UTC()

		task := &model.Task{
			TaskID:    taskID,
			TenantID:  "tenant_bench",
			UserID:    "user_bench",
			Status:    model.TaskStatusRunning,
			Prompt:    "write a benchmark output file",
			CreatedAt: now,
			UpdatedAt: now,
		}
		run := &model.Run{
			TaskID:   taskID,
			RunID:    runID,
			TenantID: "tenant_bench",
			Status:   model.RunStatusRunning,
		}
		if err := store.PutTask(ctx, task); err != nil {
			b.Fatalf("put task failed: %v", err)
		}
		if err := store.PutRun(ctx, run); err != nil {
			b.Fatalf("put run failed: %v", err)
		}

		ws, err := workspace.NewLocalManager(workspace.Config{
			Root:          filepath.Join(baseDir, fmt.Sprintf("ws_%d", i)),
			MaxTotalBytes: 50 * 1024 * 1024,
			MaxFileCount:  2000,
		})
		if err != nil {
			b.Fatalf("new workspace failed: %v", err)
		}

		registry := NewRegistry()
		for _, tool := range NewFSTools(ws) {
			registry.Register(tool)
		}
		engine := NewEngine(DefaultEngineConfig(), store, artifacts, NewMockLLMClient(1), registry, pusher)

		result, err := engine.Execute(ctx, task, run, ws)
		if err != nil {
			_ = ws.Cleanup()
			b.Fatalf("engine execute failed: %v", err)
		}
		if result.Status != model.RunStatusSucceeded {
			_ = ws.Cleanup()
			b.Fatalf("expected succeeded result, got %s", result.Status)
		}
		if err := ws.Cleanup(); err != nil {
			b.Fatalf("workspace cleanup failed: %v", err)
		}
	}
}
