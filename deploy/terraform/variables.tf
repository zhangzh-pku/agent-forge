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
