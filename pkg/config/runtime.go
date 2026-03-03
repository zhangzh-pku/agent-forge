// Package config centralizes runtime/environment configuration loading.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultConnectionsTaskIndex         = "task-index"
	DefaultSQSWaitTimeSeconds     int32 = 20
	DefaultSQSVisibilityTimeout   int32 = 300
	DefaultSQSMaxMessages         int32 = 10
	DefaultArtifactPresignExpires       = 15 * time.Minute
)

// RuntimeMode controls which backend implementations the app should use.
type RuntimeMode string

const (
	RuntimeModeLocal RuntimeMode = "local"
	RuntimeModeAWS   RuntimeMode = "aws"
)

// ParseRuntimeMode parses user-provided runtime strings.
func ParseRuntimeMode(raw string) RuntimeMode {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "", "local", "dev", "development":
		return RuntimeModeLocal
	case "aws", "prod", "production":
		return RuntimeModeAWS
	default:
		return RuntimeMode(mode)
	}
}

// RuntimeModeFromEnv reads AGENTFORGE_RUNTIME and returns the normalized mode.
func RuntimeModeFromEnv() RuntimeMode {
	return ParseRuntimeMode(os.Getenv("AGENTFORGE_RUNTIME"))
}

// AWSStateConfig contains required DynamoDB table and index names.
type AWSStateConfig struct {
	TasksTable       string
	RunsTable        string
	StepsTable       string
	ConnectionsTable string
	ConnectionIndex  string
}

// AWSRuntimeConfig contains full production runtime backend settings.
type AWSRuntimeConfig struct {
	State                       AWSStateConfig
	TaskQueueURL                string
	ArtifactsBucket             string
	WebSocketEndpoint           string
	ArtifactPresignExpires      time.Duration
	SQSWaitTimeSeconds          int32
	SQSVisibilityTimeoutSeconds int32
	SQSMaxMessages              int32
}

// LoadAWSStateConfigFromEnv loads DynamoDB table/index config for aws mode.
func LoadAWSStateConfigFromEnv() (*AWSStateConfig, error) {
	tasksTable, err := RequiredString("TASKS_TABLE")
	if err != nil {
		return nil, err
	}
	runsTable, err := RequiredString("RUNS_TABLE")
	if err != nil {
		return nil, err
	}
	stepsTable, err := RequiredString("STEPS_TABLE")
	if err != nil {
		return nil, err
	}
	connectionsTable, err := RequiredString("CONNECTIONS_TABLE")
	if err != nil {
		return nil, err
	}

	return &AWSStateConfig{
		TasksTable:       tasksTable,
		RunsTable:        runsTable,
		StepsTable:       stepsTable,
		ConnectionsTable: connectionsTable,
		ConnectionIndex:  String("CONNECTIONS_TASK_INDEX", DefaultConnectionsTaskIndex),
	}, nil
}

// LoadAWSRuntimeConfigFromEnv loads full runtime config for taskapi/worker in aws mode.
func LoadAWSRuntimeConfigFromEnv() (*AWSRuntimeConfig, error) {
	stateCfg, err := LoadAWSStateConfigFromEnv()
	if err != nil {
		return nil, err
	}

	queueURL, err := RequiredString("TASK_QUEUE_URL")
	if err != nil {
		return nil, err
	}
	artifactsBucket, err := RequiredString("ARTIFACTS_BUCKET")
	if err != nil {
		return nil, err
	}
	presign, err := Duration("ARTIFACT_PRESIGN_EXPIRES", DefaultArtifactPresignExpires)
	if err != nil {
		return nil, err
	}
	waitTime, err := Int32("SQS_WAIT_TIME_SECONDS", DefaultSQSWaitTimeSeconds)
	if err != nil {
		return nil, err
	}
	visibility, err := Int32("SQS_VISIBILITY_TIMEOUT_SECONDS", DefaultSQSVisibilityTimeout)
	if err != nil {
		return nil, err
	}
	maxMessages, err := Int32("SQS_MAX_MESSAGES", DefaultSQSMaxMessages)
	if err != nil {
		return nil, err
	}

	return &AWSRuntimeConfig{
		State:                       *stateCfg,
		TaskQueueURL:                queueURL,
		ArtifactsBucket:             artifactsBucket,
		WebSocketEndpoint:           NormalizeWebSocketEndpoint(String("WEBSOCKET_ENDPOINT", "")),
		ArtifactPresignExpires:      presign,
		SQSWaitTimeSeconds:          waitTime,
		SQSVisibilityTimeoutSeconds: visibility,
		SQSMaxMessages:              maxMessages,
	}, nil
}

// RequiredString returns a non-empty trimmed env value.
func RequiredString(key string) (string, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return "", fmt.Errorf("missing required environment variable %s", key)
	}
	return v, nil
}

// String returns a trimmed env value or the provided default.
func String(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}

// Int32 parses an int32 env value or returns default.
func Int32(key string, def int32) (int32, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return int32(n), nil
}

// Duration parses a duration env value (e.g. 15m, 1h) or returns default.
func Duration(key string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return d, nil
}

// NormalizeWebSocketEndpoint converts wss:// endpoint strings to https:// management endpoints.
func NormalizeWebSocketEndpoint(endpoint string) string {
	trimmed := strings.TrimSpace(endpoint)
	if strings.HasPrefix(trimmed, "wss://") {
		return "https://" + strings.TrimPrefix(trimmed, "wss://")
	}
	return trimmed
}
