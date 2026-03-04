package state

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestDynamoStoreIntegration_BasicLifecycleAndAtomicUsage(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("AGENTFORGE_INTEGRATION_AWS_ENDPOINT"))
	if endpoint == "" {
		t.Skip("set AGENTFORGE_INTEGRATION_AWS_ENDPOINT to run DynamoDB integration tests")
	}

	ctx := context.Background()
	client := integrationDynamoClient(t, endpoint)

	cfg := createIntegrationStateTables(t, ctx, client, fmt.Sprintf("af-it-%d", time.Now().UnixNano()))
	store, err := NewDynamoStore(client, cfg)
	if err != nil {
		t.Fatalf("new dynamo store: %v", err)
	}

	task := &model.Task{
		TaskID:      "task_it_1",
		TenantID:    "tnt_it",
		UserID:      "user_it",
		Status:      model.TaskStatusQueued,
		ActiveRunID: "run_it_1",
		Prompt:      "integration test prompt",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("put task: %v", err)
	}

	run := &model.Run{
		TaskID:   task.TaskID,
		RunID:    "run_it_1",
		TenantID: task.TenantID,
		Status:   model.RunStatusQueued,
	}
	if err := store.PutRun(ctx, run); err != nil {
		t.Fatalf("put run: %v", err)
	}

	// Additional rows to verify tenant/status listing uses indexed query semantics.
	otherTenantTask := &model.Task{
		TaskID:      "task_it_other_tenant",
		TenantID:    "tnt_other",
		UserID:      "user_it",
		Status:      model.TaskStatusQueued,
		ActiveRunID: "run_it_other_tenant",
		Prompt:      "other tenant",
		CreatedAt:   time.Now().UTC().Add(time.Second),
		UpdatedAt:   time.Now().UTC().Add(time.Second),
	}
	if err := store.PutTask(ctx, otherTenantTask); err != nil {
		t.Fatalf("put other-tenant task: %v", err)
	}
	if err := store.PutRun(ctx, &model.Run{
		TaskID:   otherTenantTask.TaskID,
		RunID:    "run_it_other_tenant",
		TenantID: otherTenantTask.TenantID,
		Status:   model.RunStatusQueued,
	}); err != nil {
		t.Fatalf("put other-tenant run: %v", err)
	}

	sameTenantFailedTask := &model.Task{
		TaskID:      "task_it_failed",
		TenantID:    task.TenantID,
		UserID:      "user_it",
		Status:      model.TaskStatusFailed,
		ActiveRunID: "run_it_failed",
		Prompt:      "failed task",
		CreatedAt:   time.Now().UTC().Add(2 * time.Second),
		UpdatedAt:   time.Now().UTC().Add(2 * time.Second),
	}
	if err := store.PutTask(ctx, sameTenantFailedTask); err != nil {
		t.Fatalf("put same-tenant failed task: %v", err)
	}
	if err := store.PutRun(ctx, &model.Run{
		TaskID:   sameTenantFailedTask.TaskID,
		RunID:    "run_it_failed",
		TenantID: sameTenantFailedTask.TenantID,
		Status:   model.RunStatusFailed,
	}); err != nil {
		t.Fatalf("put same-tenant failed run: %v", err)
	}

	gotTask, err := store.GetTask(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if gotTask.TaskID != task.TaskID || gotTask.TenantID != task.TenantID {
		t.Fatalf("unexpected task payload: %+v", gotTask)
	}

	queuedTasks, err := store.ListTasks(ctx, task.TenantID, []model.TaskStatus{model.TaskStatusQueued}, 10)
	if err != nil {
		t.Fatalf("list queued tasks for tenant: %v", err)
	}
	waitForDynamoIntegrationCondition(t, 5*time.Second, 100*time.Millisecond, func() (bool, error) {
		queuedTasks, err = store.ListTasks(ctx, task.TenantID, []model.TaskStatus{model.TaskStatusQueued}, 10)
		if err != nil {
			return false, err
		}
		return len(queuedTasks) == 1 && queuedTasks[0].TaskID == task.TaskID, nil
	}, "tenant queued task list did not converge")

	queuedRuns, err := store.ListRuns(ctx, task.TenantID, []model.RunStatus{model.RunStatusQueued}, 10)
	if err != nil {
		t.Fatalf("list queued runs for tenant: %v", err)
	}
	waitForDynamoIntegrationCondition(t, 5*time.Second, 100*time.Millisecond, func() (bool, error) {
		queuedRuns, err = store.ListRuns(ctx, task.TenantID, []model.RunStatus{model.RunStatusQueued}, 10)
		if err != nil {
			return false, err
		}
		return len(queuedRuns) == 1 && queuedRuns[0].RunID == run.RunID, nil
	}, "tenant queued run list did not converge")

	conn := &model.Connection{
		ConnectionID: "conn_it_1",
		TenantID:     task.TenantID,
		TaskID:       task.TaskID,
		RunID:        run.RunID,
		ConnectedAt:  time.Now().UTC(),
		TTL:          time.Now().Add(5 * time.Minute).Unix(),
	}
	if err := store.PutConnection(ctx, conn); err != nil {
		t.Fatalf("put connection: %v", err)
	}
	conns, err := store.GetConnectionsByTask(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("get connections by task: %v", err)
	}
	if len(conns) != 1 || conns[0].ConnectionID != conn.ConnectionID {
		t.Fatalf("unexpected connections: %+v", conns)
	}

	const writers = 24
	usagePerWrite := &model.TokenUsage{Input: 2, Output: 3, Total: 5}
	costPerWrite := 0.0015

	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.AddRunUsage(ctx, task.TaskID, run.RunID, usagePerWrite, costPerWrite); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("add run usage: %v", err)
		}
	}

	gotRun, err := store.GetRun(ctx, task.TaskID, run.RunID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if gotRun.TotalTokenUsage == nil {
		t.Fatal("expected total_token_usage to be set")
	}
	wantInput := writers * usagePerWrite.Input
	wantOutput := writers * usagePerWrite.Output
	wantTotal := writers * usagePerWrite.Total
	if gotRun.TotalTokenUsage.Input != wantInput ||
		gotRun.TotalTokenUsage.Output != wantOutput ||
		gotRun.TotalTokenUsage.Total != wantTotal {
		t.Fatalf("unexpected token usage: got=%+v want={input:%d output:%d total:%d}", gotRun.TotalTokenUsage, wantInput, wantOutput, wantTotal)
	}
	wantCost := float64(writers) * costPerWrite
	if diff := gotRun.TotalCostUSD - wantCost; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("unexpected total_cost_usd: got=%f want=%f", gotRun.TotalCostUSD, wantCost)
	}
}

func integrationDynamoClient(t *testing.T, endpoint string) *dynamodb.Client {
	t.Helper()
	cfg, err := awscfg.LoadDefaultConfig(
		context.Background(),
		awscfg.WithRegion("us-east-1"),
		awscfg.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}
	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = &endpoint
	})
}

func createIntegrationStateTables(t *testing.T, ctx context.Context, client *dynamodb.Client, prefix string) DynamoStoreConfig {
	t.Helper()
	cfg := DynamoStoreConfig{
		TasksTable:       prefix + "-tasks",
		RunsTable:        prefix + "-runs",
		StepsTable:       prefix + "-steps",
		ConnectionsTable: prefix + "-connections",
		ConnectionIndex:  "task-index",
		TaskTenantIndex:  defaultTaskTenantIndex,
		RunTenantIndex:   defaultRunTenantIndex,
	}
	createKVTableWithTenantIndex(t, ctx, client, cfg.TasksTable, cfg.TaskTenantIndex)
	createKVTableWithTenantIndex(t, ctx, client, cfg.RunsTable, cfg.RunTenantIndex)
	createKVTable(t, ctx, client, cfg.StepsTable)
	createConnectionsTableWithTaskIndex(t, ctx, client, cfg.ConnectionsTable, cfg.ConnectionIndex)
	return cfg
}

func waitForDynamoIntegrationCondition(
	t *testing.T,
	timeout time.Duration,
	interval time.Duration,
	check func() (bool, error),
	msg string,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		ok, err := check()
		if err != nil {
			lastErr = err
		} else if ok {
			return
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				t.Fatalf("%s: %v", msg, lastErr)
			}
			t.Fatalf("%s", msg)
		}
		time.Sleep(interval)
	}
}

func createKVTable(t *testing.T, ctx context.Context, client *dynamodb.Client, tableName string) {
	t.Helper()
	_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		AttributeDefinitions: []dbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: dbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: dbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []dbtypes.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: dbtypes.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: dbtypes.KeyTypeRange},
		},
		BillingMode: dbtypes.BillingModePayPerRequest,
	})
	if err != nil {
		t.Fatalf("create table %s: %v", tableName, err)
	}
	waiter := dynamodb.NewTableExistsWaiter(client)
	if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(tableName)}, 2*time.Minute); err != nil {
		t.Fatalf("wait for table %s: %v", tableName, err)
	}
}

func createKVTableWithTenantIndex(t *testing.T, ctx context.Context, client *dynamodb.Client, tableName, indexName string) {
	t.Helper()
	_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		AttributeDefinitions: []dbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: dbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: dbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsi1pk"), AttributeType: dbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsi1sk"), AttributeType: dbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []dbtypes.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: dbtypes.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: dbtypes.KeyTypeRange},
		},
		GlobalSecondaryIndexes: []dbtypes.GlobalSecondaryIndex{
			{
				IndexName: aws.String(indexName),
				KeySchema: []dbtypes.KeySchemaElement{
					{AttributeName: aws.String("gsi1pk"), KeyType: dbtypes.KeyTypeHash},
					{AttributeName: aws.String("gsi1sk"), KeyType: dbtypes.KeyTypeRange},
				},
				Projection: &dbtypes.Projection{ProjectionType: dbtypes.ProjectionTypeAll},
			},
		},
		BillingMode: dbtypes.BillingModePayPerRequest,
	})
	if err != nil {
		t.Fatalf("create table %s with tenant index: %v", tableName, err)
	}
	waiter := dynamodb.NewTableExistsWaiter(client)
	if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(tableName)}, 2*time.Minute); err != nil {
		t.Fatalf("wait for table %s: %v", tableName, err)
	}
	waitForDynamoIntegrationGSIActive(t, ctx, client, tableName, indexName, 2*time.Minute)
}

func createConnectionsTableWithTaskIndex(t *testing.T, ctx context.Context, client *dynamodb.Client, tableName, indexName string) {
	t.Helper()
	_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		AttributeDefinitions: []dbtypes.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: dbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: dbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsi1pk"), AttributeType: dbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("gsi1sk"), AttributeType: dbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []dbtypes.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: dbtypes.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: dbtypes.KeyTypeRange},
		},
		GlobalSecondaryIndexes: []dbtypes.GlobalSecondaryIndex{
			{
				IndexName: aws.String(indexName),
				KeySchema: []dbtypes.KeySchemaElement{
					{AttributeName: aws.String("gsi1pk"), KeyType: dbtypes.KeyTypeHash},
					{AttributeName: aws.String("gsi1sk"), KeyType: dbtypes.KeyTypeRange},
				},
				Projection: &dbtypes.Projection{ProjectionType: dbtypes.ProjectionTypeAll},
			},
		},
		BillingMode: dbtypes.BillingModePayPerRequest,
	})
	if err != nil {
		t.Fatalf("create table %s: %v", tableName, err)
	}
	waiter := dynamodb.NewTableExistsWaiter(client)
	if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(tableName)}, 2*time.Minute); err != nil {
		t.Fatalf("wait for table %s: %v", tableName, err)
	}
	waitForDynamoIntegrationGSIActive(t, ctx, client, tableName, indexName, 2*time.Minute)
}

func waitForDynamoIntegrationGSIActive(
	t *testing.T,
	ctx context.Context,
	client *dynamodb.Client,
	tableName string,
	indexName string,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		out, err := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
			TableName: aws.String(tableName),
		})
		if err != nil {
			if time.Now().After(deadline) {
				t.Fatalf("describe table %s: %v", tableName, err)
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if out.Table == nil {
			if time.Now().After(deadline) {
				t.Fatalf("describe table %s returned nil table", tableName)
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}
		for _, gsi := range out.Table.GlobalSecondaryIndexes {
			if aws.ToString(gsi.IndexName) == indexName && gsi.IndexStatus == dbtypes.IndexStatusActive {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("gsi %s on table %s did not become ACTIVE", indexName, tableName)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
