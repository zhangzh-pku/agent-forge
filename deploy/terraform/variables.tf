# =============================================================================
# AgentForge - Terraform Variables
# =============================================================================

variable "region" {
  description = "AWS region for all resources"
  type        = string
  default     = "us-east-1"
}

variable "environment" {
  description = "Deployment environment (e.g., dev, staging, prod). Used as a suffix on resource names to allow multiple environments in the same account."
  type        = string
  default     = "dev"

  validation {
    condition     = contains(["dev", "staging", "prod"], var.environment)
    error_message = "Environment must be one of: dev, staging, prod."
  }
}

variable "kms_key_arn" {
  description = "ARN of the KMS key for S3 server-side encryption. Leave empty to use the default aws/s3 managed key."
  type        = string
  default     = ""
}

variable "log_group_kms_key_arn" {
  description = "Optional existing CMK ARN for CloudWatch log group encryption. Leave empty to provision a dedicated CMK."
  type        = string
  default     = ""
}

variable "log_retention_days" {
  description = "CloudWatch log retention period (days) for Lambda log groups."
  type        = number
  default     = 30

  validation {
    condition = contains([
      1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180,
      365, 400, 545, 731, 1096, 1827, 2192, 2557,
      2922, 3288, 3653,
    ], var.log_retention_days)
    error_message = "log_retention_days must be a valid CloudWatch Logs retention value."
  }
}

variable "openai_api_key_secret_arn" {
  description = "Secrets Manager secret ARN used to resolve OPENAI_API_KEY at runtime (task_api/worker)."
  type        = string
  default     = ""

  validation {
    condition     = var.openai_api_key_secret_arn == "" || can(regex("^arn:aws[a-z-]*:secretsmanager:[^:]+:[0-9]{12}:secret:.+", var.openai_api_key_secret_arn))
    error_message = "openai_api_key_secret_arn must be empty or a full Secrets Manager secret ARN."
  }
}

variable "openai_api_key_secret_field" {
  description = "Optional JSON field in the secret payload that holds OPENAI_API_KEY."
  type        = string
  default     = ""
}

variable "task_api_lambda_package_path" {
  description = "Path to the task_api Lambda zip package (must contain bootstrap at archive root)."
  type        = string
  default     = "dist/task_api.zip"
}

variable "worker_lambda_package_path" {
  description = "Path to the worker Lambda zip package (must contain bootstrap at archive root)."
  type        = string
  default     = "dist/worker.zip"
}

variable "recovery_lambda_package_path" {
  description = "Path to the recovery Lambda zip package (must contain bootstrap at archive root)."
  type        = string
  default     = "dist/recovery.zip"
}

variable "ws_connect_lambda_package_path" {
  description = "Path to the ws_connect Lambda zip package (must contain bootstrap at archive root)."
  type        = string
  default     = "dist/ws_connect.zip"
}

variable "ws_disconnect_lambda_package_path" {
  description = "Path to the ws_disconnect Lambda zip package (must contain bootstrap at archive root)."
  type        = string
  default     = "dist/ws_disconnect.zip"
}

variable "http_allowed_origins" {
  description = "CORS allowlist for HTTP API origins."
  type        = list(string)
  default     = ["http://localhost:3000"]
}

variable "waf_enabled" {
  description = "Enable WAF for the HTTP API stage."
  type        = bool
  default     = true
}

variable "dashboard_enabled" {
  description = "Enable a CloudWatch dashboard with core runtime metrics."
  type        = bool
  default     = true
}

variable "http_jwt_authorizer_enabled" {
  description = "Enable JWT authorizer for HTTP API routes."
  type        = bool
  default     = false
}

variable "http_jwt_authorizer_issuer" {
  description = "JWT issuer URL for HTTP API authorizer (for example Cognito issuer URL)."
  type        = string
  default     = ""
}

variable "http_jwt_authorizer_audiences" {
  description = "Allowed JWT audiences for HTTP API authorizer."
  type        = list(string)
  default     = []
}

variable "websocket_authorizer_enabled" {
  description = "Enable CUSTOM (Lambda REQUEST) authorizer for WebSocket $connect route."
  type        = bool
  default     = false
}

variable "websocket_authorizer_lambda_arn" {
  description = "Lambda function ARN used by the WebSocket CUSTOM authorizer."
  type        = string
  default     = ""
}

variable "websocket_authorizer_identity_sources" {
  description = "Identity sources for WebSocket CUSTOM authorizer."
  type        = list(string)
  default     = ["route.request.header.Authorization"]
}

variable "recovery_enabled" {
  description = "Enable EventBridge scheduled stale-run recovery Lambda."
  type        = bool
  default     = true
}

variable "recovery_schedule_expression" {
  description = "EventBridge schedule expression for the recovery Lambda."
  type        = string
  default     = "rate(5 minutes)"
}

variable "recovery_tenant_id" {
  description = "Optional tenant scope for recovery (empty = all tenants)."
  type        = string
  default     = ""
}

variable "recovery_stale_for" {
  description = "Stale threshold passed to the recovery job."
  type        = string
  default     = "10m"
}

variable "recovery_limit" {
  description = "Maximum runs/tasks scanned per recovery pass."
  type        = number
  default     = 200

  validation {
    condition     = var.recovery_limit > 0
    error_message = "recovery_limit must be greater than 0."
  }
}

variable "recovery_consistency_check" {
  description = "Run consistency check after stale-run recovery."
  type        = bool
  default     = false
}

variable "recovery_consistency_repair" {
  description = "Apply consistency repair (requires consistency check)."
  type        = bool
  default     = false
}

variable "recovery_alarm_actions" {
  description = "CloudWatch alarm action ARNs for recovery Lambda errors."
  type        = list(string)
  default     = []
}

variable "otel_enabled" {
  description = "Enable application-side OpenTelemetry spans in Lambda entrypoints."
  type        = bool
  default     = true
}

variable "otel_exporter" {
  description = "OpenTelemetry exporter mode for app spans: none or stdout."
  type        = string
  default     = "none"

  validation {
    condition     = contains(["none", "stdout"], var.otel_exporter)
    error_message = "otel_exporter must be one of: none, stdout."
  }
}

variable "otel_sample_ratio" {
  description = "OpenTelemetry sampling ratio in [0,1]."
  type        = number
  default     = 1

  validation {
    condition     = var.otel_sample_ratio >= 0 && var.otel_sample_ratio <= 1
    error_message = "otel_sample_ratio must be between 0 and 1."
  }
}
