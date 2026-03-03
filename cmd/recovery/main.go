// Package main runs stale-run recovery and optional consistency repair.
package main

import (
	"context"
	"fmt"
	"log"
	"os/signal"
	"syscall"

	appcfg "github.com/agentforge/agentforge/pkg/config"
	"github.com/agentforge/agentforge/pkg/ops"
	"github.com/agentforge/agentforge/pkg/queue"
	"github.com/agentforge/agentforge/pkg/state"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, q, mode, err := initRuntime(ctx)
	if err != nil {
		log.Fatalf("failed to initialize runtime dependencies: %v", err)
	}

	recoveryCfg, err := appcfg.LoadRecoveryRuntimeConfigFromEnv()
	if err != nil {
		log.Fatalf("failed to load recovery config: %v", err)
	}

	// The dedicated recovery entrypoint always executes at least one pass.
	if !recoveryCfg.Enabled {
		log.Printf("recovery is disabled by AGENTFORGE_RECOVERY_ENABLED=false; running one-shot pass")
		recoveryCfg.Interval = 0
	}

	scheduler := ops.NewScheduler(store, q, ops.SchedulerConfig{
		Interval:          recoveryCfg.Interval,
		StaleFor:          recoveryCfg.StaleFor,
		Limit:             recoveryCfg.Limit,
		TenantID:          recoveryCfg.TenantID,
		ConsistencyCheck:  recoveryCfg.ConsistencyCheck,
		ConsistencyRepair: recoveryCfg.ConsistencyRepair,
	})

	log.Printf(
		"AgentForge Recovery starting (runtime=%s, interval=%s, stale_for=%s, limit=%d, tenant=%q, consistency_check=%t, consistency_repair=%t)",
		mode,
		recoveryCfg.Interval,
		recoveryCfg.StaleFor,
		recoveryCfg.Limit,
		recoveryCfg.TenantID,
		recoveryCfg.ConsistencyCheck,
		recoveryCfg.ConsistencyRepair,
	)
	scheduler.Start(ctx)
}

func initRuntime(ctx context.Context) (state.Store, queue.Queue, string, error) {
	mode := appcfg.RuntimeModeFromEnv()
	if mode != appcfg.RuntimeModeAWS {
		return state.NewMemoryStore(), queue.NewMemoryQueue(1000), "local", nil
	}

	awsCfg, err := awscfg.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, nil, "aws", fmt.Errorf("load aws config: %w", err)
	}
	backendCfg, err := appcfg.LoadAWSRuntimeConfigFromEnv()
	if err != nil {
		return nil, nil, "aws", err
	}

	store, err := state.NewDynamoStore(dynamodb.NewFromConfig(awsCfg), state.DynamoStoreConfig{
		TasksTable:       backendCfg.State.TasksTable,
		RunsTable:        backendCfg.State.RunsTable,
		StepsTable:       backendCfg.State.StepsTable,
		ConnectionsTable: backendCfg.State.ConnectionsTable,
		ConnectionIndex:  backendCfg.State.ConnectionIndex,
	})
	if err != nil {
		return nil, nil, "aws", err
	}
	q, err := queue.NewSQSQueue(sqs.NewFromConfig(awsCfg), queue.SQSQueueConfig{
		QueueURL:          backendCfg.TaskQueueURL,
		WaitTimeSeconds:   backendCfg.SQSWaitTimeSeconds,
		VisibilityTimeout: backendCfg.SQSVisibilityTimeoutSeconds,
		MaxMessages:       backendCfg.SQSMaxMessages,
	})
	if err != nil {
		return nil, nil, "aws", err
	}

	return store, q, "aws", nil
}
