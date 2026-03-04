package state

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	defaultConnectionTaskIndex = "task-index"
	eventTTLAttribute          = "ttl"
	taskMetaSK                 = "META"
	connMetaSK                 = "META"
	maxBatchWriteSize          = 25
)

// DynamoStoreConfig defines table/index names for DynamoDB-backed state.
type DynamoStoreConfig struct {
	TasksTable       string
	RunsTable        string
	StepsTable       string
	ConnectionsTable string
	ConnectionIndex  string
	EventRetention   time.Duration
}

// DynamoStore is a production Store backed by DynamoDB.
type DynamoStore struct {
	client           *dynamodb.Client
	tasksTable       string
	runsTable        string
	stepsTable       string
	connectionsTable string
	connectionIndex  string
	eventRetention   time.Duration
}

// NewDynamoStore creates a new DynamoDB-backed state store.
func NewDynamoStore(client *dynamodb.Client, cfg DynamoStoreConfig) (*DynamoStore, error) {
	if client == nil {
		return nil, fmt.Errorf("state: dynamodb client is required")
	}
	if strings.TrimSpace(cfg.TasksTable) == "" {
		return nil, fmt.Errorf("state: tasks table is required")
	}
	if strings.TrimSpace(cfg.RunsTable) == "" {
		return nil, fmt.Errorf("state: runs table is required")
	}
	if strings.TrimSpace(cfg.StepsTable) == "" {
		return nil, fmt.Errorf("state: steps table is required")
	}
	if strings.TrimSpace(cfg.ConnectionsTable) == "" {
		return nil, fmt.Errorf("state: connections table is required")
	}
	if strings.TrimSpace(cfg.ConnectionIndex) == "" {
		cfg.ConnectionIndex = defaultConnectionTaskIndex
	}
	if cfg.EventRetention < 0 {
		return nil, fmt.Errorf("state: event retention must be >= 0")
	}

	return &DynamoStore{
		client:           client,
		tasksTable:       strings.TrimSpace(cfg.TasksTable),
		runsTable:        strings.TrimSpace(cfg.RunsTable),
		stepsTable:       strings.TrimSpace(cfg.StepsTable),
		connectionsTable: strings.TrimSpace(cfg.ConnectionsTable),
		connectionIndex:  strings.TrimSpace(cfg.ConnectionIndex),
		eventRetention:   cfg.EventRetention,
	}, nil
}

// --- TaskStore ---

func (s *DynamoStore) ApplyCreateTransition(ctx context.Context, task *model.Task, run *model.Run) error {
	if task == nil || run == nil || task.TaskID == "" || run.TaskID == "" || run.RunID == "" {
		return ErrConflict
	}
	if run.TaskID != task.TaskID {
		return ErrConflict
	}

	taskItem, err := attributevalue.MarshalMap(task)
	if err != nil {
		return fmt.Errorf("state: marshal create task: %w", err)
	}
	taskItem["pk"] = avString(taskPK(task.TaskID))
	taskItem["sk"] = avString(taskMetaSK)
	taskItem["entity"] = avString("task")

	runItem, err := attributevalue.MarshalMap(run)
	if err != nil {
		return fmt.Errorf("state: marshal create run: %w", err)
	}
	runItem["pk"] = avString(runPK(run.TaskID))
	runItem["sk"] = avString(runSK(run.RunID))
	runItem["entity"] = avString("run")

	transactItems := []dbtypes.TransactWriteItem{
		{Put: &dbtypes.Put{
			TableName:           aws.String(s.tasksTable),
			Item:                taskItem,
			ConditionExpression: aws.String("attribute_not_exists(pk) AND attribute_not_exists(sk)"),
		}},
		{Put: &dbtypes.Put{
			TableName:           aws.String(s.runsTable),
			Item:                runItem,
			ConditionExpression: aws.String("attribute_not_exists(pk) AND attribute_not_exists(sk)"),
		}},
	}

	if task.IdempotencyKey != "" {
		idCreatedAt := task.CreatedAt
		if idCreatedAt.IsZero() {
			idCreatedAt = time.Now().UTC()
		}
		idItem := map[string]dbtypes.AttributeValue{
			"pk":              avString(idempotencyPK(task.TenantID, task.IdempotencyKey)),
			"sk":              avString(taskMetaSK),
			"entity":          avString("idempotency"),
			"tenant_id":       avString(task.TenantID),
			"idempotency_key": avString(task.IdempotencyKey),
			"task_id":         avString(task.TaskID),
			"created_at":      avTime(idCreatedAt),
		}
		transactItems = append(transactItems, dbtypes.TransactWriteItem{Put: &dbtypes.Put{
			TableName:           aws.String(s.tasksTable),
			Item:                idItem,
			ConditionExpression: aws.String("attribute_not_exists(pk) AND attribute_not_exists(sk)"),
		}})
	}

	_, err = s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: transactItems,
	})
	if err != nil {
		if isConditionalCheckFailed(err) || isTransactionConditionFailed(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("state: apply create transition: %w", err)
	}
	return nil
}

func (s *DynamoStore) PutTask(ctx context.Context, task *model.Task) error {
	if task == nil || task.TaskID == "" {
		return fmt.Errorf("state: put task: invalid task")
	}

	item, err := attributevalue.MarshalMap(task)
	if err != nil {
		return fmt.Errorf("state: put task marshal: %w", err)
	}
	item["pk"] = avString(taskPK(task.TaskID))
	item["sk"] = avString(taskMetaSK)
	item["entity"] = avString("task")

	if task.IdempotencyKey == "" {
		_, err := s.client.PutItem(ctx, &dynamodb.PutItemInput{
			TableName:           aws.String(s.tasksTable),
			Item:                item,
			ConditionExpression: aws.String("attribute_not_exists(pk) AND attribute_not_exists(sk)"),
		})
		if err != nil {
			if isConditionalCheckFailed(err) {
				return ErrAlreadyExists
			}
			return fmt.Errorf("state: put task: %w", err)
		}
		return nil
	}

	idItem := map[string]dbtypes.AttributeValue{
		"pk":              avString(idempotencyPK(task.TenantID, task.IdempotencyKey)),
		"sk":              avString(taskMetaSK),
		"entity":          avString("idempotency"),
		"tenant_id":       avString(task.TenantID),
		"idempotency_key": avString(task.IdempotencyKey),
		"task_id":         avString(task.TaskID),
		"created_at":      avTime(time.Now().UTC()),
	}

	_, err = s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []dbtypes.TransactWriteItem{
			{Put: &dbtypes.Put{
				TableName:           aws.String(s.tasksTable),
				Item:                item,
				ConditionExpression: aws.String("attribute_not_exists(pk) AND attribute_not_exists(sk)"),
			}},
			{Put: &dbtypes.Put{
				TableName:           aws.String(s.tasksTable),
				Item:                idItem,
				ConditionExpression: aws.String("attribute_not_exists(pk) AND attribute_not_exists(sk)"),
			}},
		},
	})
	if err != nil {
		if isConditionalCheckFailed(err) || isTransactionConditionFailed(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("state: put task transact: %w", err)
	}
	return nil
}

func (s *DynamoStore) GetTask(ctx context.Context, taskID string) (*model.Task, error) {
	if taskID == "" {
		return nil, ErrNotFound
	}

	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.tasksTable),
		ConsistentRead: aws.Bool(true),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(taskPK(taskID)),
			"sk": avString(taskMetaSK),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("state: get task: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, ErrNotFound
	}

	var task model.Task
	if err := attributevalue.UnmarshalMap(out.Item, &task); err != nil {
		return nil, fmt.Errorf("state: unmarshal task: %w", err)
	}
	return &task, nil
}

func (s *DynamoStore) IsAbortRequested(ctx context.Context, taskID string) (bool, string, error) {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return false, "", err
	}
	return task.AbortRequested, task.AbortReason, nil
}

func (s *DynamoStore) UpdateTaskStatus(ctx context.Context, taskID string, from []model.TaskStatus, to model.TaskStatus) error {
	if len(from) == 0 {
		return ErrConflict
	}

	condition, names, values := buildStatusConditionTask(from)
	names["#status"] = "status"
	values[":to"] = avString(string(to))
	values[":updated"] = avTime(time.Now().UTC())

	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tasksTable),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(taskPK(taskID)),
			"sk": avString(taskMetaSK),
		},
		ConditionExpression:       aws.String("attribute_exists(pk) AND (" + condition + ")"),
		UpdateExpression:          aws.String("SET #status = :to, updated_at = :updated"),
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: values,
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return s.mapTaskConditionErr(ctx, taskID)
		}
		return fmt.Errorf("state: update task status: %w", err)
	}
	return nil
}

func (s *DynamoStore) UpdateTaskStatusForRun(ctx context.Context, taskID, runID string, from []model.TaskStatus, to model.TaskStatus) error {
	if len(from) == 0 {
		return ErrConflict
	}

	condition, names, values := buildStatusConditionTask(from)
	names["#status"] = "status"
	values[":run_id"] = avString(runID)
	values[":to"] = avString(string(to))
	values[":updated"] = avTime(time.Now().UTC())

	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tasksTable),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(taskPK(taskID)),
			"sk": avString(taskMetaSK),
		},
		ConditionExpression:       aws.String("attribute_exists(pk) AND active_run_id = :run_id AND (" + condition + ")"),
		UpdateExpression:          aws.String("SET #status = :to, updated_at = :updated"),
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: values,
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return s.mapTaskConditionErr(ctx, taskID)
		}
		return fmt.Errorf("state: update task status for run: %w", err)
	}
	return nil
}

func (s *DynamoStore) SetAbortRequested(ctx context.Context, taskID string, reason string) error {
	now := time.Now().UTC()
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tasksTable),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(taskPK(taskID)),
			"sk": avString(taskMetaSK),
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
		UpdateExpression:    aws.String("SET abort_requested = :req, abort_reason = :reason, abort_ts = :abort_ts, updated_at = :updated"),
		ExpressionAttributeValues: map[string]dbtypes.AttributeValue{
			":req":      avBool(true),
			":reason":   avString(reason),
			":abort_ts": avTime(now),
			":updated":  avTime(now),
		},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return ErrNotFound
		}
		return fmt.Errorf("state: set abort requested: %w", err)
	}
	return nil
}

func (s *DynamoStore) ClearAbortRequested(ctx context.Context, taskID string) error {
	now := time.Now().UTC()
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tasksTable),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(taskPK(taskID)),
			"sk": avString(taskMetaSK),
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
		UpdateExpression:    aws.String("SET abort_requested = :req, abort_reason = :reason, updated_at = :updated REMOVE abort_ts"),
		ExpressionAttributeValues: map[string]dbtypes.AttributeValue{
			":req":     avBool(false),
			":reason":  avString(""),
			":updated": avTime(now),
		},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return ErrNotFound
		}
		return fmt.Errorf("state: clear abort requested: %w", err)
	}
	return nil
}

func (s *DynamoStore) SetActiveRun(ctx context.Context, taskID string, runID string) error {
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tasksTable),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(taskPK(taskID)),
			"sk": avString(taskMetaSK),
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
		UpdateExpression:    aws.String("SET active_run_id = :run_id, updated_at = :updated"),
		ExpressionAttributeValues: map[string]dbtypes.AttributeValue{
			":run_id":  avString(runID),
			":updated": avTime(time.Now().UTC()),
		},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return ErrNotFound
		}
		return fmt.Errorf("state: set active run: %w", err)
	}
	return nil
}

func (s *DynamoStore) ApplyResumeTransition(ctx context.Context, taskID string, run *model.Run, from []model.TaskStatus, to model.TaskStatus) error {
	if run == nil || run.TaskID == "" || run.RunID == "" || run.TaskID != taskID {
		return ErrConflict
	}
	if len(from) == 0 {
		return ErrConflict
	}

	exists, err := s.taskExists(ctx, taskID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}

	runItem, err := attributevalue.MarshalMap(run)
	if err != nil {
		return fmt.Errorf("state: marshal resume run: %w", err)
	}
	runItem["pk"] = avString(runPK(run.TaskID))
	runItem["sk"] = avString(runSK(run.RunID))
	runItem["entity"] = avString("run")

	statusCond, statusNames, statusValues := buildStatusConditionTask(from)
	statusNames["#status"] = "status"
	statusValues[":run_id"] = avString(run.RunID)
	statusValues[":abort_requested"] = avBool(false)
	statusValues[":abort_reason"] = avString("")
	statusValues[":to"] = avString(string(to))
	statusValues[":updated"] = avTime(time.Now().UTC())

	_, err = s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []dbtypes.TransactWriteItem{
			{Put: &dbtypes.Put{
				TableName:           aws.String(s.runsTable),
				Item:                runItem,
				ConditionExpression: aws.String("attribute_not_exists(pk) AND attribute_not_exists(sk)"),
			}},
			{Update: &dbtypes.Update{
				TableName: aws.String(s.tasksTable),
				Key: map[string]dbtypes.AttributeValue{
					"pk": avString(taskPK(taskID)),
					"sk": avString(taskMetaSK),
				},
				ConditionExpression:       aws.String("attribute_exists(pk) AND (" + statusCond + ")"),
				UpdateExpression:          aws.String("SET active_run_id = :run_id, abort_requested = :abort_requested, abort_reason = :abort_reason, #status = :to, updated_at = :updated REMOVE abort_ts"),
				ExpressionAttributeNames:  statusNames,
				ExpressionAttributeValues: statusValues,
			}},
		},
	})
	if err != nil {
		if isRunInsertConflict(err) {
			return ErrAlreadyExists
		}
		if isTaskUpdateConflict(err) || isConditionalCheckFailed(err) || isTransactionConditionFailed(err) {
			exists, exErr := s.taskExists(ctx, taskID)
			if exErr != nil {
				return exErr
			}
			if !exists {
				return ErrNotFound
			}
			return ErrConflict
		}
		return fmt.Errorf("state: apply resume transition: %w", err)
	}
	return nil
}

func (s *DynamoStore) GetTaskByIdempotencyKey(ctx context.Context, tenantID, idempotencyKey string) (*model.Task, error) {
	if tenantID == "" || idempotencyKey == "" {
		return nil, ErrNotFound
	}

	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.tasksTable),
		ConsistentRead: aws.Bool(true),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(idempotencyPK(tenantID, idempotencyKey)),
			"sk": avString(taskMetaSK),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("state: get task by idempotency key: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, ErrNotFound
	}
	taskID, err := getStringAttr(out.Item, "task_id")
	if err != nil || taskID == "" {
		return nil, ErrNotFound
	}
	return s.GetTask(ctx, taskID)
}

func (s *DynamoStore) ListTasks(ctx context.Context, tenantID string, statuses []model.TaskStatus, limit int) ([]*model.Task, error) {
	filterExpr, names, values := buildTaskScanFilter(tenantID, statuses)

	var out []*model.Task
	var startKey map[string]dbtypes.AttributeValue
	for {
		res, err := s.client.Scan(ctx, &dynamodb.ScanInput{
			TableName:                 aws.String(s.tasksTable),
			ExclusiveStartKey:         startKey,
			FilterExpression:          aws.String(filterExpr),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: values,
		})
		if err != nil {
			return nil, fmt.Errorf("state: list tasks scan: %w", err)
		}

		for _, item := range res.Items {
			var task model.Task
			if err := attributevalue.UnmarshalMap(item, &task); err != nil {
				continue
			}
			out = append(out, &task)
		}

		if len(res.LastEvaluatedKey) == 0 {
			break
		}
		startKey = res.LastEvaluatedKey
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].TaskID < out[j].TaskID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// --- RunStore ---

func (s *DynamoStore) PutRun(ctx context.Context, run *model.Run) error {
	if run == nil || run.TaskID == "" || run.RunID == "" {
		return fmt.Errorf("state: put run: invalid run")
	}

	item, err := attributevalue.MarshalMap(run)
	if err != nil {
		return fmt.Errorf("state: put run marshal: %w", err)
	}
	item["pk"] = avString(runPK(run.TaskID))
	item["sk"] = avString(runSK(run.RunID))
	item["entity"] = avString("run")

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(s.runsTable),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(pk) AND attribute_not_exists(sk)"),
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("state: put run: %w", err)
	}
	return nil
}

func (s *DynamoStore) GetRun(ctx context.Context, taskID, runID string) (*model.Run, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.runsTable),
		ConsistentRead: aws.Bool(true),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(runPK(taskID)),
			"sk": avString(runSK(runID)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("state: get run: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, ErrNotFound
	}
	var run model.Run
	if err := attributevalue.UnmarshalMap(out.Item, &run); err != nil {
		return nil, fmt.Errorf("state: unmarshal run: %w", err)
	}
	return &run, nil
}

func (s *DynamoStore) ClaimRun(ctx context.Context, taskID, runID string) error {
	now := time.Now().UTC()
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.runsTable),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(runPK(taskID)),
			"sk": avString(runSK(runID)),
		},
		ConditionExpression: aws.String("attribute_exists(pk) AND #status = :queued"),
		UpdateExpression:    aws.String("SET #status = :running, started_at = :started_at"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]dbtypes.AttributeValue{
			":queued":     avString(string(model.RunStatusQueued)),
			":running":    avString(string(model.RunStatusRunning)),
			":started_at": avTime(now),
		},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			exists, exErr := s.runExists(ctx, taskID, runID)
			if exErr != nil {
				return exErr
			}
			if !exists {
				return ErrNotFound
			}
			return ErrConflict
		}
		return fmt.Errorf("state: claim run: %w", err)
	}
	return nil
}

func (s *DynamoStore) CompleteRun(ctx context.Context, taskID, runID string, status model.RunStatus) error {
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.runsTable),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(runPK(taskID)),
			"sk": avString(runSK(runID)),
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
		UpdateExpression:    aws.String("SET #status = :status, ended_at = :ended_at"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]dbtypes.AttributeValue{
			":status":   avString(string(status)),
			":ended_at": avTime(time.Now().UTC()),
		},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return ErrNotFound
		}
		return fmt.Errorf("state: complete run: %w", err)
	}
	return nil
}

func (s *DynamoStore) UpdateLastStepIndex(ctx context.Context, taskID, runID string, stepIndex int) error {
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.runsTable),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(runPK(taskID)),
			"sk": avString(runSK(runID)),
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
		UpdateExpression:    aws.String("SET last_step_index = :last_step_index"),
		ExpressionAttributeValues: map[string]dbtypes.AttributeValue{
			":last_step_index": avInt(stepIndex),
		},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return ErrNotFound
		}
		return fmt.Errorf("state: update last step index: %w", err)
	}
	return nil
}

func (s *DynamoStore) AddRunUsage(ctx context.Context, taskID, runID string, usage *model.TokenUsage, costUSD float64) error {
	if usage == nil && costUSD <= 0 {
		return nil
	}

	setParts := make([]string, 0, 4)
	values := map[string]dbtypes.AttributeValue{
		":zero": avInt(0),
	}
	names := map[string]string{}
	if usage != nil {
		names["#usage"] = "total_token_usage"
		names["#input"] = "input"
		names["#output"] = "output"
		names["#total"] = "total"
		values[":in_delta"] = avInt(usage.Input)
		values[":out_delta"] = avInt(usage.Output)
		values[":total_delta"] = avInt(usage.Total)
		setParts = append(setParts,
			"#usage.#input = if_not_exists(#usage.#input, :zero) + :in_delta",
			"#usage.#output = if_not_exists(#usage.#output, :zero) + :out_delta",
			"#usage.#total = if_not_exists(#usage.#total, :zero) + :total_delta",
		)
	}
	if costUSD > 0 {
		values[":zero_cost"] = avFloat(0)
		values[":cost_delta"] = avFloat(costUSD)
		setParts = append(setParts, "total_cost_usd = if_not_exists(total_cost_usd, :zero_cost) + :cost_delta")
	}
	if len(setParts) == 0 {
		return nil
	}
	var exprNames map[string]string
	if len(names) > 0 {
		exprNames = names
	}

	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.runsTable),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(runPK(taskID)),
			"sk": avString(runSK(runID)),
		},
		ConditionExpression:       aws.String("attribute_exists(pk)"),
		UpdateExpression:          aws.String("SET " + strings.Join(setParts, ", ")),
		ExpressionAttributeNames:  exprNames,
		ExpressionAttributeValues: values,
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return ErrNotFound
		}
		return fmt.Errorf("state: add run usage: %w", err)
	}
	return nil
}

func (s *DynamoStore) ResetRunToQueued(ctx context.Context, taskID, runID string) error {
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.runsTable),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(runPK(taskID)),
			"sk": avString(runSK(runID)),
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
		UpdateExpression:    aws.String("SET #status = :queued REMOVE started_at, ended_at"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]dbtypes.AttributeValue{
			":queued": avString(string(model.RunStatusQueued)),
		},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return ErrNotFound
		}
		return fmt.Errorf("state: reset run to queued: %w", err)
	}
	return nil
}

func (s *DynamoStore) ListRuns(ctx context.Context, tenantID string, statuses []model.RunStatus, limit int) ([]*model.Run, error) {
	filterExpr, names, values := buildRunScanFilter(tenantID, statuses)

	var out []*model.Run
	var startKey map[string]dbtypes.AttributeValue
	for {
		res, err := s.client.Scan(ctx, &dynamodb.ScanInput{
			TableName:                 aws.String(s.runsTable),
			ExclusiveStartKey:         startKey,
			FilterExpression:          aws.String(filterExpr),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: values,
		})
		if err != nil {
			return nil, fmt.Errorf("state: list runs scan: %w", err)
		}

		for _, item := range res.Items {
			var run model.Run
			if err := attributevalue.UnmarshalMap(item, &run); err != nil {
				continue
			}
			out = append(out, &run)
		}

		if len(res.LastEvaluatedKey) == 0 {
			break
		}
		startKey = res.LastEvaluatedKey
	}

	sort.Slice(out, func(i, j int) bool {
		iStart := time.Time{}
		jStart := time.Time{}
		if out[i].StartedAt != nil {
			iStart = *out[i].StartedAt
		}
		if out[j].StartedAt != nil {
			jStart = *out[j].StartedAt
		}
		if iStart.Equal(jStart) {
			iKey := out[i].TaskID + "#" + out[i].RunID
			jKey := out[j].TaskID + "#" + out[j].RunID
			return iKey < jKey
		}
		return iStart.Before(jStart)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// --- StepStore ---

func (s *DynamoStore) PutStep(ctx context.Context, step *model.Step) error {
	if step == nil || step.RunID == "" {
		return fmt.Errorf("state: put step: invalid step")
	}

	item, err := attributevalue.MarshalMap(step)
	if err != nil {
		return fmt.Errorf("state: marshal step: %w", err)
	}
	item["pk"] = avString(stepPK(step.RunID))
	item["sk"] = avString(stepSK(step.StepIndex))
	item["entity"] = avString("step")

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(s.stepsTable),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(pk) AND attribute_not_exists(sk)"),
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return ErrConflict
		}
		return fmt.Errorf("state: put step: %w", err)
	}
	return nil
}

func (s *DynamoStore) GetStep(ctx context.Context, runID string, stepIndex int) (*model.Step, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.stepsTable),
		ConsistentRead: aws.Bool(true),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(stepPK(runID)),
			"sk": avString(stepSK(stepIndex)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("state: get step: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, ErrNotFound
	}

	var step model.Step
	if err := attributevalue.UnmarshalMap(out.Item, &step); err != nil {
		return nil, fmt.Errorf("state: unmarshal step: %w", err)
	}
	return &step, nil
}

func (s *DynamoStore) ListSteps(ctx context.Context, runID string, from, limit int) ([]*model.Step, error) {
	start := stepSK(from)
	end := stepSK(99999999)

	var out []*model.Step
	var startKey map[string]dbtypes.AttributeValue
	for {
		input := &dynamodb.QueryInput{
			TableName:              aws.String(s.stepsTable),
			KeyConditionExpression: aws.String("pk = :pk AND sk BETWEEN :start AND :end"),
			ExpressionAttributeValues: map[string]dbtypes.AttributeValue{
				":pk":    avString(stepPK(runID)),
				":start": avString(start),
				":end":   avString(end),
			},
			ExclusiveStartKey: startKey,
			ScanIndexForward:  aws.Bool(true),
		}
		if limit > 0 {
			remaining := int32(limit - len(out))
			if remaining <= 0 {
				break
			}
			input.Limit = aws.Int32(remaining)
		}

		res, err := s.client.Query(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("state: list steps query: %w", err)
		}

		for _, item := range res.Items {
			var step model.Step
			if err := attributevalue.UnmarshalMap(item, &step); err != nil {
				continue
			}
			out = append(out, &step)
			if limit > 0 && len(out) >= limit {
				return out, nil
			}
		}

		if len(res.LastEvaluatedKey) == 0 {
			break
		}
		startKey = res.LastEvaluatedKey
	}
	return out, nil
}

// --- ConnectionStore ---

func (s *DynamoStore) PutConnection(ctx context.Context, conn *model.Connection) error {
	if conn == nil || conn.ConnectionID == "" {
		return fmt.Errorf("state: put connection: invalid connection")
	}
	item, err := attributevalue.MarshalMap(conn)
	if err != nil {
		return fmt.Errorf("state: marshal connection: %w", err)
	}
	item["pk"] = avString(connectionPK(conn.ConnectionID))
	item["sk"] = avString(connMetaSK)
	item["entity"] = avString("connection")
	item["gsi1pk"] = avString(taskGSIKey(conn.TaskID))
	item["gsi1sk"] = avString(connectionGSIKey(conn.ConnectionID))

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.connectionsTable),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("state: put connection: %w", err)
	}
	return nil
}

func (s *DynamoStore) GetConnection(ctx context.Context, connectionID string) (*model.Connection, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.connectionsTable),
		ConsistentRead: aws.Bool(true),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(connectionPK(connectionID)),
			"sk": avString(connMetaSK),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("state: get connection: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, ErrNotFound
	}
	var conn model.Connection
	if err := attributevalue.UnmarshalMap(out.Item, &conn); err != nil {
		return nil, fmt.Errorf("state: unmarshal connection: %w", err)
	}
	return &conn, nil
}

func (s *DynamoStore) DeleteConnection(ctx context.Context, connectionID string) error {
	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.connectionsTable),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(connectionPK(connectionID)),
			"sk": avString(connMetaSK),
		},
	})
	if err != nil {
		return fmt.Errorf("state: delete connection: %w", err)
	}
	return nil
}

func (s *DynamoStore) GetConnectionsByTask(ctx context.Context, taskID string) ([]*model.Connection, error) {
	if taskID == "" {
		return nil, nil
	}

	var out []*model.Connection
	var startKey map[string]dbtypes.AttributeValue
	for {
		res, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.connectionsTable),
			IndexName:              aws.String(s.connectionIndex),
			KeyConditionExpression: aws.String("gsi1pk = :taskpk"),
			ExpressionAttributeValues: map[string]dbtypes.AttributeValue{
				":taskpk": avString(taskGSIKey(taskID)),
			},
			ExclusiveStartKey: startKey,
		})
		if err != nil {
			return nil, fmt.Errorf("state: query connections by task: %w", err)
		}

		for _, item := range res.Items {
			var conn model.Connection
			if err := attributevalue.UnmarshalMap(item, &conn); err != nil {
				continue
			}
			out = append(out, &conn)
		}

		if len(res.LastEvaluatedKey) == 0 {
			break
		}
		startKey = res.LastEvaluatedKey
	}
	return out, nil
}

// --- EventStore ---

func (s *DynamoStore) PutEvent(ctx context.Context, event *model.StreamEvent) error {
	if event == nil {
		return nil
	}
	if event.RunID == "" {
		return ErrConflict
	}
	cp := *event
	if cp.TS == 0 {
		cp.TS = time.Now().Unix()
	}

	item, err := attributevalue.MarshalMap(cp)
	if err != nil {
		return fmt.Errorf("state: marshal event: %w", err)
	}
	item["pk"] = avString(stepPK(cp.RunID))
	item["sk"] = avString(eventSK(cp.Seq))
	item["entity"] = avString("event")
	if s.eventRetention > 0 {
		expiresAt := cp.TS + int64(s.eventRetention/time.Second)
		item[eventTTLAttribute] = &dbtypes.AttributeValueMemberN{Value: strconv.FormatInt(expiresAt, 10)}
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(s.stepsTable),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(pk) AND attribute_not_exists(sk)"),
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			// Idempotent append: duplicate sequence for run is ignored.
			return nil
		}
		return fmt.Errorf("state: put event: %w", err)
	}
	return nil
}

func (s *DynamoStore) ReplayEvents(ctx context.Context, taskID, runID string, fromSeq, fromTS int64, limit int) ([]*model.StreamEvent, error) {
	if runID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}

	start := int64(0)
	if fromSeq > 0 {
		start = fromSeq + 1
	}

	var out []*model.StreamEvent
	var retentionCutoff int64
	if s.eventRetention > 0 {
		retentionCutoff = time.Now().Add(-s.eventRetention).Unix()
	}
	var startKey map[string]dbtypes.AttributeValue
	for {
		queryLimit := int32(limit - len(out))
		if queryLimit <= 0 {
			break
		}
		if queryLimit < 50 {
			queryLimit = 50
		}
		res, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.stepsTable),
			KeyConditionExpression: aws.String("pk = :pk AND sk BETWEEN :start AND :end"),
			ExpressionAttributeValues: map[string]dbtypes.AttributeValue{
				":pk":    avString(stepPK(runID)),
				":start": avString(eventSK(start)),
				":end":   avString(eventSK(math.MaxInt64)),
			},
			ExclusiveStartKey: startKey,
			ScanIndexForward:  aws.Bool(true),
			Limit:             aws.Int32(queryLimit),
		})
		if err != nil {
			return nil, fmt.Errorf("state: replay events query: %w", err)
		}

		for _, item := range res.Items {
			var ev model.StreamEvent
			if err := attributevalue.UnmarshalMap(item, &ev); err != nil {
				continue
			}
			if taskID != "" && ev.TaskID != taskID {
				continue
			}
			if retentionCutoff > 0 && ev.TS < retentionCutoff {
				continue
			}
			if fromTS > 0 && ev.TS < fromTS {
				continue
			}
			out = append(out, &ev)
			if len(out) >= limit {
				return out, nil
			}
		}

		if len(res.LastEvaluatedKey) == 0 {
			break
		}
		startKey = res.LastEvaluatedKey
	}
	return out, nil
}

func (s *DynamoStore) CompactEvents(ctx context.Context, taskID, runID string, beforeTS int64) (int, error) {
	if beforeTS <= 0 || runID == "" {
		return 0, nil
	}

	type deleteKey struct {
		pk dbtypes.AttributeValue
		sk dbtypes.AttributeValue
	}
	toDelete := make([]deleteKey, 0)

	var startKey map[string]dbtypes.AttributeValue
	for {
		res, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.stepsTable),
			KeyConditionExpression: aws.String("pk = :pk AND sk BETWEEN :start AND :end"),
			ExpressionAttributeValues: map[string]dbtypes.AttributeValue{
				":pk":    avString(stepPK(runID)),
				":start": avString(eventSK(0)),
				":end":   avString(eventSK(math.MaxInt64)),
			},
			ExclusiveStartKey: startKey,
			ScanIndexForward:  aws.Bool(true),
		})
		if err != nil {
			return 0, fmt.Errorf("state: compact events query: %w", err)
		}

		for _, item := range res.Items {
			var ev model.StreamEvent
			if err := attributevalue.UnmarshalMap(item, &ev); err != nil {
				continue
			}
			if taskID != "" && ev.TaskID != taskID {
				continue
			}
			if ev.TS < beforeTS {
				pk := item["pk"]
				sk := item["sk"]
				if pk != nil && sk != nil {
					toDelete = append(toDelete, deleteKey{pk: pk, sk: sk})
				}
			}
		}

		if len(res.LastEvaluatedKey) == 0 {
			break
		}
		startKey = res.LastEvaluatedKey
	}

	if len(toDelete) == 0 {
		return 0, nil
	}

	removed := 0
	for i := 0; i < len(toDelete); i += maxBatchWriteSize {
		end := i + maxBatchWriteSize
		if end > len(toDelete) {
			end = len(toDelete)
		}

		writes := make([]dbtypes.WriteRequest, 0, end-i)
		for _, k := range toDelete[i:end] {
			writes = append(writes, dbtypes.WriteRequest{
				DeleteRequest: &dbtypes.DeleteRequest{Key: map[string]dbtypes.AttributeValue{
					"pk": k.pk,
					"sk": k.sk,
				}},
			})
		}

		res, err := s.client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
			RequestItems: map[string][]dbtypes.WriteRequest{
				s.stepsTable: writes,
			},
		})
		if err != nil {
			return removed, fmt.Errorf("state: compact events batch delete: %w", err)
		}
		if len(res.UnprocessedItems) > 0 {
			return removed, fmt.Errorf("state: compact events batch delete returned unprocessed items")
		}
		removed += len(writes)
	}

	return removed, nil
}

// --- helpers ---

func taskPK(taskID string) string {
	return "TASK#" + taskID
}

func runPK(taskID string) string {
	return "TASK#" + taskID
}

func runSK(runID string) string {
	return "RUN#" + runID
}

func stepPK(runID string) string {
	return "RUN#" + runID
}

func stepSK(stepIndex int) string {
	return fmt.Sprintf("STEP#%08d", stepIndex)
}

func eventSK(seq int64) string {
	if seq < 0 {
		seq = 0
	}
	return fmt.Sprintf("EVT#%020d", seq)
}

func connectionPK(connectionID string) string {
	return "CONN#" + connectionID
}

func taskGSIKey(taskID string) string {
	return "TASK#" + taskID
}

func connectionGSIKey(connectionID string) string {
	return "CONN#" + connectionID
}

func idempotencyPK(tenantID, idempotencyKey string) string {
	return "IDEMP#" + tenantID + "#" + idempotencyKey
}

func avString(v string) dbtypes.AttributeValue {
	return &dbtypes.AttributeValueMemberS{Value: v}
}

func avBool(v bool) dbtypes.AttributeValue {
	return &dbtypes.AttributeValueMemberBOOL{Value: v}
}

func avInt(v int) dbtypes.AttributeValue {
	return &dbtypes.AttributeValueMemberN{Value: strconv.Itoa(v)}
}

func avFloat(v float64) dbtypes.AttributeValue {
	return &dbtypes.AttributeValueMemberN{Value: strconv.FormatFloat(v, 'f', -1, 64)}
}

func avTime(t time.Time) dbtypes.AttributeValue {
	av, err := attributevalue.Marshal(t)
	if err != nil {
		return &dbtypes.AttributeValueMemberS{Value: t.UTC().Format(time.RFC3339Nano)}
	}
	return av
}

func getStringAttr(item map[string]dbtypes.AttributeValue, key string) (string, error) {
	av, ok := item[key]
	if !ok {
		return "", fmt.Errorf("missing attribute %s", key)
	}
	var v string
	if err := attributevalue.Unmarshal(av, &v); err != nil {
		return "", err
	}
	return v, nil
}

func buildStatusConditionTask(statuses []model.TaskStatus) (string, map[string]string, map[string]dbtypes.AttributeValue) {
	parts := make([]string, 0, len(statuses))
	values := make(map[string]dbtypes.AttributeValue, len(statuses))
	for i, status := range statuses {
		key := fmt.Sprintf(":s%d", i)
		parts = append(parts, "#status = "+key)
		values[key] = avString(string(status))
	}
	return strings.Join(parts, " OR "), map[string]string{"#status": "status"}, values
}

func buildTaskScanFilter(tenantID string, statuses []model.TaskStatus) (string, map[string]string, map[string]dbtypes.AttributeValue) {
	parts := []string{"#entity = :entity"}
	names := map[string]string{"#entity": "entity", "#status": "status"}
	values := map[string]dbtypes.AttributeValue{":entity": avString("task")}

	if tenantID != "" {
		parts = append(parts, "tenant_id = :tenant_id")
		values[":tenant_id"] = avString(tenantID)
	}
	if len(statuses) > 0 {
		statusKeys := make([]string, 0, len(statuses))
		for i, st := range statuses {
			k := fmt.Sprintf(":status%d", i)
			values[k] = avString(string(st))
			statusKeys = append(statusKeys, k)
		}
		parts = append(parts, "#status IN ("+strings.Join(statusKeys, ",")+")")
	}

	return strings.Join(parts, " AND "), names, values
}

func buildRunScanFilter(tenantID string, statuses []model.RunStatus) (string, map[string]string, map[string]dbtypes.AttributeValue) {
	parts := []string{"#entity = :entity"}
	names := map[string]string{"#entity": "entity", "#status": "status"}
	values := map[string]dbtypes.AttributeValue{":entity": avString("run")}

	if tenantID != "" {
		parts = append(parts, "tenant_id = :tenant_id")
		values[":tenant_id"] = avString(tenantID)
	}
	if len(statuses) > 0 {
		statusKeys := make([]string, 0, len(statuses))
		for i, st := range statuses {
			k := fmt.Sprintf(":status%d", i)
			values[k] = avString(string(st))
			statusKeys = append(statusKeys, k)
		}
		parts = append(parts, "#status IN ("+strings.Join(statusKeys, ",")+")")
	}

	return strings.Join(parts, " AND "), names, values
}

func (s *DynamoStore) taskExists(ctx context.Context, taskID string) (bool, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.tasksTable),
		ConsistentRead: aws.Bool(true),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(taskPK(taskID)),
			"sk": avString(taskMetaSK),
		},
		ProjectionExpression: aws.String("pk"),
	})
	if err != nil {
		return false, fmt.Errorf("state: check task exists: %w", err)
	}
	return len(out.Item) > 0, nil
}

func (s *DynamoStore) runExists(ctx context.Context, taskID, runID string) (bool, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.runsTable),
		ConsistentRead: aws.Bool(true),
		Key: map[string]dbtypes.AttributeValue{
			"pk": avString(runPK(taskID)),
			"sk": avString(runSK(runID)),
		},
		ProjectionExpression: aws.String("pk"),
	})
	if err != nil {
		return false, fmt.Errorf("state: check run exists: %w", err)
	}
	return len(out.Item) > 0, nil
}

func (s *DynamoStore) mapTaskConditionErr(ctx context.Context, taskID string) error {
	exists, err := s.taskExists(ctx, taskID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return ErrConflict
}

func isConditionalCheckFailed(err error) bool {
	var target *dbtypes.ConditionalCheckFailedException
	return errors.As(err, &target)
}

func isTransactionConditionFailed(err error) bool {
	var target *dbtypes.TransactionCanceledException
	if !errors.As(err, &target) {
		return false
	}
	for _, reason := range target.CancellationReasons {
		if aws.ToString(reason.Code) == "ConditionalCheckFailed" {
			return true
		}
	}
	return false
}

func isRunInsertConflict(err error) bool {
	var target *dbtypes.TransactionCanceledException
	if !errors.As(err, &target) {
		return false
	}
	if len(target.CancellationReasons) < 1 {
		return false
	}
	return aws.ToString(target.CancellationReasons[0].Code) == "ConditionalCheckFailed"
}

func isTaskUpdateConflict(err error) bool {
	var target *dbtypes.TransactionCanceledException
	if !errors.As(err, &target) {
		return false
	}
	if len(target.CancellationReasons) < 2 {
		return false
	}
	return aws.ToString(target.CancellationReasons[1].Code) == "ConditionalCheckFailed"
}
