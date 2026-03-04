package stream

import (
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
)

func BenchmarkChunkerWrite(b *testing.B) {
	cfg := ChunkerConfig{
		FlushInterval: time.Second,
		FlushBytes:    64 * 1024,
	}
	c := NewChunker(cfg, func(_ []byte) error { return nil })
	defer c.Stop()

	ev := &model.StreamEvent{
		TaskID: "task_bench",
		RunID:  "run_bench",
		Type:   model.StreamEventTokenChunk,
		Data:   map[string]string{"text": "benchmark token chunk"},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ev.Seq = int64(i + 1)
		if err := c.Write(ev); err != nil {
			b.Fatalf("chunker write failed: %v", err)
		}
	}
}
