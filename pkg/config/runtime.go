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
	DefaultRecoveryLimit          int32 = 200
	DefaultArtifactPresignExpires       = 15 * time.Minute
	DefaultRecoveryStaleFor             = 10 * time.Minute
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
	ArtifactSSEKMSKeyARN        string
	WebSocketEndpoint           string
	ArtifactPresignExpires      time.Duration
	SQSWaitTimeSeconds          int32
	SQSVisibilityTimeoutSeconds int32
	SQSMaxMessages              int32
}

// RecoveryRuntimeConfig controls stale-run recovery and consistency checks.
type RecoveryRuntimeConfig struct {
	Enabled           bool
	Interval          time.Duration
	StaleFor          time.Duration
	Limit             int
	TenantID          string
	ConsistencyCheck  bool
	ConsistencyRepair bool
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
		ArtifactSSEKMSKeyARN:        String("ARTIFACT_SSE_KMS_KEY_ARN", ""),
		WebSocketEndpoint:           NormalizeWebSocketEndpoint(String("WEBSOCKET_ENDPOINT", "")),
		ArtifactPresignExpires:      presign,
		SQSWaitTimeSeconds:          waitTime,
		SQSVisibilityTimeoutSeconds: visibility,
		SQSMaxMessages:              maxMessages,
	}, nil
}

// LoadRecoveryRuntimeConfigFromEnv loads operational recovery settings.
func LoadRecoveryRuntimeConfigFromEnv() (*RecoveryRuntimeConfig, error) {
	enabled, err := Bool("AGENTFORGE_RECOVERY_ENABLED", false)
	if err != nil {
		return nil, err
	}
	interval, err := Duration("AGENTFORGE_RECOVERY_INTERVAL", 0)
	if err != nil {
		return nil, err
	}
	if interval < 0 {
		return nil, fmt.Errorf("invalid AGENTFORGE_RECOVERY_INTERVAL: must be >= 0")
	}
	staleFor, err := Duration("AGENTFORGE_RECOVERY_STALE_FOR", DefaultRecoveryStaleFor)
	if err != nil {
		return nil, err
	}
	if staleFor <= 0 {
		return nil, fmt.Errorf("invalid AGENTFORGE_RECOVERY_STALE_FOR: must be > 0")
	}
	limit, err := Int32("AGENTFORGE_RECOVERY_LIMIT", DefaultRecoveryLimit)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, fmt.Errorf("invalid AGENTFORGE_RECOVERY_LIMIT: must be > 0")
	}
	check, err := Bool("AGENTFORGE_RECOVERY_CONSISTENCY_CHECK", false)
	if err != nil {
		return nil, err
	}
	repair, err := Bool("AGENTFORGE_RECOVERY_CONSISTENCY_REPAIR", false)
	if err != nil {
		return nil, err
	}
	if repair && !check {
		return nil, fmt.Errorf("AGENTFORGE_RECOVERY_CONSISTENCY_REPAIR requires AGENTFORGE_RECOVERY_CONSISTENCY_CHECK=true")
	}

	return &RecoveryRuntimeConfig{
		Enabled:           enabled,
		Interval:          interval,
		StaleFor:          staleFor,
		Limit:             int(limit),
		TenantID:          String("AGENTFORGE_RECOVERY_TENANT_ID", ""),
		ConsistencyCheck:  check,
		ConsistencyRepair: repair,
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

// Bool parses a boolean env value or returns default.
func Bool(key string, def bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "y", "on":
		return true, nil
	case "0", "false", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid %s: %q", key, raw)
	}
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
