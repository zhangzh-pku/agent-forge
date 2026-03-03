package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

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
	client            *sqs.Client
	queueURL          string
	waitTimeSeconds   int32
	visibilityTimeout int32
	maxMessages       int32
	isFIFO            bool
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
		client:            client,
		queueURL:          queueURL,
		waitTimeSeconds:   cfg.WaitTimeSeconds,
		visibilityTimeout: cfg.VisibilityTimeout,
		maxMessages:       cfg.MaxMessages,
		isFIFO:            strings.HasSuffix(queueURL, ".fifo"),
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

	input := &sqs.SendMessageInput{
		QueueUrl:    aws.String(q.queueURL),
		MessageBody: aws.String(string(body)),
	}
	if q.isFIFO {
		groupID := cp.TenantID
		if groupID == "" {
			groupID = "default"
		}
		dedupeID := cp.DedupeKey
		if dedupeID == "" {
			dedupeID = cp.TaskID + "#" + cp.RunID
		}
		input.MessageGroupId = aws.String(groupID)
		input.MessageDeduplicationId = aws.String(dedupeID)
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

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		out, err := q.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(q.queueURL),
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
			return fmt.Errorf("queue: sqs receive message: %w", err)
		}

		for _, sqsMsg := range out.Messages {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			if sqsMsg.Body == nil {
				if err := q.deleteMessage(ctx, sqsMsg); err != nil {
					log.Printf("queue: drop empty sqs message failed: %v", err)
				}
				continue
			}

			msg := &model.SQSMessage{}
			if err := json.Unmarshal([]byte(*sqsMsg.Body), msg); err != nil {
				log.Printf("queue: invalid sqs payload, dropping message: %v", err)
				if delErr := q.deleteMessage(ctx, sqsMsg); delErr != nil {
					log.Printf("queue: delete invalid sqs message failed: %v", delErr)
				}
				continue
			}

			if rcRaw, ok := sqsMsg.Attributes[string(sqstypes.MessageSystemAttributeNameApproximateReceiveCount)]; ok {
				if rc, convErr := strconv.Atoi(rcRaw); convErr == nil && rc > 0 {
					msg.Attempt = rc
				}
			}
			if msg.Attempt <= 0 {
				msg.Attempt = 1
			}

			err := handler(ctx, msg)
			if err != nil {
				// Do not delete. SQS redrive policy handles retries/DLQ.
				continue
			}
			if err := q.deleteMessage(ctx, sqsMsg); err != nil {
				return err
			}
		}
	}
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
