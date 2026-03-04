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

  validation {
    condition = !var.http_jwt_authorizer_enabled || (
      trimspace(var.http_jwt_authorizer_issuer) != "" &&
      length(var.http_jwt_authorizer_audiences) > 0
    )
    error_message = "http_jwt_authorizer_enabled=true requires http_jwt_authorizer_issuer and at least one http_jwt_authorizer_audiences value."
  }
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

  validation {
    condition     = !var.websocket_authorizer_enabled || trimspace(var.websocket_authorizer_lambda_arn) != ""
    error_message = "websocket_authorizer_enabled=true requires websocket_authorizer_lambda_arn."
  }
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

  validation {
    condition     = !var.recovery_consistency_repair || var.recovery_consistency_check
    error_message = "recovery_consistency_repair=true requires recovery_consistency_check=true."
  }
}

variable "recovery_alarm_actions" {
  description = "CloudWatch alarm action ARNs for recovery Lambda errors."
  type        = list(string)
  default     = []
}
