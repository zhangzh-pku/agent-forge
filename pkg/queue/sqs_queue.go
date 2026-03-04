package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentforge/agentforge/pkg/model"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

const (
	defaultSQSWaitTimeSeconds       int32 = 20
	defaultSQSVisibilityTimeoutSecs int32 = 300
	defaultSQSMaxMessages           int32 = 10
)

// SQSQueueConfig defines SQS queue behavior.
type SQSQueueConfig struct {
	QueueURL          string
	WaitTimeSeconds   int32
	VisibilityTimeout int32
	MaxMessages       int32
}

// SQSQueue is a production queue implementation backed by Amazon SQS.
type SQSQueue struct {
	client             *sqs.Client
	queueURL           string
	waitTimeSeconds    int32
	visibilityTimeout  int32
	maxMessages        int32
	handlerConcurrency int
	isFIFO             bool
}

// HealthCheck verifies SQS queue reachability.
func (q *SQSQueue) HealthCheck(ctx context.Context) error {
	_, err := q.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(q.queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{
			sqstypes.QueueAttributeNameApproximateNumberOfMessages,
		},
	})
	if err != nil {
		return fmt.Errorf("queue: sqs health check: %w", err)
	}
	return nil
}

// NewSQSQueue creates an SQS-backed queue implementation.
func NewSQSQueue(client *sqs.Client, cfg SQSQueueConfig) (*SQSQueue, error) {
	if client == nil {
		return nil, fmt.Errorf("queue: sqs client is required")
	}
	if strings.TrimSpace(cfg.QueueURL) == "" {
		return nil, fmt.Errorf("queue: queue url is required")
	}
	if cfg.WaitTimeSeconds <= 0 {
		cfg.WaitTimeSeconds = defaultSQSWaitTimeSeconds
	}
	if cfg.VisibilityTimeout <= 0 {
		cfg.VisibilityTimeout = defaultSQSVisibilityTimeoutSecs
	}
	if cfg.MaxMessages <= 0 || cfg.MaxMessages > 10 {
		cfg.MaxMessages = defaultSQSMaxMessages
	}

	queueURL := strings.TrimSpace(cfg.QueueURL)
	return &SQSQueue{
		client:             client,
		queueURL:           queueURL,
		waitTimeSeconds:    cfg.WaitTimeSeconds,
		visibilityTimeout:  cfg.VisibilityTimeout,
		maxMessages:        cfg.MaxMessages,
		handlerConcurrency: int(cfg.MaxMessages),
		isFIFO:             strings.HasSuffix(queueURL, ".fifo"),
	}, nil
}

// Enqueue publishes a pointer message to SQS.
func (q *SQSQueue) Enqueue(ctx context.Context, msg *model.SQSMessage) error {
	if msg == nil {
		return fmt.Errorf("queue: enqueue: nil message")
	}
	cp := *msg
	if cp.Attempt <= 0 {
		cp.Attempt = 1
	}

	body, err := json.Marshal(cp)
	if err != nil {
		return fmt.Errorf("queue: marshal message: %w", err)
	}

	input := q.buildSendMessageInput(string(body))
	if q.isFIFO {
		// MessageGroupId is scoped to task/run to reduce tenant-level head-of-line
		// blocking while preserving ordering within a single run.
		input.MessageGroupId = aws.String(groupIDForMessage(&cp))
		input.MessageDeduplicationId = aws.String(dedupeIDForMessage(&cp))
	}

	if _, err := q.client.SendMessage(ctx, input); err != nil {
		return fmt.Errorf("queue: sqs send message: %w", err)
	}
	return nil
}

// StartConsumer long-polls SQS and dispatches messages to handler.
func (q *SQSQueue) StartConsumer(ctx context.Context, handler MessageHandler) error {
	if handler == nil {
		return fmt.Errorf("queue: nil handler")
	}
	backoff := time.Second

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		out, err := q.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(q.queueURL),
			// Keep both selectors for compatibility across providers.
			// LocalStack currently populates ApproximateReceiveCount via
			// AttributeNames, while AWS supports MessageSystemAttributeNames.
			AttributeNames: []sqstypes.QueueAttributeName{
				sqstypes.QueueAttributeName("ApproximateReceiveCount"),
			},
			MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{
				sqstypes.MessageSystemAttributeNameApproximateReceiveCount,
			},
			MaxNumberOfMessages: q.maxMessages,
			WaitTimeSeconds:     q.waitTimeSeconds,
			VisibilityTimeout:   q.visibilityTimeout,
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("queue: sqs receive message failed: %v", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
			continue
		}
		backoff = time.Second

		if err := q.processBatch(ctx, handler, out.Messages); err != nil {
			return err
		}
	}
}

func (q *SQSQueue) processBatch(ctx context.Context, handler MessageHandler, messages []sqstypes.Message) error {
	if len(messages) == 0 {
		return nil
	}
	concurrency := q.handlerConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > len(messages) {
		concurrency = len(messages)
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for _, sqsMsg := range messages {
		if ctx.Err() != nil {
			break
		}
		msg := sqsMsg
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			q.processMessage(ctx, handler, msg)
		}()
	}
	wg.Wait()
	return ctx.Err()
}

func (q *SQSQueue) processMessage(ctx context.Context, handler MessageHandler, sqsMsg sqstypes.Message) {
	if sqsMsg.Body == nil {
		if err := q.deleteMessage(ctx, sqsMsg); err != nil {
			log.Printf("queue: drop empty sqs message failed: %v", err)
		}
		return
	}

	msg := &model.SQSMessage{}
	if err := json.Unmarshal([]byte(*sqsMsg.Body), msg); err != nil {
		log.Printf("queue: invalid sqs payload, dropping message: %v", err)
		if delErr := q.deleteMessage(ctx, sqsMsg); delErr != nil {
			log.Printf("queue: delete invalid sqs message failed: %v", delErr)
		}
		return
	}

	if rcRaw, ok := sqsMsg.Attributes[string(sqstypes.MessageSystemAttributeNameApproximateReceiveCount)]; ok {
		if rc, convErr := strconv.Atoi(rcRaw); convErr == nil && rc > 0 {
			msg.Attempt = rc
		}
	}
	if msg.Attempt <= 0 {
		msg.Attempt = 1
	}

	stopHeartbeat := q.startVisibilityHeartbeat(ctx, sqsMsg)
	err := handler(ctx, msg)
	if stopHeartbeat != nil {
		close(stopHeartbeat)
	}
	if err != nil {
		// Do not delete. SQS redrive policy handles retries/DLQ.
		// Hint visibility timeout to a short retry interval so retries
		// are not gated by queue-level defaults (for example, 30s).
		if visErr := q.accelerateRetry(sqsMsg); visErr != nil {
			log.Printf("queue: accelerate retry visibility failed: %v", visErr)
		}
		return
	}
	if err := q.deleteMessage(ctx, sqsMsg); err != nil {
		log.Printf("queue: delete processed sqs message failed: %v", err)
	}
}

func (q *SQSQueue) buildSendMessageInput(body string) *sqs.SendMessageInput {
	return &sqs.SendMessageInput{
		QueueUrl:    aws.String(q.queueURL),
		MessageBody: aws.String(body),
	}
}

func groupIDForMessage(msg *model.SQSMessage) string {
	if msg == nil {
		return "default"
	}
	switch {
	case msg.TaskID != "" && msg.RunID != "":
		return msg.TaskID + "#" + msg.RunID
	case msg.TaskID != "":
		return msg.TaskID
	case msg.RunID != "":
		return msg.RunID
	case msg.TenantID != "":
		return msg.TenantID
	default:
		return "default"
	}
}

func dedupeIDForMessage(msg *model.SQSMessage) string {
	if msg == nil {
		return ""
	}
	if msg.DedupeKey != "" {
		return msg.DedupeKey
	}
	return msg.TaskID + "#" + msg.RunID
}

func (q *SQSQueue) startVisibilityHeartbeat(ctx context.Context, msg sqstypes.Message) chan struct{} {
	if q.visibilityTimeout <= 1 || msg.ReceiptHandle == nil || *msg.ReceiptHandle == "" {
		return nil
	}
	interval := time.Duration(q.visibilityTimeout/2) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				heartbeatCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				_, err := q.client.ChangeMessageVisibility(heartbeatCtx, &sqs.ChangeMessageVisibilityInput{
					QueueUrl:          aws.String(q.queueURL),
					ReceiptHandle:     msg.ReceiptHandle,
					VisibilityTimeout: q.visibilityTimeout,
				})
				cancel()
				if err != nil {
					log.Printf("queue: visibility heartbeat failed: %v", err)
				}
			}
		}
	}()
	return stop
}

func (q *SQSQueue) deleteMessage(ctx context.Context, msg sqstypes.Message) error {
	if msg.ReceiptHandle == nil || *msg.ReceiptHandle == "" {
		return nil
	}
	_, err := q.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(q.queueURL),
		ReceiptHandle: msg.ReceiptHandle,
	})
	if err != nil {
		return fmt.Errorf("queue: sqs delete message: %w", err)
	}
	return nil
}

func (q *SQSQueue) accelerateRetry(msg sqstypes.Message) error {
	if msg.ReceiptHandle == nil || *msg.ReceiptHandle == "" {
		return nil
	}
	// Keep retry cadence bounded and independent from queue defaults.
	retryVisibility := int32(1)
	if q.visibilityTimeout > 0 && q.visibilityTimeout < retryVisibility {
		retryVisibility = q.visibilityTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := q.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(q.queueURL),
		ReceiptHandle:     msg.ReceiptHandle,
		VisibilityTimeout: retryVisibility,
	})
	if err != nil {
		return fmt.Errorf("queue: sqs accelerate retry visibility: %w", err)
	}
	return nil
}
