// Package main implements the Task API HTTP server.
// In local mode, it also starts an embedded worker for convenience.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/agentforge/agentforge/pkg/api"
	artstore "github.com/agentforge/agentforge/pkg/artifact"
	appcfg "github.com/agentforge/agentforge/pkg/config"
	"github.com/agentforge/agentforge/pkg/engine"
	"github.com/agentforge/agentforge/pkg/ops"
	"github.com/agentforge/agentforge/pkg/queue"
	"github.com/agentforge/agentforge/pkg/state"
	"github.com/agentforge/agentforge/pkg/stream"
	"github.com/agentforge/agentforge/pkg/task"
	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	agwtypes "github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type readinessChecker interface {
	HealthCheck(ctx context.Context) error
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	store, artifacts, q, pusher, embeddedWorker, mode, err := initRuntime(context.Background())
	if err != nil {
		log.Fatalf("failed to initialize runtime dependencies: %v", err)
	}

	svc := task.NewService(store, q)
	handler := api.NewHandler(svc)
	if provider, ok := q.(api.TenantRuntimeProvider); ok {
		handler.SetTenantRuntimeProvider(provider)
	}
	wsHandler := api.NewWSHandler(store, svc)
	wsHandler.SetReplayPusher(pusher)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	wsHandler.RegisterRoutes(mux)

	// Health check.
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprintln(w, `{"status":"ok"}`); err != nil {
			log.Printf("health write failed: %v", err)
		}
	})
	mux.HandleFunc("/health/ready", func(w http.ResponseWriter, _ *http.Request) {
		probeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		statusCode, resp := buildReadinessResponse(probeCtx, store, q)
		writeJSON(w, statusCode, resp)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		snap := api.SnapshotRequestMetrics()
		writePrometheusMetrics(w, snap)
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	var workerWg sync.WaitGroup

	recoveryCfg, err := appcfg.LoadRecoveryRuntimeConfigFromEnv()
	if err != nil {
		log.Fatalf("failed to load recovery config: %v", err)
	}
	if recoveryCfg.Enabled {
		recoveryScheduler := ops.NewScheduler(store, q, ops.SchedulerConfig{
			Interval:          recoveryCfg.Interval,
			StaleFor:          recoveryCfg.StaleFor,
			Limit:             recoveryCfg.Limit,
			TenantID:          recoveryCfg.TenantID,
			ConsistencyCheck:  recoveryCfg.ConsistencyCheck,
			ConsistencyRepair: recoveryCfg.ConsistencyRepair,
		})
		go func() {
			log.Printf(
				"Recovery scheduler enabled (interval=%s, stale_for=%s, limit=%d, tenant=%q, consistency_check=%t, consistency_repair=%t)",
				recoveryCfg.Interval,
				recoveryCfg.StaleFor,
				recoveryCfg.Limit,
				recoveryCfg.TenantID,
				recoveryCfg.ConsistencyCheck,
				recoveryCfg.ConsistencyRepair,
			)
			recoveryScheduler.Start(ctx)
		}()
	}

	if embeddedWorker {
		llm, err := engine.NewLLMClientFromEnv()
		if err != nil {
			log.Fatalf("failed to initialize LLM client: %v", err)
		}
		registry := engine.NewRegistry()
		worker := engine.NewWorker(
			store, artifacts, q,
			llm, registry, pusher,
			engine.DefaultEngineConfig(),
		)
		workerWg.Add(1)
		go func() {
			defer workerWg.Done()
			log.Println("Embedded worker started (local mode)")
			if err := worker.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("Worker error: %v\n", err)
			}
		}()
	}

	// Graceful shutdown.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("shutting down...")
		cancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("http shutdown error: %v", err)
		}

		workerDone := make(chan struct{})
		go func() {
			workerWg.Wait()
			close(workerDone)
		}()

		select {
		case <-workerDone:
		case <-shutdownCtx.Done():
			if embeddedWorker {
				log.Printf("worker drain timed out: %v", shutdownCtx.Err())
			}
		}
	}()

	if embeddedWorker {
		log.Printf("AgentForge Task API listening on :%s (runtime=%s, embedded worker enabled)\n", port, mode)
	} else {
		log.Printf("AgentForge Task API listening on :%s (runtime=%s)\n", port, mode)
	}
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func writeJSON(w http.ResponseWriter, code int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("json encode failed: %v", err)
	}
}

func writePrometheusMetrics(w http.ResponseWriter, snap api.RequestMetricsSnapshot) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	_, _ = fmt.Fprintf(w, "# HELP agentforge_http_requests_total Total HTTP requests processed by AuthMiddleware.\n")
	_, _ = fmt.Fprintf(w, "# TYPE agentforge_http_requests_total counter\n")
	_, _ = fmt.Fprintf(w, "agentforge_http_requests_total %d\n", snap.RequestsTotal)

	_, _ = fmt.Fprintf(w, "# HELP agentforge_http_request_latency_ms_total Cumulative request latency in milliseconds.\n")
	_, _ = fmt.Fprintf(w, "# TYPE agentforge_http_request_latency_ms_total counter\n")
	_, _ = fmt.Fprintf(w, "agentforge_http_request_latency_ms_total %d\n", snap.LatencyMSTotal)

	avgLatency := 0.0
	if snap.RequestsTotal > 0 {
		avgLatency = float64(snap.LatencyMSTotal) / float64(snap.RequestsTotal)
	}
	_, _ = fmt.Fprintf(w, "# HELP agentforge_http_request_latency_ms_avg Average request latency in milliseconds.\n")
	_, _ = fmt.Fprintf(w, "# TYPE agentforge_http_request_latency_ms_avg gauge\n")
	_, _ = fmt.Fprintf(w, "agentforge_http_request_latency_ms_avg %.6f\n", avgLatency)

	_, _ = fmt.Fprintf(w, "# HELP agentforge_http_responses_total Total HTTP responses by status class.\n")
	_, _ = fmt.Fprintf(w, "# TYPE agentforge_http_responses_total counter\n")
	_, _ = fmt.Fprintf(w, "agentforge_http_responses_total{code_class=\"1xx\"} %d\n", snap.Status1xx)
	_, _ = fmt.Fprintf(w, "agentforge_http_responses_total{code_class=\"2xx\"} %d\n", snap.Status2xx)
	_, _ = fmt.Fprintf(w, "agentforge_http_responses_total{code_class=\"3xx\"} %d\n", snap.Status3xx)
	_, _ = fmt.Fprintf(w, "agentforge_http_responses_total{code_class=\"4xx\"} %d\n", snap.Status4xx)
	_, _ = fmt.Fprintf(w, "agentforge_http_responses_total{code_class=\"5xx\"} %d\n", snap.Status5xx)

	_, _ = fmt.Fprintf(w, "# HELP agentforge_http_5xx_total Total HTTP 5xx responses.\n")
	_, _ = fmt.Fprintf(w, "# TYPE agentforge_http_5xx_total counter\n")
	_, _ = fmt.Fprintf(w, "agentforge_http_5xx_total %d\n", snap.Errors5xxTotal)
}

func buildReadinessResponse(ctx context.Context, store any, q any) (int, map[string]interface{}) {
	checks := map[string]string{
		"state": "ok",
		"queue": "ok",
	}
	errorsByCheck := map[string]string{}
	statusCode := http.StatusOK

	if checker, ok := store.(readinessChecker); ok {
		if err := checker.HealthCheck(ctx); err != nil {
			checks["state"] = "error"
			errorsByCheck["state"] = err.Error()
			statusCode = http.StatusServiceUnavailable
		}
	}

	if checker, ok := q.(readinessChecker); ok {
		if err := checker.HealthCheck(ctx); err != nil {
			checks["queue"] = "error"
			errorsByCheck["queue"] = err.Error()
			statusCode = http.StatusServiceUnavailable
		}
	}

	resp := map[string]interface{}{
		"status": "ready",
		"checks": checks,
	}
	if statusCode != http.StatusOK {
		resp["status"] = "not_ready"
		resp["errors"] = errorsByCheck
	}
	return statusCode, resp
}

func initRuntime(ctx context.Context) (state.Store, artstore.Store, queue.Queue, stream.Pusher, bool, string, error) {
	mode := appcfg.RuntimeModeFromEnv()
	if mode != appcfg.RuntimeModeAWS {
		store := state.NewMemoryStore()
		artifacts := artstore.NewMemoryStore()
		q := queue.NewMemoryQueue(1000)
		pusher := stream.NewMockPusher()
		return store, artifacts, q, pusher, true, "local", nil
	}

	awsCfg, err := awscfg.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, nil, nil, nil, false, "aws", fmt.Errorf("load aws config: %w", err)
	}
	backendCfg, err := appcfg.LoadAWSRuntimeConfigFromEnv()
	if err != nil {
		return nil, nil, nil, nil, false, "aws", err
	}

	store, err := state.NewDynamoStore(dynamodb.NewFromConfig(awsCfg), state.DynamoStoreConfig{
		TasksTable:       backendCfg.State.TasksTable,
		RunsTable:        backendCfg.State.RunsTable,
		StepsTable:       backendCfg.State.StepsTable,
		ConnectionsTable: backendCfg.State.ConnectionsTable,
		ConnectionIndex:  backendCfg.State.ConnectionIndex,
		EventRetention:   backendCfg.EventRetention,
	})
	if err != nil {
		return nil, nil, nil, nil, false, "aws", err
	}
	artifacts, err := artstore.NewS3Store(s3.NewFromConfig(awsCfg), artstore.S3StoreConfig{
		Bucket:         backendCfg.ArtifactsBucket,
		PresignExpires: backendCfg.ArtifactPresignExpires,
		SSEKMSKeyID:    backendCfg.ArtifactSSEKMSKeyARN,
	})
	if err != nil {
		return nil, nil, nil, nil, false, "aws", err
	}
	q, err := queue.NewSQSQueue(sqs.NewFromConfig(awsCfg), queue.SQSQueueConfig{
		QueueURL:          backendCfg.TaskQueueURL,
		WaitTimeSeconds:   backendCfg.SQSWaitTimeSeconds,
		VisibilityTimeout: backendCfg.SQSVisibilityTimeoutSeconds,
		MaxMessages:       backendCfg.SQSMaxMessages,
	})
	if err != nil {
		return nil, nil, nil, nil, false, "aws", err
	}

	pusher, err := buildWSPusher(awsCfg, backendCfg.WebSocketEndpoint)
	if err != nil {
		return nil, nil, nil, nil, false, "aws", err
	}

	return store, artifacts, q, pusher, false, "aws", nil
}

func buildWSPusher(cfg aws.Config, endpoint string) (stream.Pusher, error) {
	if endpoint == "" {
		return stream.NewMockPusher(), nil
	}

	client := apigatewaymanagementapi.NewFromConfig(cfg, func(opts *apigatewaymanagementapi.Options) {
		opts.BaseEndpoint = aws.String(endpoint)
	})

	post := func(ctx context.Context, connectionID string, data []byte) error {
		_, err := client.PostToConnection(ctx, &apigatewaymanagementapi.PostToConnectionInput{
			ConnectionId: aws.String(connectionID),
			Data:         data,
		})
		if err != nil {
			var goneErr *agwtypes.GoneException
			if errors.As(err, &goneErr) {
				return stream.ErrConnectionGone
			}
			return err
		}
		return nil
	}

	return stream.NewChunkedAWSPusher(endpoint, stream.DefaultChunkerConfig(), post), nil
}
