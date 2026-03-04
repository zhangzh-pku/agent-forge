// Package model defines the core domain types for AgentForge.
package model

import "time"

// TaskStatus represents the lifecycle state of a task.
type TaskStatus string

const (
	TaskStatusQueued    TaskStatus = "QUEUED"
	TaskStatusRunning   TaskStatus = "RUNNING"
	TaskStatusSucceeded TaskStatus = "SUCCEEDED"
	TaskStatusFailed    TaskStatus = "FAILED"
	TaskStatusAborted   TaskStatus = "ABORTED"
)

// RunStatus represents the lifecycle state of a run attempt.
type RunStatus string

const (
	RunStatusQueued    RunStatus = "RUN_QUEUED"
	RunStatusRunning   RunStatus = "RUNNING"
	RunStatusSucceeded RunStatus = "SUCCEEDED"
	RunStatusFailed    RunStatus = "FAILED"
	RunStatusAborted   RunStatus = "ABORTED"
)

// StepType classifies the kind of work in a step.
type StepType string

const (
	StepTypeLLMCall     StepType = "llm_call"
	StepTypeToolCall    StepType = "tool_call"
	StepTypeObservation StepType = "observation"
	StepTypeFinal       StepType = "final"
)

// StepStatus indicates whether a step succeeded or failed.
type StepStatus string

const (
	StepStatusOK    StepStatus = "OK"
	StepStatusError StepStatus = "ERROR"
)

// Task is the top-level entity representing a user-submitted agent task.
type Task struct {
	TaskID         string       `json:"task_id"`
	TenantID       string       `json:"tenant_id"`
	UserID         string       `json:"user_id"`
	Status         TaskStatus   `json:"status"`
	ActiveRunID    string       `json:"active_run_id"`
	AbortRequested bool         `json:"abort_requested"`
	AbortReason    string       `json:"abort_reason,omitempty"`
	AbortTS        *time.Time   `json:"abort_ts,omitempty"`
	IdempotencyKey string       `json:"idempotency_key,omitempty"`
	Prompt         string       `json:"prompt"`
	ModelConfig    *ModelConfig `json:"model_config,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
}

// Run represents a single execution attempt of a task.
type Run struct {
	TaskID              string       `json:"task_id"`
	RunID               string       `json:"run_id"`
	TenantID            string       `json:"tenant_id"`
	Status              RunStatus    `json:"status"`
	QueuedAt            *time.Time   `json:"queued_at,omitempty"`
	ParentRunID         string       `json:"parent_run_id,omitempty"`
	ResumeFromStepIndex *int         `json:"resume_from_step_index,omitempty"`
	ModelConfig         *ModelConfig `json:"model_config,omitempty"`
	StartedAt           *time.Time   `json:"started_at,omitempty"`
	EndedAt             *time.Time   `json:"ended_at,omitempty"`
	LastStepIndex       int          `json:"last_step_index"`
	TotalTokenUsage     *TokenUsage  `json:"total_token_usage,omitempty"`
	TotalCostUSD        float64      `json:"total_cost_usd,omitempty"`
}

// Step represents a single execution step within a run.
type Step struct {
	RunID         string         `json:"run_id"`
	StepIndex     int            `json:"step_index"`
	Type          StepType       `json:"type"`
	Status        StepStatus     `json:"status"`
	Input         string         `json:"input,omitempty"`
	Output        string         `json:"output,omitempty"`
	TSStart       time.Time      `json:"ts_start"`
	TSEnd         time.Time      `json:"ts_end"`
	LatencyMS     int64          `json:"latency_ms"`
	TokenUsage    *TokenUsage    `json:"token_usage,omitempty"`
	CheckpointRef *CheckpointRef `json:"checkpoint_ref,omitempty"`
	ErrorCode     string         `json:"error_code,omitempty"`
	ErrorMessage  string         `json:"error_message,omitempty"`
}

// TokenUsage tracks LLM token consumption.
type TokenUsage struct {
	Input  int `json:"input"`
	Output int `json:"output"`
	Total  int `json:"total"`
}

// CheckpointRef holds S3 references for memory and workspace snapshots.
type CheckpointRef struct {
	Memory    *ArtifactRef `json:"memory,omitempty"`
	Workspace *ArtifactRef `json:"workspace,omitempty"`
}

// ArtifactRef is a pointer to an S3 object.
type ArtifactRef struct {
	S3Key  string `json:"s3_key"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// ModelConfig holds LLM configuration for a run.
type ModelConfig struct {
	ModelID          string   `json:"model_id"`
	Temperature      float64  `json:"temperature,omitempty"`
	MaxTokens        int      `json:"max_tokens,omitempty"`
	PolicyMode       string   `json:"policy_mode,omitempty"`
	CostCapUSD       float64  `json:"cost_cap_usd,omitempty"`
	FallbackModelIDs []string `json:"fallback_model_ids,omitempty"`
}

// Connection represents a WebSocket connection.
type Connection struct {
	ConnectionID string    `json:"connection_id"`
	TenantID     string    `json:"tenant_id"`
	UserID       string    `json:"user_id"`
	TaskID       string    `json:"task_id"`
	RunID        string    `json:"run_id"`
	ConnectedAt  time.Time `json:"connected_at"`
	TTL          int64     `json:"ttl"`
}

// SQSMessage is the pointer message sent via the task queue.
type SQSMessage struct {
	TenantID        string  `json:"tenant_id"`
	TaskID          string  `json:"task_id"`
	RunID           string  `json:"run_id"`
	TaskType        string  `json:"task_type,omitempty"`
	SubmittedAt     int64   `json:"submitted_at"`
	Attempt         int     `json:"attempt"`
	DedupeKey       string  `json:"dedupe_key,omitempty"`
	EstimatedTokens int64   `json:"estimated_tokens,omitempty"`
	EstimatedCost   float64 `json:"estimated_cost_usd,omitempty"`
}
