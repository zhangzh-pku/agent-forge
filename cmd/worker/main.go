// Package main implements the SQS worker / local queue consumer.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	appcfg "github.com/agentforge/agentforge/internal/config"
	"github.com/agentforge/agentforge/internal/telemetry"
	artstore "github.com/agentforge/agentforge/pkg/artifact"
	"github.com/agentforge/agentforge/pkg/engine"
	"github.com/agentforge/agentforge/pkg/queue"
	"github.com/agentforge/agentforge/pkg/state"
	"github.com/agentforge/agentforge/pkg/stream"
	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	agwtypes "github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

func main() {
	telemetryCfg, err := appcfg.LoadTelemetryRuntimeConfigFromEnv("agentforge-worker")
	if err != nil {
		log.Fatalf("failed to load telemetry config: %v", err)
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetryCfg)
	if err != nil {
		log.Fatalf("failed to initialize telemetry: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(shutdownCtx); err != nil {
			log.Printf("telemetry shutdown error: %v", err)
		}
	}()

	store, artifacts, q, pusher, mode, err := initRuntime(context.Background())
	if err != nil {
		log.Fatalf("failed to initialize runtime dependencies: %v", err)
	}

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

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("shutting down worker...")
		cancel()
	}()

	log.Printf("AgentForge Worker starting (runtime=%s)...", mode)
	if err := worker.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func initRuntime(ctx context.Context) (state.Store, artstore.Store, queue.Queue, stream.Pusher, string, error) {
	mode := appcfg.RuntimeModeFromEnv()
	if mode != appcfg.RuntimeModeAWS {
		store := state.NewMemoryStore()
		artifacts := artstore.NewMemoryStore()
		q := queue.NewMemoryQueue(1000)
		pusher := stream.NewMockPusher()
		return store, artifacts, q, pusher, "local", nil
	}

	awsCfg, err := awscfg.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, nil, nil, nil, "aws", fmt.Errorf("load aws config: %w", err)
	}
	backendCfg, err := appcfg.LoadAWSRuntimeConfigFromEnv()
	if err != nil {
		return nil, nil, nil, nil, "aws", err
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
		return nil, nil, nil, nil, "aws", err
	}
	artifacts, err := artstore.NewS3Store(s3.NewFromConfig(awsCfg), artstore.S3StoreConfig{
		Bucket:         backendCfg.ArtifactsBucket,
		PresignExpires: backendCfg.ArtifactPresignExpires,
		SSEKMSKeyID:    backendCfg.ArtifactSSEKMSKeyARN,
	})
	if err != nil {
		return nil, nil, nil, nil, "aws", err
	}
	q, err := queue.NewSQSQueue(sqs.NewFromConfig(awsCfg), queue.SQSQueueConfig{
		QueueURL:          backendCfg.TaskQueueURL,
		WaitTimeSeconds:   backendCfg.SQSWaitTimeSeconds,
		VisibilityTimeout: backendCfg.SQSVisibilityTimeoutSeconds,
		MaxMessages:       backendCfg.SQSMaxMessages,
	})
	if err != nil {
		return nil, nil, nil, nil, "aws", err
	}

	pusher, err := buildWSPusher(awsCfg, backendCfg.WebSocketEndpoint)
	if err != nil {
		return nil, nil, nil, nil, "aws", err
	}

	return store, artifacts, q, pusher, "aws", nil
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
