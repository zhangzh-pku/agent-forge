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
	t.Setenv("AGENTFORGE_EVENT_RETENTION", "36h")
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
	if cfg.EventRetention != 36*time.Hour {
		t.Fatalf("unexpected event retention: %s", cfg.EventRetention)
	}
	if cfg.SQSWaitTimeSeconds != 15 || cfg.SQSVisibilityTimeoutSeconds != 600 || cfg.SQSMaxMessages != 5 {
		t.Fatalf("unexpected sqs config: %+v", cfg)
	}
}

func TestEventRetentionFromEnvRejectsNegative(t *testing.T) {
	t.Setenv("AGENTFORGE_EVENT_RETENTION", "-1m")
	if _, err := EventRetentionFromEnv(); err == nil {
		t.Fatal("expected error for negative AGENTFORGE_EVENT_RETENTION")
	}
}

func TestEventRetentionFromEnvAllowsZero(t *testing.T) {
	t.Setenv("AGENTFORGE_EVENT_RETENTION", "0")
	retention, err := EventRetentionFromEnv()
	if err != nil {
		t.Fatalf("EventRetentionFromEnv error: %v", err)
	}
	if retention != 0 {
		t.Fatalf("expected zero retention, got %s", retention)
	}
}

func TestEventRetentionFromEnvDefault(t *testing.T) {
	t.Setenv("AGENTFORGE_EVENT_RETENTION", "")
	retention, err := EventRetentionFromEnv()
	if err != nil {
		t.Fatalf("EventRetentionFromEnv error: %v", err)
	}
	if retention != DefaultEventRetention {
		t.Fatalf("expected default retention %s, got %s", DefaultEventRetention, retention)
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

func TestLoadTelemetryRuntimeConfigFromEnvDefaults(t *testing.T) {
	cfg, err := LoadTelemetryRuntimeConfigFromEnv("agentforge-taskapi")
	if err != nil {
		t.Fatalf("LoadTelemetryRuntimeConfigFromEnv error: %v", err)
	}
	if cfg.Enabled {
		t.Fatal("expected telemetry disabled by default")
	}
	if cfg.ServiceName != "agentforge-taskapi" {
		t.Fatalf("unexpected service name: %s", cfg.ServiceName)
	}
	if cfg.Exporter != DefaultOTelExporter {
		t.Fatalf("unexpected exporter: %s", cfg.Exporter)
	}
	if cfg.SampleRatio != DefaultOTelSampleRatio {
		t.Fatalf("unexpected sample ratio: %v", cfg.SampleRatio)
	}
}

func TestLoadTelemetryRuntimeConfigFromEnvCustom(t *testing.T) {
	t.Setenv("AGENTFORGE_OTEL_ENABLED", "true")
	t.Setenv("AGENTFORGE_OTEL_SERVICE_NAME", "agentforge-worker")
	t.Setenv("AGENTFORGE_OTEL_EXPORTER", "stdout")
	t.Setenv("AGENTFORGE_OTEL_SAMPLE_RATIO", "0.25")

	cfg, err := LoadTelemetryRuntimeConfigFromEnv("fallback")
	if err != nil {
		t.Fatalf("LoadTelemetryRuntimeConfigFromEnv error: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("expected telemetry enabled")
	}
	if cfg.ServiceName != "agentforge-worker" {
		t.Fatalf("unexpected service name: %s", cfg.ServiceName)
	}
	if cfg.Exporter != "stdout" {
		t.Fatalf("unexpected exporter: %s", cfg.Exporter)
	}
	if cfg.SampleRatio != 0.25 {
		t.Fatalf("unexpected sample ratio: %v", cfg.SampleRatio)
	}
}

func TestLoadTelemetryRuntimeConfigFromEnvRejectsInvalidExporter(t *testing.T) {
	t.Setenv("AGENTFORGE_OTEL_EXPORTER", "invalid")
	if _, err := LoadTelemetryRuntimeConfigFromEnv("agentforge"); err == nil {
		t.Fatal("expected invalid exporter error")
	}
}

func TestLoadTelemetryRuntimeConfigFromEnvRejectsInvalidSampleRatio(t *testing.T) {
	t.Setenv("AGENTFORGE_OTEL_SAMPLE_RATIO", "1.5")
	if _, err := LoadTelemetryRuntimeConfigFromEnv("agentforge"); err == nil {
		t.Fatal("expected invalid sample ratio error")
	}
}
