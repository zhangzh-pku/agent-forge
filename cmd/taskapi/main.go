// Package main implements the Task API HTTP server.
// In local mode, it also starts an embedded worker for convenience.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/agentforge/agentforge/pkg/api"
	artstore "github.com/agentforge/agentforge/pkg/artifact"
	"github.com/agentforge/agentforge/pkg/engine"
	"github.com/agentforge/agentforge/pkg/queue"
	"github.com/agentforge/agentforge/pkg/state"
	"github.com/agentforge/agentforge/pkg/stream"
	"github.com/agentforge/agentforge/pkg/task"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Shared in-memory stores for local development.
	store := state.NewMemoryStore()
	artifacts := artstore.NewMemoryStore()
	q := queue.NewMemoryQueue(1000)
	pusher := stream.NewMockPusher()

	svc := task.NewService(store, q)
	handler := api.NewHandler(svc)
	wsHandler := api.NewWSHandler(store, svc)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	wsHandler.RegisterRoutes(mux)

	// Health check.
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status":"ok"}`)
	})

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Start embedded worker (shares stores with the API for local dev).
	llm := engine.NewMockLLMClient(3)
	registry := engine.NewRegistry()
	worker := engine.NewWorker(
		store, artifacts, q,
		llm, registry, pusher,
		engine.DefaultEngineConfig(),
	)
	go func() {
		log.Println("Embedded worker started (local dev mode)")
		if err := worker.Start(ctx); err != nil && err != context.Canceled {
			log.Printf("Worker error: %v\n", err)
		}
	}()

	// Graceful shutdown.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("shutting down...")
		cancel()
		srv.Shutdown(context.Background())
	}()

	log.Printf("AgentForge Task API listening on :%s (with embedded worker)\n", port)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
