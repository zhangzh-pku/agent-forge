package queue

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

func TestSQSQueueIntegration_EnqueueConsumeAndDelete(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("AGENTFORGE_INTEGRATION_AWS_ENDPOINT"))
	if endpoint == "" {
		t.Skip("set AGENTFORGE_INTEGRATION_AWS_ENDPOINT to run SQS integration tests")
	}

	ctx := context.Background()
	client := integrationSQSClient(t, endpoint)

	queueName := "af-it-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")) + "-" + time.Now().UTC().Format("150405")
	createOut, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(queueName),
	})
	if err != nil {
		t.Fatalf("create queue: %v", err)
	}
	if createOut.QueueUrl == nil || *createOut.QueueUrl == "" {
		t.Fatal("create queue returned empty queue url")
	}

	q, err := NewSQSQueue(client, SQSQueueConfig{
		QueueURL:          *createOut.QueueUrl,
		WaitTimeSeconds:   1,
		VisibilityTimeout: 10,
		MaxMessages:       1,
	})
	if err != nil {
		t.Fatalf("new sqs queue: %v", err)
	}

	consumeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	receivedCh := make(chan *model.SQSMessage, 1)
	errCh := make(chan error, 1)

	go func() {
		err := q.StartConsumer(consumeCtx, func(_ context.Context, msg *model.SQSMessage) error {
			select {
			case receivedCh <- msg:
			default:
			}
			cancel()
			return nil
		})
		if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
			errCh <- err
		}
	}()

	msg := &model.SQSMessage{
		TenantID:    "tnt_integration",
		TaskID:      "task_it_1",
		RunID:       "run_it_1",
		SubmittedAt: time.Now().Unix(),
		Attempt:     1,
	}
	if err := q.Enqueue(consumeCtx, msg); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case got := <-receivedCh:
		if got.TaskID != msg.TaskID || got.RunID != msg.RunID || got.TenantID != msg.TenantID {
			t.Fatalf("unexpected message: %+v", got)
		}
	case err := <-errCh:
		t.Fatalf("consumer error: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for consumed message")
	}

	assertQueueEventuallyEmpty(t, client, *createOut.QueueUrl)
}

func TestSQSQueueIntegration_StartConsumerBatchConcurrency(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("AGENTFORGE_INTEGRATION_AWS_ENDPOINT"))
	if endpoint == "" {
		t.Skip("set AGENTFORGE_INTEGRATION_AWS_ENDPOINT to run SQS integration tests")
	}

	ctx := context.Background()
	client := integrationSQSClient(t, endpoint)

	queueName := "af-it-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")) + "-" + time.Now().UTC().Format("150405")
	createOut, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(queueName),
	})
	if err != nil {
		t.Fatalf("create queue: %v", err)
	}
	if createOut.QueueUrl == nil || *createOut.QueueUrl == "" {
		t.Fatal("create queue returned empty queue url")
	}

	q, err := NewSQSQueue(client, SQSQueueConfig{
		QueueURL:          *createOut.QueueUrl,
		WaitTimeSeconds:   1,
		VisibilityTimeout: 10,
		MaxMessages:       10,
	})
	if err != nil {
		t.Fatalf("new sqs queue: %v", err)
	}

	const messageCount = 4
	taskPrefix := "task_batch_" + time.Now().UTC().Format("150405")
	for i := 0; i < messageCount; i++ {
		if err := q.Enqueue(ctx, &model.SQSMessage{
			TenantID:    "tnt_integration",
			TaskID:      taskPrefix + string(rune('a'+i)),
			RunID:       "run_it_1",
			SubmittedAt: time.Now().Unix(),
		}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	consumeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var inFlight atomic.Int64
	var maxInFlight atomic.Int64
	var processed atomic.Int64

	errCh := make(chan error, 1)
	go func() {
		err := q.StartConsumer(consumeCtx, func(_ context.Context, _ *model.SQSMessage) error {
			curr := inFlight.Add(1)
			for {
				prev := maxInFlight.Load()
				if curr <= prev {
					break
				}
				if maxInFlight.CompareAndSwap(prev, curr) {
					break
				}
			}

			time.Sleep(200 * time.Millisecond)
			inFlight.Add(-1)

			if processed.Add(1) >= messageCount {
				cancel()
			}
			return nil
		})
		if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		t.Fatalf("consumer error: %v", err)
	case <-consumeCtx.Done():
	}

	if processed.Load() != messageCount {
		t.Fatalf("expected %d messages processed, got %d", messageCount, processed.Load())
	}
	if maxInFlight.Load() < 2 {
		t.Fatalf("expected concurrent batch processing, max in-flight was %d", maxInFlight.Load())
	}

	assertQueueEventuallyEmpty(t, client, *createOut.QueueUrl)
}

func TestSQSQueueIntegration_RetryOnHandlerError(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("AGENTFORGE_INTEGRATION_AWS_ENDPOINT"))
	if endpoint == "" {
		t.Skip("set AGENTFORGE_INTEGRATION_AWS_ENDPOINT to run SQS integration tests")
	}

	ctx := context.Background()
	client := integrationSQSClient(t, endpoint)
	queueURL := createIntegrationQueue(t, ctx, client, t.Name(), nil)

	q, err := NewSQSQueue(client, SQSQueueConfig{
		QueueURL:          queueURL,
		WaitTimeSeconds:   1,
		VisibilityTimeout: 1,
		MaxMessages:       1,
	})
	if err != nil {
		t.Fatalf("new sqs queue: %v", err)
	}

	msg := &model.SQSMessage{
		TenantID:    "tnt_integration",
		TaskID:      "task_retry_1",
		RunID:       "run_retry_1",
		SubmittedAt: time.Now().Unix(),
		Attempt:     1,
	}
	if err := q.Enqueue(ctx, msg); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	consumeCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	var seenFirst atomic.Bool
	var seenRetry atomic.Bool
	errCh := make(chan error, 1)
	doneCh := make(chan struct{}, 1)

	go func() {
		err := q.StartConsumer(consumeCtx, func(_ context.Context, received *model.SQSMessage) error {
			if received.TaskID != msg.TaskID || received.RunID != msg.RunID {
				return errors.New("unexpected message identity")
			}
			if received.Attempt <= 1 {
				seenFirst.Store(true)
				return errors.New("force retry")
			}
			seenRetry.Store(true)
			select {
			case doneCh <- struct{}{}:
			default:
			}
			cancel()
			return nil
		})
		if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
			errCh <- err
		}
	}()

	select {
	case <-doneCh:
	case err := <-errCh:
		t.Fatalf("consumer error: %v", err)
	case <-consumeCtx.Done():
		t.Fatalf("timeout waiting for retry delivery, first=%t retry=%t", seenFirst.Load(), seenRetry.Load())
	}

	if !seenFirst.Load() || !seenRetry.Load() {
		t.Fatalf("expected first attempt and retry, got first=%t retry=%t", seenFirst.Load(), seenRetry.Load())
	}

	assertQueueEventuallyEmpty(t, client, queueURL)
}

func TestSQSQueueIntegration_RedriveToDLQAfterMaxReceiveCount(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("AGENTFORGE_INTEGRATION_AWS_ENDPOINT"))
	if endpoint == "" {
		t.Skip("set AGENTFORGE_INTEGRATION_AWS_ENDPOINT to run SQS integration tests")
	}

	ctx := context.Background()
	client := integrationSQSClient(t, endpoint)

	dlqURL := createIntegrationQueue(t, ctx, client, t.Name()+"-dlq", nil)
	dlqAttrs, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(dlqURL),
		AttributeNames: []sqstypes.QueueAttributeName{
			sqstypes.QueueAttributeNameQueueArn,
		},
	})
	if err != nil {
		t.Fatalf("get DLQ attributes: %v", err)
	}
	dlqARN := dlqAttrs.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]
	if dlqARN == "" {
		t.Fatal("dlq queue arn is empty")
	}

	redrivePolicy := `{"deadLetterTargetArn":"` + dlqARN + `","maxReceiveCount":"2"}`
	queueURL := createIntegrationQueue(t, ctx, client, t.Name(), map[string]string{
		string(sqstypes.QueueAttributeNameRedrivePolicy): redrivePolicy,
	})

	q, err := NewSQSQueue(client, SQSQueueConfig{
		QueueURL:          queueURL,
		WaitTimeSeconds:   1,
		VisibilityTimeout: 1,
		MaxMessages:       1,
	})
	if err != nil {
		t.Fatalf("new sqs queue: %v", err)
	}

	msg := &model.SQSMessage{
		TenantID:    "tnt_integration",
		TaskID:      "task_dlq_1",
		RunID:       "run_dlq_1",
		SubmittedAt: time.Now().Unix(),
		Attempt:     1,
	}
	if err := q.Enqueue(ctx, msg); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	consumeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		err := q.StartConsumer(consumeCtx, func(_ context.Context, _ *model.SQSMessage) error {
			return errors.New("force DLQ")
		})
		if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
			errCh <- err
		}
	}()

	var dlqMsg *model.SQSMessage
	waitForSQSCondition(t, 25*time.Second, 500*time.Millisecond, func() (bool, error) {
		out, err := client.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(dlqURL),
			MaxNumberOfMessages: 1,
			WaitTimeSeconds:     1,
			VisibilityTimeout:   10,
		})
		if err != nil {
			return false, err
		}
		if len(out.Messages) == 0 {
			return false, nil
		}
		m := out.Messages[0]
		if m.ReceiptHandle != nil {
			_, _ = client.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{
				QueueUrl:      aws.String(dlqURL),
				ReceiptHandle: m.ReceiptHandle,
			})
		}
		if m.Body == nil || *m.Body == "" {
			return false, errors.New("dlq message body is empty")
		}
		var decoded model.SQSMessage
		if err := json.Unmarshal([]byte(*m.Body), &decoded); err != nil {
			return false, err
		}
		dlqMsg = &decoded
		return true, nil
	}, "message was not moved to DLQ")

	cancel()
	select {
	case err := <-errCh:
		t.Fatalf("consumer error: %v", err)
	default:
	}

	if dlqMsg == nil {
		t.Fatal("expected decoded DLQ message")
	}
	if dlqMsg.TaskID != msg.TaskID || dlqMsg.RunID != msg.RunID {
		t.Fatalf("unexpected DLQ payload: %+v", *dlqMsg)
	}
}

func integrationSQSClient(t *testing.T, endpoint string) *sqs.Client {
	t.Helper()
	cfg, err := awscfg.LoadDefaultConfig(
		context.Background(),
		awscfg.WithRegion("us-east-1"),
		awscfg.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}
	return sqs.NewFromConfig(cfg, func(o *sqs.Options) {
		o.BaseEndpoint = &endpoint
	})
}

func createIntegrationQueue(
	t *testing.T,
	ctx context.Context,
	client *sqs.Client,
	nameSeed string,
	attributes map[string]string,
) string {
	t.Helper()
	queueName := "af-it-" + strings.ToLower(strings.ReplaceAll(nameSeed, "/", "-")) + "-" + time.Now().UTC().Format("150405")
	in := &sqs.CreateQueueInput{
		QueueName: aws.String(queueName),
	}
	if len(attributes) > 0 {
		in.Attributes = attributes
	}
	out, err := client.CreateQueue(ctx, in)
	if err != nil {
		t.Fatalf("create queue: %v", err)
	}
	if out.QueueUrl == nil || *out.QueueUrl == "" {
		t.Fatal("create queue returned empty queue url")
	}
	return *out.QueueUrl
}

func waitForSQSCondition(
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

func assertQueueEventuallyEmpty(t *testing.T, client *sqs.Client, queueURL string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, err := client.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: 1,
			WaitTimeSeconds:     1,
			VisibilityTimeout:   1,
		})
		if err != nil {
			t.Fatalf("receive message: %v", err)
		}
		if len(out.Messages) == 0 {
			return
		}
		// Unexpected leftover message: delete and continue one more poll.
		for _, msg := range out.Messages {
			if msg.ReceiptHandle == nil {
				continue
			}
			_, _ = client.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{
				QueueUrl:      aws.String(queueURL),
				ReceiptHandle: msg.ReceiptHandle,
			})
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("queue did not become empty after successful consumer handling")
}
