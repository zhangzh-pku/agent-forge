// Package main implements the Task API HTTP server.
// In local mode, it also starts an embedded worker for convenience.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
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

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())

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
		go func() {
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
		_ = srv.Shutdown(context.Background())
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
