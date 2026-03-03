package config

import (
	"testing"
	"time"
)

func TestParseRuntimeMode(t *testing.T) {
	cases := map[string]RuntimeMode{
		"":            RuntimeModeLocal,
		"local":       RuntimeModeLocal,
		"development": RuntimeModeLocal,
		"aws":         RuntimeModeAWS,
		"prod":        RuntimeModeAWS,
		"custom":      RuntimeMode("custom"),
	}
	for in, want := range cases {
		if got := ParseRuntimeMode(in); got != want {
			t.Fatalf("ParseRuntimeMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadAWSStateConfigFromEnv(t *testing.T) {
	t.Setenv("TASKS_TABLE", "tasks")
	t.Setenv("RUNS_TABLE", "runs")
	t.Setenv("STEPS_TABLE", "steps")
	t.Setenv("CONNECTIONS_TABLE", "connections")
	t.Setenv("CONNECTIONS_TASK_INDEX", "task-index")

	cfg, err := LoadAWSStateConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadAWSStateConfigFromEnv error: %v", err)
	}
	if cfg.TasksTable != "tasks" || cfg.RunsTable != "runs" || cfg.StepsTable != "steps" || cfg.ConnectionsTable != "connections" {
		t.Fatalf("unexpected state config: %+v", cfg)
	}
}

func TestLoadAWSRuntimeConfigFromEnv(t *testing.T) {
	t.Setenv("TASKS_TABLE", "tasks")
	t.Setenv("RUNS_TABLE", "runs")
	t.Setenv("STEPS_TABLE", "steps")
	t.Setenv("CONNECTIONS_TABLE", "connections")
	t.Setenv("TASK_QUEUE_URL", "https://sqs.us-east-1.amazonaws.com/123/agentforge")
	t.Setenv("ARTIFACTS_BUCKET", "agentforge-artifacts")
	t.Setenv("ARTIFACT_SSE_KMS_KEY_ARN", "arn:aws:kms:us-east-1:123456789012:key/abcd-1234")
	t.Setenv("WEBSOCKET_ENDPOINT", "wss://abc.execute-api.us-east-1.amazonaws.com/prod")
	t.Setenv("ARTIFACT_PRESIGN_EXPIRES", "30m")
	t.Setenv("SQS_WAIT_TIME_SECONDS", "15")
	t.Setenv("SQS_VISIBILITY_TIMEOUT_SECONDS", "600")
	t.Setenv("SQS_MAX_MESSAGES", "5")

	cfg, err := LoadAWSRuntimeConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadAWSRuntimeConfigFromEnv error: %v", err)
	}
	if cfg.TaskQueueURL == "" || cfg.ArtifactsBucket == "" {
		t.Fatalf("missing required runtime fields: %+v", cfg)
	}
	if cfg.ArtifactSSEKMSKeyARN == "" {
		t.Fatalf("expected artifact SSE KMS key to be loaded")
	}
	if cfg.WebSocketEndpoint != "https://abc.execute-api.us-east-1.amazonaws.com/prod" {
		t.Fatalf("unexpected websocket endpoint: %s", cfg.WebSocketEndpoint)
	}
	if cfg.ArtifactPresignExpires != 30*time.Minute {
		t.Fatalf("unexpected presign expires: %s", cfg.ArtifactPresignExpires)
	}
	if cfg.SQSWaitTimeSeconds != 15 || cfg.SQSVisibilityTimeoutSeconds != 600 || cfg.SQSMaxMessages != 5 {
		t.Fatalf("unexpected sqs config: %+v", cfg)
	}
}

func TestLoadRecoveryRuntimeConfigFromEnv(t *testing.T) {
	t.Setenv("AGENTFORGE_RECOVERY_ENABLED", "true")
	t.Setenv("AGENTFORGE_RECOVERY_INTERVAL", "2m")
	t.Setenv("AGENTFORGE_RECOVERY_STALE_FOR", "15m")
	t.Setenv("AGENTFORGE_RECOVERY_LIMIT", "300")
	t.Setenv("AGENTFORGE_RECOVERY_TENANT_ID", "tnt_1")
	t.Setenv("AGENTFORGE_RECOVERY_CONSISTENCY_CHECK", "true")
	t.Setenv("AGENTFORGE_RECOVERY_CONSISTENCY_REPAIR", "true")

	cfg, err := LoadRecoveryRuntimeConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadRecoveryRuntimeConfigFromEnv error: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("expected recovery enabled")
	}
	if cfg.Interval != 2*time.Minute {
		t.Fatalf("unexpected interval: %s", cfg.Interval)
	}
	if cfg.StaleFor != 15*time.Minute {
		t.Fatalf("unexpected stale_for: %s", cfg.StaleFor)
	}
	if cfg.Limit != 300 {
		t.Fatalf("unexpected limit: %d", cfg.Limit)
	}
	if cfg.TenantID != "tnt_1" {
		t.Fatalf("unexpected tenant_id: %s", cfg.TenantID)
	}
	if !cfg.ConsistencyCheck || !cfg.ConsistencyRepair {
		t.Fatalf("unexpected consistency flags: %+v", cfg)
	}
}

func TestLoadRecoveryRuntimeConfigFromEnvInvalidRepairWithoutCheck(t *testing.T) {
	t.Setenv("AGENTFORGE_RECOVERY_CONSISTENCY_CHECK", "false")
	t.Setenv("AGENTFORGE_RECOVERY_CONSISTENCY_REPAIR", "true")
	if _, err := LoadRecoveryRuntimeConfigFromEnv(); err == nil {
		t.Fatal("expected error when repair is enabled without check")
	}
}
