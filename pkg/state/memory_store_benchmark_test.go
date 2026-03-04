package state

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
)

func BenchmarkMemoryStoreApplyCreateTransitionParallel(b *testing.B) {
	store := NewMemoryStore()
	ctx := context.Background()
	var seq uint64

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := atomic.AddUint64(&seq, 1)
			now := time.Now().UTC()
			taskID := fmt.Sprintf("task_bench_%d", id)
			runID := fmt.Sprintf("run_bench_%d", id)

			task := &model.Task{
				TaskID:      taskID,
				TenantID:    "tenant_bench",
				UserID:      "user_bench",
				Status:      model.TaskStatusQueued,
				ActiveRunID: runID,
				Prompt:      "benchmark",
				CreatedAt:   now,
				UpdatedAt:   now,
			}
			run := &model.Run{
				TaskID:   taskID,
				RunID:    runID,
				TenantID: "tenant_bench",
				Status:   model.RunStatusQueued,
			}
			if err := store.ApplyCreateTransition(ctx, task, run); err != nil {
				b.Fatalf("apply create transition failed: %v", err)
			}
		}
	})
}
