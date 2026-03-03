package queue

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
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
