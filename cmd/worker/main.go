// Package main implements the SQS worker / local queue consumer.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	artstore "github.com/agentforge/agentforge/pkg/artifact"
	"github.com/agentforge/agentforge/pkg/engine"
	"github.com/agentforge/agentforge/pkg/queue"
	"github.com/agentforge/agentforge/pkg/state"
	"github.com/agentforge/agentforge/pkg/stream"
)

func main() {
	store := state.NewMemoryStore()
	artifacts := artstore.NewMemoryStore()
	q := queue.NewMemoryQueue(1000)
	pusher := stream.NewMockPusher()
	llm := engine.NewMockLLMClient(3)

	registry := engine.NewRegistry()

	worker := engine.NewWorker(
		store, artifacts, q,
		llm, registry, pusher,
		engine.DefaultEngineConfig(),
	)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("shutting down worker...")
		cancel()
	}()

	log.Println("AgentForge Worker starting...")
	if err := worker.Start(ctx); err != nil && err != context.Canceled {
		log.Fatal(err)
	}
}
