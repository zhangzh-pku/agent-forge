# =============================================================================
# AgentForge - Terraform Infrastructure
# =============================================================================
# This configuration provisions the core AWS infrastructure for AgentForge:
#   - SQS queues for async task processing
#   - DynamoDB tables for task, run, step, and connection state
#   - S3 bucket for artifact storage
#   - Lambda functions for API, worker, and WebSocket handlers
#   - API Gateway (HTTP + WebSocket) for external access
# =============================================================================

terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.34"
    }
  }
}

# -----------------------------------------------------------------------------
# Provider
# -----------------------------------------------------------------------------

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project     = "AgentForge"
      Environment = var.environment
      ManagedBy   = "terraform"
    }
  }
}

# -----------------------------------------------------------------------------
# Data Sources
# -----------------------------------------------------------------------------

data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

# =============================================================================
# SQS - Task Queue with Dead-Letter Queue
# =============================================================================

# Dead-letter queue receives messages that fail processing after max retries.
# Monitor this queue to detect failing tasks and investigate root causes.
resource "aws_sqs_queue" "tasks_dlq" {
  name                       = "agentforge-tasks-dlq-${var.environment}"
  message_retention_seconds  = 1209600 # 14 days - retain failed messages for debugging
  visibility_timeout_seconds = 300

  # SSE-KMS encryption at rest
  sqs_managed_sse_enabled = var.kms_key_arn == "" ? true : false
  kms_master_key_id       = var.kms_key_arn != "" ? var.kms_key_arn : null

  tags = {
    Name = "agentforge-tasks-dlq"
  }
}

# Primary task queue - workers poll this queue for new tasks to execute.
# Messages that fail 3 times are redirected to the DLQ for manual inspection.
resource "aws_sqs_queue" "tasks" {
  name                       = "agentforge-tasks-${var.environment}"
  visibility_timeout_seconds = 900     # 15 min - must exceed Lambda timeout
  message_retention_seconds  = 1209600 # 14 days - align with DLQ for longer replay window
  receive_wait_time_seconds  = 20      # Long polling to reduce empty receives

  # SSE-KMS encryption at rest
  sqs_managed_sse_enabled = var.kms_key_arn == "" ? true : false
  kms_master_key_id       = var.kms_key_arn != "" ? var.kms_key_arn : null

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.tasks_dlq.arn
    maxReceiveCount     = 3
  })

  tags = {
    Name = "agentforge-tasks"
  }
}

# =============================================================================
# DynamoDB Tables
# =============================================================================
# All tables use a composite primary key (pk + sk) to support flexible
# access patterns via key overloading. Billing is on-demand (PAY_PER_REQUEST)
# to handle bursty agent workloads without capacity planning.
# =============================================================================

# Stores top-level task records: status, metadata, ownership, timestamps.
resource "aws_dynamodb_table" "tasks" {
  name         = "agentforge-tasks-${var.environment}"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  range_key    = "sk"

  attribute {
    name = "pk"
    type = "S"
  }

  attribute {
    name = "sk"
    type = "S"
  }

  # GSI keys for tenant-scoped task listing without full table scans.
  attribute {
    name = "gsi1pk"
    type = "S"
  }

  attribute {
    name = "gsi1sk"
    type = "S"
  }

  # GSI keys for global task listing via Query (no table Scan).
  attribute {
    name = "gsi2pk"
    type = "S"
  }

  attribute {
    name = "gsi2sk"
    type = "S"
  }

  global_secondary_index {
    name            = "tenant-created-index"
    hash_key        = "gsi1pk"
    range_key       = "gsi1sk"
    projection_type = "ALL"
  }

  global_secondary_index {
    name            = "entity-created-index"
    hash_key        = "gsi2pk"
    range_key       = "gsi2sk"
    projection_type = "ALL"
  }

  point_in_time_recovery {
    enabled = true
  }

  server_side_encryption {
    enabled     = true
    kms_key_arn = var.kms_key_arn != "" ? var.kms_key_arn : null
  }

  tags = {
    Name = "agentforge-tasks"
  }
}

# Stores execution run records: one run per task attempt, tracks overall
# progress, start/end times, and final status.
resource "aws_dynamodb_table" "runs" {
  name         = "agentforge-runs-${var.environment}"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  range_key    = "sk"

  attribute {
    name = "pk"
    type = "S"
  }

  attribute {
    name = "sk"
    type = "S"
  }

  # GSI keys for tenant-scoped run listing without full table scans.
  attribute {
    name = "gsi1pk"
    type = "S"
  }

  attribute {
    name = "gsi1sk"
    type = "S"
  }

  # GSI keys for global run listing via Query (no table Scan).
  attribute {
    name = "gsi2pk"
    type = "S"
  }

  attribute {
    name = "gsi2sk"
    type = "S"
  }

  global_secondary_index {
    name            = "tenant-run-index"
    hash_key        = "gsi1pk"
    range_key       = "gsi1sk"
    projection_type = "ALL"
  }

  global_secondary_index {
    name            = "entity-run-index"
    hash_key        = "gsi2pk"
    range_key       = "gsi2sk"
    projection_type = "ALL"
  }

  point_in_time_recovery {
    enabled = true
  }

  server_side_encryption {
    enabled     = true
    kms_key_arn = var.kms_key_arn != "" ? var.kms_key_arn : null
  }

  tags = {
    Name = "agentforge-runs"
  }
}

# Stores individual step records within a run: tool calls, LLM invocations,
# reasoning traces, and intermediate outputs.
resource "aws_dynamodb_table" "steps" {
  name         = "agentforge-steps-${var.environment}"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  range_key    = "sk"

  attribute {
    name = "pk"
    type = "S"
  }

  attribute {
    name = "sk"
    type = "S"
  }

  point_in_time_recovery {
    enabled = true
  }

  server_side_encryption {
    enabled     = true
    kms_key_arn = var.kms_key_arn != "" ? var.kms_key_arn : null
  }

  # Enable TTL for stream events. Only items with "ttl" are expired;
  # step records without that attribute remain intact.
  ttl {
    attribute_name = "ttl"
    enabled        = true
  }

  tags = {
    Name = "agentforge-steps"
  }
}

# Stores WebSocket connection records for real-time streaming. The GSI
# allows efficient lookup of all connections subscribed to a given task,
# so the worker can fan out progress updates.
resource "aws_dynamodb_table" "connections" {
  name         = "agentforge-connections-${var.environment}"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  range_key    = "sk"

  attribute {
    name = "pk"
    type = "S"
  }

  attribute {
    name = "sk"
    type = "S"
  }

  attribute {
    name = "gsi1pk"
    type = "S"
  }

  attribute {
    name = "gsi1sk"
    type = "S"
  }

  # GSI: look up connections by task ID for streaming fan-out.
  # Example: gsi1pk = "TASK#<task-id>", gsi1sk = "CONN#<connection-id>"
  global_secondary_index {
    name            = "task-index"
    hash_key        = "gsi1pk"
    range_key       = "gsi1sk"
    projection_type = "ALL"
  }

  # Enable TTL so stale WebSocket connections are automatically cleaned up.
  # The "ttl" attribute stores a Unix epoch timestamp; DynamoDB removes items
  # after this time (typically within 48 hours of expiry).
  ttl {
    attribute_name = "ttl"
    enabled        = true
  }

  point_in_time_recovery {
    enabled = true
  }

  server_side_encryption {
    enabled     = true
    kms_key_arn = var.kms_key_arn != "" ? var.kms_key_arn : null
  }

  tags = {
    Name = "agentforge-connections"
  }
}

# =============================================================================
# S3 - Artifact Storage
# =============================================================================
# Stores task artifacts (generated files, logs, tool outputs). Encrypted with
# KMS, versioned for auditability, and fully locked down against public access.
# =============================================================================

resource "aws_s3_bucket" "artifacts" {
  bucket = "agentforge-artifacts-${var.environment}-${data.aws_caller_identity.current.account_id}"

  tags = {
    Name = "agentforge-artifacts"
  }
}

# Enable versioning so artifacts are never silently overwritten.
resource "aws_s3_bucket_versioning" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  versioning_configuration {
    status = "Enabled"
  }
}

# Encrypt all objects at rest with KMS. If no key ARN is provided,
# the default aws/s3 KMS key is used.
resource "aws_s3_bucket_server_side_encryption_configuration" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = var.kms_key_arn != "" ? var.kms_key_arn : null
    }
    bucket_key_enabled = true
  }
}

# Block all public access - artifacts must never be exposed to the internet.
resource "aws_s3_bucket_public_access_block" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

data "aws_iam_policy_document" "artifacts_tls_only" {
  statement {
    sid    = "DenyInsecureTransport"
    effect = "Deny"

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    actions = ["s3:*"]
    resources = [
      aws_s3_bucket.artifacts.arn,
      "${aws_s3_bucket.artifacts.arn}/*",
    ]

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }
}

resource "aws_s3_bucket_policy" "artifacts_tls_only" {
  bucket = aws_s3_bucket.artifacts.id
  policy = data.aws_iam_policy_document.artifacts_tls_only.json
}

# Lifecycle policy for cost control and bounded storage growth:
# - Current versions transition to STANDARD_IA after 30 days.
# - Noncurrent versions expire after 180 days.
resource "aws_s3_bucket_lifecycle_configuration" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  rule {
    id     = "artifacts-tiering-and-expiry"
    status = "Enabled"

    transition {
      days          = 30
      storage_class = "STANDARD_IA"
    }

    noncurrent_version_expiration {
      noncurrent_days = 180
    }
  }
}

# Dedicated log bucket for S3 server access logs.
#tfsec:ignore:aws-s3-enable-bucket-logging Access-log sink bucket is the terminal log destination; enabling logging on it would recurse.
resource "aws_s3_bucket" "artifacts_access_logs" {
  bucket = "agentforge-artifacts-logs-${var.environment}-${data.aws_caller_identity.current.account_id}"

  tags = {
    Name = "agentforge-artifacts-access-logs"
  }
}

resource "aws_s3_bucket_versioning" "artifacts_access_logs" {
  bucket = aws_s3_bucket.artifacts_access_logs.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "artifacts_access_logs" {
  bucket = aws_s3_bucket.artifacts_access_logs.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = var.kms_key_arn != "" ? var.kms_key_arn : null
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "artifacts_access_logs" {
  bucket = aws_s3_bucket.artifacts_access_logs.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

data "aws_iam_policy_document" "artifacts_access_logs" {
  statement {
    sid    = "DenyInsecureTransport"
    effect = "Deny"

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    actions = ["s3:*"]
    resources = [
      aws_s3_bucket.artifacts_access_logs.arn,
      "${aws_s3_bucket.artifacts_access_logs.arn}/*",
    ]

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }

  statement {
    sid    = "AllowS3ServerAccessLogsAclCheck"
    effect = "Allow"

    principals {
      type        = "Service"
      identifiers = ["logging.s3.amazonaws.com"]
    }

    actions = ["s3:GetBucketAcl"]
    resources = [
      aws_s3_bucket.artifacts_access_logs.arn,
    ]

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }

  statement {
    sid    = "AllowS3ServerAccessLogsWrite"
    effect = "Allow"

    principals {
      type        = "Service"
      identifiers = ["logging.s3.amazonaws.com"]
    }

    actions = ["s3:PutObject"]
    resources = [
      "${aws_s3_bucket.artifacts_access_logs.arn}/s3-access/*",
    ]

    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = [aws_s3_bucket.artifacts.arn]
    }

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

resource "aws_s3_bucket_policy" "artifacts_access_logs" {
  bucket = aws_s3_bucket.artifacts_access_logs.id
  policy = data.aws_iam_policy_document.artifacts_access_logs.json
}

resource "aws_s3_bucket_logging" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  target_bucket = aws_s3_bucket.artifacts_access_logs.id
  target_prefix = "s3-access/"

  depends_on = [aws_s3_bucket_policy.artifacts_access_logs]
}

# =============================================================================
# KMS - CloudWatch Logs Encryption
# =============================================================================

locals {
  log_group_kms_key_arn = trimspace(var.log_group_kms_key_arn) != "" ? trimspace(var.log_group_kms_key_arn) : aws_kms_key.cloudwatch_logs[0].arn
}

data "aws_iam_policy_document" "cloudwatch_logs_kms" {
  statement {
    sid    = "EnableRootPermissions"
    effect = "Allow"

    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:root"]
    }

    actions   = ["kms:*"]
    resources = ["*"]
  }

  statement {
    sid    = "AllowCloudWatchLogsUsage"
    effect = "Allow"

    principals {
      type        = "Service"
      identifiers = ["logs.${data.aws_region.current.name}.amazonaws.com"]
    }

    actions = [
      "kms:Encrypt",
      "kms:Decrypt",
      "kms:ReEncrypt*",
      "kms:GenerateDataKey*",
      "kms:DescribeKey",
    ]
    resources = ["*"]

    condition {
      test     = "ArnLike"
      variable = "kms:EncryptionContext:aws:logs:arn"
      values   = ["arn:aws:logs:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:log-group:*"]
    }
  }
}

resource "aws_kms_key" "cloudwatch_logs" {
  count                   = trimspace(var.log_group_kms_key_arn) == "" ? 1 : 0
  description             = "AgentForge CloudWatch Logs CMK (${var.environment})"
  deletion_window_in_days = 30
  enable_key_rotation     = true
  policy                  = data.aws_iam_policy_document.cloudwatch_logs_kms.json
}

resource "aws_kms_alias" "cloudwatch_logs" {
  count         = trimspace(var.log_group_kms_key_arn) == "" ? 1 : 0
  name          = "alias/agentforge-cloudwatch-logs-${var.environment}"
  target_key_id = aws_kms_key.cloudwatch_logs[0].key_id
}

# =============================================================================
# IAM - Lambda Execution Roles
# =============================================================================

# Shared assume-role policy allowing Lambda to assume these roles.
data "aws_iam_policy_document" "lambda_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

# --- Task API Lambda Role ---
# Permissions: CloudWatch Logs, DynamoDB (tasks/runs/steps), SQS send, S3 read.
resource "aws_iam_role" "task_api_lambda" {
  name               = "agentforge-task-api-lambda-${var.environment}"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role.json

  tags = {
    Name = "agentforge-task-api-lambda"
  }
}

resource "aws_iam_role_policy_attachment" "task_api_basic_execution" {
  role       = aws_iam_role.task_api_lambda.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

data "aws_iam_policy_document" "task_api_policy" {
  # DynamoDB access for reading/writing task, run, and step records
  statement {
    sid    = "DynamoDBAccess"
    effect = "Allow"
    actions = [
      "dynamodb:GetItem",
      "dynamodb:PutItem",
      "dynamodb:UpdateItem",
      "dynamodb:DeleteItem",
      "dynamodb:Query",
      "dynamodb:TransactWriteItems",
      "dynamodb:BatchWriteItem",
    ]
    resources = [
      aws_dynamodb_table.tasks.arn,
      aws_dynamodb_table.runs.arn,
      aws_dynamodb_table.steps.arn,
      aws_dynamodb_table.connections.arn,
      "${aws_dynamodb_table.connections.arn}/index/*",
    ]
  }

  # SQS access to enqueue new task messages
  statement {
    sid    = "SQSSend"
    effect = "Allow"
    actions = [
      "sqs:SendMessage",
    ]
    resources = [aws_sqs_queue.tasks.arn]
  }

  # S3 read access for returning artifact URLs
  #tfsec:ignore:aws-iam-no-policy-wildcards R-132 (remaining TODO): object-level paths are dynamic at runtime; policy minimization tracked separately.
  statement {
    sid    = "S3Read"
    effect = "Allow"
    actions = [
      "s3:GetObject",
    ]
    resources = [
      "${aws_s3_bucket.artifacts.arn}/*",
    ]
  }

  dynamic "statement" {
    for_each = var.openai_api_key_secret_arn != "" ? [1] : []
    content {
      sid    = "SecretsManagerReadOpenAIKey"
      effect = "Allow"
      actions = [
        "secretsmanager:GetSecretValue",
        "secretsmanager:DescribeSecret",
      ]
      resources = [var.openai_api_key_secret_arn]
    }
  }

  dynamic "statement" {
    for_each = var.kms_key_arn != "" ? [1] : []
    content {
      sid    = "KMSDecryptArtifacts"
      effect = "Allow"
      actions = [
        "kms:Decrypt",
        "kms:DescribeKey",
      ]
      resources = [var.kms_key_arn]
    }
  }
}

resource "aws_iam_role_policy" "task_api" {
  name   = "agentforge-task-api-policy"
  role   = aws_iam_role.task_api_lambda.id
  policy = data.aws_iam_policy_document.task_api_policy.json
}

# --- Worker Lambda Role ---
# Permissions: CloudWatch Logs, DynamoDB (all tables), SQS consume, S3 read/write.
resource "aws_iam_role" "worker_lambda" {
  name               = "agentforge-worker-lambda-${var.environment}"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role.json

  tags = {
    Name = "agentforge-worker-lambda"
  }
}

resource "aws_iam_role_policy_attachment" "worker_basic_execution" {
  role       = aws_iam_role.worker_lambda.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

data "aws_iam_policy_document" "worker_policy" {
  # Worker reads tasks/runs and writes runs/steps while streaming updates.
  statement {
    sid    = "DynamoDBAccess"
    effect = "Allow"
    actions = [
      "dynamodb:GetItem",
      "dynamodb:PutItem",
      "dynamodb:UpdateItem",
      "dynamodb:DeleteItem",
      "dynamodb:Query",
    ]
    resources = [
      aws_dynamodb_table.tasks.arn,
      aws_dynamodb_table.runs.arn,
      aws_dynamodb_table.steps.arn,
      aws_dynamodb_table.connections.arn,
      "${aws_dynamodb_table.connections.arn}/index/*",
    ]
  }

  # SQS consume - receive and delete processed messages
  statement {
    sid    = "SQSConsume"
    effect = "Allow"
    actions = [
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage",
      "sqs:ChangeMessageVisibility",
      "sqs:GetQueueAttributes",
    ]
    resources = [aws_sqs_queue.tasks.arn]
  }

  # S3 read/write for storing and retrieving task artifacts
  #tfsec:ignore:aws-iam-no-policy-wildcards R-132 (remaining TODO): object-level paths are dynamic at runtime; policy minimization tracked separately.
  statement {
    sid    = "S3ReadWrite"
    effect = "Allow"
    actions = [
      "s3:GetObject",
      "s3:PutObject",
    ]
    resources = [
      "${aws_s3_bucket.artifacts.arn}/*",
    ]
  }

  dynamic "statement" {
    for_each = var.openai_api_key_secret_arn != "" ? [1] : []
    content {
      sid    = "SecretsManagerReadOpenAIKey"
      effect = "Allow"
      actions = [
        "secretsmanager:GetSecretValue",
        "secretsmanager:DescribeSecret",
      ]
      resources = [var.openai_api_key_secret_arn]
    }
  }

  dynamic "statement" {
    for_each = var.kms_key_arn != "" ? [1] : []
    content {
      sid    = "KMSS3Artifacts"
      effect = "Allow"
      actions = [
        "kms:Encrypt",
        "kms:Decrypt",
        "kms:GenerateDataKey",
        "kms:DescribeKey",
      ]
      resources = [var.kms_key_arn]
    }
  }

  # Allow worker to post messages back through the WebSocket API
  statement {
    sid    = "WebSocketPost"
    effect = "Allow"
    actions = [
      "execute-api:ManageConnections",
    ]
    resources = [
      "${aws_apigatewayv2_api.websocket.execution_arn}/${var.environment}/POST/@connections/*",
    ]
  }
}

resource "aws_iam_role_policy" "worker" {
  name   = "agentforge-worker-policy"
  role   = aws_iam_role.worker_lambda.id
  policy = data.aws_iam_policy_document.worker_policy.json
}

# --- Recovery Lambda Role ---
# Permissions are scoped to stale-run recovery and optional consistency checks.
resource "aws_iam_role" "recovery_lambda" {
  name               = "agentforge-recovery-lambda-${var.environment}"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role.json

  tags = {
    Name = "agentforge-recovery-lambda"
  }
}

resource "aws_iam_role_policy_attachment" "recovery_basic_execution" {
  role       = aws_iam_role.recovery_lambda.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

data "aws_iam_policy_document" "recovery_policy" {
  # Recovery requires tenant/global scans plus targeted run/task updates.
  statement {
    sid    = "DynamoDBRecoveryAccess"
    effect = "Allow"
    actions = [
      "dynamodb:GetItem",
      "dynamodb:UpdateItem",
      "dynamodb:Query",
    ]
    resources = [
      aws_dynamodb_table.tasks.arn,
      "${aws_dynamodb_table.tasks.arn}/index/tenant-created-index",
      "${aws_dynamodb_table.tasks.arn}/index/entity-created-index",
      aws_dynamodb_table.runs.arn,
      "${aws_dynamodb_table.runs.arn}/index/tenant-run-index",
      "${aws_dynamodb_table.runs.arn}/index/entity-run-index",
      aws_dynamodb_table.steps.arn,
    ]
  }

  statement {
    sid    = "SQSRedriveSend"
    effect = "Allow"
    actions = [
      "sqs:SendMessage",
    ]
    resources = [aws_sqs_queue.tasks.arn]
  }

  dynamic "statement" {
    for_each = var.kms_key_arn != "" ? [1] : []
    content {
      sid    = "KMSQueueAndEnv"
      effect = "Allow"
      actions = [
        "kms:Encrypt",
        "kms:Decrypt",
        "kms:GenerateDataKey",
        "kms:DescribeKey",
      ]
      resources = [var.kms_key_arn]
    }
  }
}

resource "aws_iam_role_policy" "recovery" {
  name   = "agentforge-recovery-policy"
  role   = aws_iam_role.recovery_lambda.id
  policy = data.aws_iam_policy_document.recovery_policy.json
}

# --- WebSocket Lambda Role ---
# Permissions: CloudWatch Logs, DynamoDB (connections table only).
resource "aws_iam_role" "websocket_lambda" {
  name               = "agentforge-websocket-lambda-${var.environment}"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role.json

  tags = {
    Name = "agentforge-websocket-lambda"
  }
}

resource "aws_iam_role_policy_attachment" "websocket_basic_execution" {
  role       = aws_iam_role.websocket_lambda.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

data "aws_iam_policy_document" "websocket_policy" {
  # DynamoDB access for the connections table and its GSI
  statement {
    sid    = "DynamoDBConnections"
    effect = "Allow"
    actions = [
      "dynamodb:GetItem",
      "dynamodb:PutItem",
      "dynamodb:UpdateItem",
      "dynamodb:DeleteItem",
      "dynamodb:Query",
    ]
    resources = [
      aws_dynamodb_table.connections.arn,
      "${aws_dynamodb_table.connections.arn}/index/*",
    ]
  }

  # Read-only access to tasks table for ownership validation on $connect
  statement {
    sid    = "DynamoDBTasksReadOnly"
    effect = "Allow"
    actions = [
      "dynamodb:GetItem",
    ]
    resources = [
      aws_dynamodb_table.tasks.arn,
    ]
  }
}

resource "aws_iam_role_policy" "websocket" {
  name   = "agentforge-websocket-policy"
  role   = aws_iam_role.websocket_lambda.id
  policy = data.aws_iam_policy_document.websocket_policy.json
}

# =============================================================================
# Lambda Functions
# =============================================================================
# Lambda packages must be real build artifacts (bootstrap at archive root).
# Defaults point to deploy/terraform/dist/*.zip, generated via:
#   make build-lambda-zip
# =============================================================================

locals {
  task_api_lambda_filename         = trimspace(var.task_api_lambda_package_path)
  task_api_lambda_source_code_hash = fileexists(local.task_api_lambda_filename) ? filebase64sha256(local.task_api_lambda_filename) : null

  worker_lambda_filename         = trimspace(var.worker_lambda_package_path)
  worker_lambda_source_code_hash = fileexists(local.worker_lambda_filename) ? filebase64sha256(local.worker_lambda_filename) : null

  recovery_lambda_filename         = trimspace(var.recovery_lambda_package_path)
  recovery_lambda_source_code_hash = fileexists(local.recovery_lambda_filename) ? filebase64sha256(local.recovery_lambda_filename) : null

  ws_connect_lambda_filename         = trimspace(var.ws_connect_lambda_package_path)
  ws_connect_lambda_source_code_hash = fileexists(local.ws_connect_lambda_filename) ? filebase64sha256(local.ws_connect_lambda_filename) : null

  ws_disconnect_lambda_filename         = trimspace(var.ws_disconnect_lambda_package_path)
  ws_disconnect_lambda_source_code_hash = fileexists(local.ws_disconnect_lambda_filename) ? filebase64sha256(local.ws_disconnect_lambda_filename) : null
}

# --- Task API Lambda ---
# Handles REST API requests: create task, get task status, list runs, etc.
resource "aws_lambda_function" "task_api" {
  function_name                  = "agentforge-task-api-${var.environment}"
  role                           = aws_iam_role.task_api_lambda.arn
  handler                        = "bootstrap"
  runtime                        = "provided.al2023"
  architectures                  = ["arm64"]
  timeout                        = 30
  memory_size                    = 256
  reserved_concurrent_executions = 50
  kms_key_arn                    = var.kms_key_arn != "" ? var.kms_key_arn : null

  filename         = local.task_api_lambda_filename
  source_code_hash = local.task_api_lambda_source_code_hash

  environment {
    variables = {
      AGENTFORGE_RUNTIME           = "aws"
      ENVIRONMENT                  = var.environment
      TASKS_TABLE                  = aws_dynamodb_table.tasks.name
      RUNS_TABLE                   = aws_dynamodb_table.runs.name
      STEPS_TABLE                  = aws_dynamodb_table.steps.name
      CONNECTIONS_TABLE            = aws_dynamodb_table.connections.name
      TASK_QUEUE_URL               = aws_sqs_queue.tasks.url
      ARTIFACTS_BUCKET             = aws_s3_bucket.artifacts.id
      ARTIFACT_SSE_KMS_KEY_ARN     = var.kms_key_arn
      OPENAI_API_KEY_SECRET_ARN    = var.openai_api_key_secret_arn
      OPENAI_API_KEY_SECRET_FIELD  = var.openai_api_key_secret_field
      AGENTFORGE_OTEL_ENABLED      = tostring(var.otel_enabled)
      AGENTFORGE_OTEL_SERVICE_NAME = "agentforge-taskapi"
      AGENTFORGE_OTEL_EXPORTER     = var.otel_exporter
      AGENTFORGE_OTEL_SAMPLE_RATIO = tostring(var.otel_sample_ratio)
    }
  }

  dead_letter_config {
    target_arn = aws_sqs_queue.tasks_dlq.arn
  }

  tracing_config {
    mode = "Active"
  }

  lifecycle {
    precondition {
      condition     = fileexists(local.task_api_lambda_filename)
      error_message = "task_api Lambda package not found. Run 'make build-lambda-zip' or set task_api_lambda_package_path to a valid zip."
    }
  }

  tags = {
    Name = "agentforge-task-api"
  }
}

# --- Worker Lambda ---
# Processes task messages from SQS. Orchestrates agent execution, writes
# step records, stores artifacts, and streams progress via WebSocket.
resource "aws_lambda_function" "worker" {
  function_name = "agentforge-worker-${var.environment}"
  role          = aws_iam_role.worker_lambda.arn
  handler       = "bootstrap"
  runtime       = "provided.al2023"
  architectures = ["arm64"]
  timeout       = 900 # 15 min - matches SQS visibility timeout
  memory_size   = 1024
  # Keep worker concurrency bounded to protect downstream LLM/provider quotas.
  reserved_concurrent_executions = 20
  kms_key_arn                    = var.kms_key_arn != "" ? var.kms_key_arn : null

  filename         = local.worker_lambda_filename
  source_code_hash = local.worker_lambda_source_code_hash

  environment {
    variables = {
      AGENTFORGE_RUNTIME           = "aws"
      ENVIRONMENT                  = var.environment
      TASKS_TABLE                  = aws_dynamodb_table.tasks.name
      RUNS_TABLE                   = aws_dynamodb_table.runs.name
      STEPS_TABLE                  = aws_dynamodb_table.steps.name
      CONNECTIONS_TABLE            = aws_dynamodb_table.connections.name
      TASK_QUEUE_URL               = aws_sqs_queue.tasks.url
      ARTIFACTS_BUCKET             = aws_s3_bucket.artifacts.id
      ARTIFACT_SSE_KMS_KEY_ARN     = var.kms_key_arn
      WEBSOCKET_ENDPOINT           = aws_apigatewayv2_stage.websocket.invoke_url
      OPENAI_API_KEY_SECRET_ARN    = var.openai_api_key_secret_arn
      OPENAI_API_KEY_SECRET_FIELD  = var.openai_api_key_secret_field
      AGENTFORGE_OTEL_ENABLED      = tostring(var.otel_enabled)
      AGENTFORGE_OTEL_SERVICE_NAME = "agentforge-worker"
      AGENTFORGE_OTEL_EXPORTER     = var.otel_exporter
      AGENTFORGE_OTEL_SAMPLE_RATIO = tostring(var.otel_sample_ratio)
    }
  }

  dead_letter_config {
    target_arn = aws_sqs_queue.tasks_dlq.arn
  }

  tracing_config {
    mode = "Active"
  }

  lifecycle {
    precondition {
      condition     = fileexists(local.worker_lambda_filename)
      error_message = "worker Lambda package not found. Run 'make build-lambda-zip' or set worker_lambda_package_path to a valid zip."
    }
  }

  tags = {
    Name = "agentforge-worker"
  }
}

# --- Recovery Lambda ---
# Periodically redrives stale runs and optionally performs consistency repair.
resource "aws_lambda_function" "recovery" {
  function_name                  = "agentforge-recovery-${var.environment}"
  role                           = aws_iam_role.recovery_lambda.arn
  handler                        = "bootstrap"
  runtime                        = "provided.al2023"
  architectures                  = ["arm64"]
  timeout                        = 120
  memory_size                    = 256
  reserved_concurrent_executions = 10
  kms_key_arn                    = var.kms_key_arn != "" ? var.kms_key_arn : null

  filename         = local.recovery_lambda_filename
  source_code_hash = local.recovery_lambda_source_code_hash

  environment {
    variables = {
      AGENTFORGE_RUNTIME                     = "aws"
      ENVIRONMENT                            = var.environment
      TASKS_TABLE                            = aws_dynamodb_table.tasks.name
      RUNS_TABLE                             = aws_dynamodb_table.runs.name
      STEPS_TABLE                            = aws_dynamodb_table.steps.name
      CONNECTIONS_TABLE                      = aws_dynamodb_table.connections.name
      TASK_QUEUE_URL                         = aws_sqs_queue.tasks.url
      ARTIFACTS_BUCKET                       = aws_s3_bucket.artifacts.id
      ARTIFACT_SSE_KMS_KEY_ARN               = var.kms_key_arn
      AGENTFORGE_RECOVERY_ENABLED            = "false"
      AGENTFORGE_RECOVERY_STALE_FOR          = var.recovery_stale_for
      AGENTFORGE_RECOVERY_LIMIT              = tostring(var.recovery_limit)
      AGENTFORGE_RECOVERY_TENANT_ID          = var.recovery_tenant_id
      AGENTFORGE_RECOVERY_CONSISTENCY_CHECK  = tostring(var.recovery_consistency_check)
      AGENTFORGE_RECOVERY_CONSISTENCY_REPAIR = tostring(var.recovery_consistency_repair)
      AGENTFORGE_OTEL_ENABLED                = tostring(var.otel_enabled)
      AGENTFORGE_OTEL_SERVICE_NAME           = "agentforge-recovery"
      AGENTFORGE_OTEL_EXPORTER               = var.otel_exporter
      AGENTFORGE_OTEL_SAMPLE_RATIO           = tostring(var.otel_sample_ratio)
    }
  }

  dead_letter_config {
    target_arn = aws_sqs_queue.tasks_dlq.arn
  }

  tracing_config {
    mode = "Active"
  }

  lifecycle {
    precondition {
      condition     = fileexists(local.recovery_lambda_filename)
      error_message = "recovery Lambda package not found. Run 'make build-lambda-zip' or set recovery_lambda_package_path to a valid zip."
    }
  }

  tags = {
    Name = "agentforge-recovery"
  }
}

# --- WebSocket Connect Lambda ---
# Handles $connect route: validates auth token, stores connection record.
resource "aws_lambda_function" "ws_connect" {
  function_name                  = "agentforge-ws-connect-${var.environment}"
  role                           = aws_iam_role.websocket_lambda.arn
  handler                        = "bootstrap"
  runtime                        = "provided.al2023"
  architectures                  = ["arm64"]
  timeout                        = 10
  memory_size                    = 128
  reserved_concurrent_executions = 50
  kms_key_arn                    = var.kms_key_arn != "" ? var.kms_key_arn : null

  filename         = local.ws_connect_lambda_filename
  source_code_hash = local.ws_connect_lambda_source_code_hash

  environment {
    variables = {
      AGENTFORGE_RUNTIME           = "aws"
      ENVIRONMENT                  = var.environment
      TASKS_TABLE                  = aws_dynamodb_table.tasks.name
      RUNS_TABLE                   = aws_dynamodb_table.runs.name
      STEPS_TABLE                  = aws_dynamodb_table.steps.name
      CONNECTIONS_TABLE            = aws_dynamodb_table.connections.name
      AGENTFORGE_OTEL_ENABLED      = tostring(var.otel_enabled)
      AGENTFORGE_OTEL_SERVICE_NAME = "agentforge-ws-connect"
      AGENTFORGE_OTEL_EXPORTER     = var.otel_exporter
      AGENTFORGE_OTEL_SAMPLE_RATIO = tostring(var.otel_sample_ratio)
    }
  }

  dead_letter_config {
    target_arn = aws_sqs_queue.tasks_dlq.arn
  }

  tracing_config {
    mode = "Active"
  }

  lifecycle {
    precondition {
      condition     = fileexists(local.ws_connect_lambda_filename)
      error_message = "ws_connect Lambda package not found. Run 'make build-lambda-zip' or set ws_connect_lambda_package_path to a valid zip."
    }
  }

  tags = {
    Name = "agentforge-ws-connect"
  }
}

# --- WebSocket Disconnect Lambda ---
# Handles $disconnect route: removes stale connection record from DynamoDB.
resource "aws_lambda_function" "ws_disconnect" {
  function_name                  = "agentforge-ws-disconnect-${var.environment}"
  role                           = aws_iam_role.websocket_lambda.arn
  handler                        = "bootstrap"
  runtime                        = "provided.al2023"
  architectures                  = ["arm64"]
  timeout                        = 10
  memory_size                    = 128
  reserved_concurrent_executions = 50
  kms_key_arn                    = var.kms_key_arn != "" ? var.kms_key_arn : null

  filename         = local.ws_disconnect_lambda_filename
  source_code_hash = local.ws_disconnect_lambda_source_code_hash

  environment {
    variables = {
      AGENTFORGE_RUNTIME           = "aws"
      ENVIRONMENT                  = var.environment
      TASKS_TABLE                  = aws_dynamodb_table.tasks.name
      RUNS_TABLE                   = aws_dynamodb_table.runs.name
      STEPS_TABLE                  = aws_dynamodb_table.steps.name
      CONNECTIONS_TABLE            = aws_dynamodb_table.connections.name
      AGENTFORGE_OTEL_ENABLED      = tostring(var.otel_enabled)
      AGENTFORGE_OTEL_SERVICE_NAME = "agentforge-ws-disconnect"
      AGENTFORGE_OTEL_EXPORTER     = var.otel_exporter
      AGENTFORGE_OTEL_SAMPLE_RATIO = tostring(var.otel_sample_ratio)
    }
  }

  dead_letter_config {
    target_arn = aws_sqs_queue.tasks_dlq.arn
  }

  tracing_config {
    mode = "Active"
  }

  lifecycle {
    precondition {
      condition     = fileexists(local.ws_disconnect_lambda_filename)
      error_message = "ws_disconnect Lambda package not found. Run 'make build-lambda-zip' or set ws_disconnect_lambda_package_path to a valid zip."
    }
  }

  tags = {
    Name = "agentforge-ws-disconnect"
  }
}

# CloudWatch log groups are explicitly managed to enforce retention and CMK encryption.
resource "aws_cloudwatch_log_group" "task_api_lambda" {
  name              = "/aws/lambda/agentforge-task-api-${var.environment}"
  retention_in_days = var.log_retention_days
  kms_key_id        = local.log_group_kms_key_arn
}

resource "aws_cloudwatch_log_group" "worker_lambda" {
  name              = "/aws/lambda/agentforge-worker-${var.environment}"
  retention_in_days = var.log_retention_days
  kms_key_id        = local.log_group_kms_key_arn
}

resource "aws_cloudwatch_log_group" "recovery_lambda" {
  name              = "/aws/lambda/agentforge-recovery-${var.environment}"
  retention_in_days = var.log_retention_days
  kms_key_id        = local.log_group_kms_key_arn
}

resource "aws_cloudwatch_log_group" "ws_connect_lambda" {
  name              = "/aws/lambda/agentforge-ws-connect-${var.environment}"
  retention_in_days = var.log_retention_days
  kms_key_id        = local.log_group_kms_key_arn
}

resource "aws_cloudwatch_log_group" "ws_disconnect_lambda" {
  name              = "/aws/lambda/agentforge-ws-disconnect-${var.environment}"
  retention_in_days = var.log_retention_days
  kms_key_id        = local.log_group_kms_key_arn
}

# =============================================================================
# SQS -> Lambda Event Source Mapping
# =============================================================================
# Triggers the worker Lambda when messages arrive in the task queue.
# Batch size of 1 ensures each task gets the full Lambda timeout.
# =============================================================================

resource "aws_lambda_event_source_mapping" "worker_sqs" {
  event_source_arn                   = aws_sqs_queue.tasks.arn
  function_name                      = aws_lambda_function.worker.arn
  batch_size                         = 1
  maximum_batching_window_in_seconds = 0 # Process immediately, no batching delay
  enabled                            = true

  # Send failed invocations to the DLQ after exhausting retries
  function_response_types = ["ReportBatchItemFailures"]
}

# Scheduled stale-run recovery
resource "aws_cloudwatch_event_rule" "recovery_schedule" {
  count               = var.recovery_enabled ? 1 : 0
  name                = "agentforge-recovery-${var.environment}"
  schedule_expression = var.recovery_schedule_expression
}

resource "aws_cloudwatch_event_target" "recovery_lambda" {
  count     = var.recovery_enabled ? 1 : 0
  rule      = aws_cloudwatch_event_rule.recovery_schedule[0].name
  target_id = "agentforge-recovery"
  arn       = aws_lambda_function.recovery.arn
}

resource "aws_lambda_permission" "recovery_events" {
  count         = var.recovery_enabled ? 1 : 0
  statement_id  = "AllowEventBridgeInvokeRecovery"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.recovery.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.recovery_schedule[0].arn
}

# Recovery Lambda error alarm
resource "aws_cloudwatch_metric_alarm" "recovery_lambda_errors" {
  alarm_name          = "agentforge-recovery-errors-${var.environment}"
  alarm_description   = "Recovery Lambda reported execution errors."
  comparison_operator = "GreaterThanOrEqualToThreshold"
  evaluation_periods  = 1
  metric_name         = "Errors"
  namespace           = "AWS/Lambda"
  period              = 300
  statistic           = "Sum"
  threshold           = 1
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.recovery_alarm_actions
  ok_actions          = var.recovery_alarm_actions

  dimensions = {
    FunctionName = aws_lambda_function.recovery.function_name
  }
}

# Worker Lambda error alarm
resource "aws_cloudwatch_metric_alarm" "worker_lambda_errors" {
  alarm_name          = "agentforge-worker-errors-${var.environment}"
  alarm_description   = "Worker Lambda reported execution errors."
  comparison_operator = "GreaterThanOrEqualToThreshold"
  evaluation_periods  = 1
  metric_name         = "Errors"
  namespace           = "AWS/Lambda"
  period              = 300
  statistic           = "Sum"
  threshold           = 1
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.recovery_alarm_actions
  ok_actions          = var.recovery_alarm_actions

  dimensions = {
    FunctionName = aws_lambda_function.worker.function_name
  }
}

# Task API Lambda error alarm
resource "aws_cloudwatch_metric_alarm" "task_api_lambda_errors" {
  alarm_name          = "agentforge-task-api-errors-${var.environment}"
  alarm_description   = "Task API Lambda reported execution errors."
  comparison_operator = "GreaterThanOrEqualToThreshold"
  evaluation_periods  = 1
  metric_name         = "Errors"
  namespace           = "AWS/Lambda"
  period              = 300
  statistic           = "Sum"
  threshold           = 1
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.recovery_alarm_actions
  ok_actions          = var.recovery_alarm_actions

  dimensions = {
    FunctionName = aws_lambda_function.task_api.function_name
  }
}

# DLQ backlog alarm
resource "aws_cloudwatch_metric_alarm" "tasks_dlq_messages_visible" {
  alarm_name          = "agentforge-tasks-dlq-visible-${var.environment}"
  alarm_description   = "Task DLQ has visible messages."
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 1
  metric_name         = "ApproximateNumberOfMessagesVisible"
  namespace           = "AWS/SQS"
  period              = 300
  statistic           = "Maximum"
  threshold           = 0
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.recovery_alarm_actions
  ok_actions          = var.recovery_alarm_actions

  dimensions = {
    QueueName = aws_sqs_queue.tasks_dlq.name
  }
}

# HTTP API 5xx alarm
resource "aws_cloudwatch_metric_alarm" "http_api_5xx" {
  alarm_name          = "agentforge-http-api-5xx-${var.environment}"
  alarm_description   = "HTTP API reported 5xx responses."
  comparison_operator = "GreaterThanOrEqualToThreshold"
  evaluation_periods  = 1
  metric_name         = "5xx"
  namespace           = "AWS/ApiGateway"
  period              = 300
  statistic           = "Sum"
  threshold           = 1
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.recovery_alarm_actions
  ok_actions          = var.recovery_alarm_actions

  dimensions = {
    ApiId = aws_apigatewayv2_api.http.id
    Stage = aws_apigatewayv2_stage.http.name
  }
}

# WAF blocked-request alarm (security reject baseline)
resource "aws_cloudwatch_metric_alarm" "http_waf_blocked_requests" {
  count = var.waf_enabled ? 1 : 0

  alarm_name          = "agentforge-http-waf-blocked-${var.environment}"
  alarm_description   = "WAF blocked one or more requests."
  comparison_operator = "GreaterThanOrEqualToThreshold"
  evaluation_periods  = 1
  metric_name         = "BlockedRequests"
  namespace           = "AWS/WAFV2"
  period              = 300
  statistic           = "Sum"
  threshold           = 1
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.recovery_alarm_actions
  ok_actions          = var.recovery_alarm_actions

  dimensions = {
    WebACL = aws_wafv2_web_acl.http_api[0].name
    Rule   = "ALL"
    Region = data.aws_region.current.name
  }
}

resource "aws_cloudwatch_dashboard" "agentforge" {
  count = var.dashboard_enabled ? 1 : 0

  dashboard_name = "agentforge-${var.environment}"
  dashboard_body = jsonencode({
    widgets = [
      {
        type   = "metric"
        x      = 0
        y      = 0
        width  = 12
        height = 6
        properties = {
          title  = "Lambda Errors (5m sum)"
          region = data.aws_region.current.name
          stat   = "Sum"
          period = 300
          metrics = [
            ["AWS/Lambda", "Errors", "FunctionName", aws_lambda_function.worker.function_name],
            ["AWS/Lambda", "Errors", "FunctionName", aws_lambda_function.task_api.function_name],
            ["AWS/Lambda", "Errors", "FunctionName", aws_lambda_function.recovery.function_name],
          ]
        }
      },
      {
        type   = "metric"
        x      = 12
        y      = 0
        width  = 12
        height = 6
        properties = {
          title  = "Queue Backlog"
          region = data.aws_region.current.name
          stat   = "Maximum"
          period = 300
          metrics = [
            ["AWS/SQS", "ApproximateNumberOfMessagesVisible", "QueueName", aws_sqs_queue.tasks.name],
            ["AWS/SQS", "ApproximateNumberOfMessagesVisible", "QueueName", aws_sqs_queue.tasks_dlq.name],
          ]
        }
      },
      {
        type   = "metric"
        x      = 0
        y      = 6
        width  = 12
        height = 6
        properties = {
          title  = "HTTP API 5xx and Count"
          region = data.aws_region.current.name
          stat   = "Sum"
          period = 300
          metrics = [
            ["AWS/ApiGateway", "5xx", "ApiId", aws_apigatewayv2_api.http.id, "Stage", aws_apigatewayv2_stage.http.name],
            ["AWS/ApiGateway", "Count", "ApiId", aws_apigatewayv2_api.http.id, "Stage", aws_apigatewayv2_stage.http.name],
          ]
        }
      },
      {
        type   = "metric"
        x      = 12
        y      = 6
        width  = 12
        height = 6
        properties = {
          title  = "Worker Concurrency and Throttles"
          region = data.aws_region.current.name
          stat   = "Maximum"
          period = 300
          metrics = [
            ["AWS/Lambda", "ConcurrentExecutions", "FunctionName", aws_lambda_function.worker.function_name],
            ["AWS/Lambda", "Throttles", "FunctionName", aws_lambda_function.worker.function_name, { "stat" : "Sum" }],
          ]
        }
      },
    ]
  })
}

# WAF for HTTP API (regional)
resource "aws_wafv2_web_acl" "http_api" {
  count = var.waf_enabled ? 1 : 0

  name  = "agentforge-http-waf-${var.environment}"
  scope = "REGIONAL"

  default_action {
    allow {}
  }

  rule {
    name     = "AWSManagedRulesCommonRuleSet"
    priority = 1

    override_action {
      none {}
    }

    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesCommonRuleSet"
        vendor_name = "AWS"
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "agentforge-http-waf-common-rules"
      sampled_requests_enabled   = true
    }
  }

  rule {
    # Adds explicit Log4Shell/known bad input signatures on top of CommonRuleSet.
    name     = "AWSManagedRulesKnownBadInputsRuleSet"
    priority = 2

    override_action {
      none {}
    }

    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesKnownBadInputsRuleSet"
        vendor_name = "AWS"
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "agentforge-http-waf-known-bad-inputs"
      sampled_requests_enabled   = true
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = "agentforge-http-waf"
    sampled_requests_enabled   = true
  }

  tags = {
    Name = "agentforge-http-waf"
  }
}

resource "aws_wafv2_web_acl_association" "http_api" {
  count = var.waf_enabled ? 1 : 0

  resource_arn = aws_apigatewayv2_stage.http.arn
  web_acl_arn  = aws_wafv2_web_acl.http_api[0].arn
}

# =============================================================================
# API Gateway - HTTP API (Task REST API)
# =============================================================================
# Provides a public HTTP endpoint for submitting tasks, querying status,
# and retrieving results. Uses the $default stage for simplicity.
# =============================================================================

resource "aws_apigatewayv2_api" "http" {
  name          = "agentforge-api-${var.environment}"
  protocol_type = "HTTP"
  description   = "AgentForge Task REST API"

  cors_configuration {
    allow_origins = var.http_allowed_origins
    allow_methods = ["GET", "POST", "PUT", "DELETE", "OPTIONS"]
    allow_headers = ["Content-Type", "Authorization", "X-Request-ID", "Idempotency-Key"]
    max_age       = 3600
  }

  tags = {
    Name = "agentforge-api"
  }
}

resource "aws_apigatewayv2_integration" "task_api" {
  api_id                 = aws_apigatewayv2_api.http.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.task_api.invoke_arn
  integration_method     = "POST"
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_authorizer" "http_jwt" {
  count = var.http_jwt_authorizer_enabled ? 1 : 0

  api_id           = aws_apigatewayv2_api.http.id
  authorizer_type  = "JWT"
  name             = "agentforge-http-jwt-${var.environment}"
  identity_sources = ["$request.header.Authorization"]

  jwt_configuration {
    issuer   = var.http_jwt_authorizer_issuer
    audience = var.http_jwt_authorizer_audiences
  }
}

resource "aws_apigatewayv2_route" "task_api_default" {
  api_id             = aws_apigatewayv2_api.http.id
  route_key          = "$default"
  authorization_type = var.http_jwt_authorizer_enabled ? "JWT" : "AWS_IAM"
  authorizer_id      = var.http_jwt_authorizer_enabled ? aws_apigatewayv2_authorizer.http_jwt[0].id : null
  target             = "integrations/${aws_apigatewayv2_integration.task_api.id}"
}

resource "aws_apigatewayv2_stage" "http" {
  api_id      = aws_apigatewayv2_api.http.id
  name        = var.environment
  auto_deploy = true

  access_log_settings {
    destination_arn = aws_cloudwatch_log_group.api_gateway.arn
    format = jsonencode({
      requestId        = "$context.requestId"
      ip               = "$context.identity.sourceIp"
      requestTime      = "$context.requestTime"
      httpMethod       = "$context.httpMethod"
      routeKey         = "$context.routeKey"
      status           = "$context.status"
      protocol         = "$context.protocol"
      responseLength   = "$context.responseLength"
      integrationError = "$context.integrationErrorMessage"
    })
  }

  tags = {
    Name = "agentforge-api-stage"
  }
}

# Allow API Gateway to invoke the task API Lambda
resource "aws_lambda_permission" "task_api_apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.task_api.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.http.execution_arn}/*/*"
}

# CloudWatch log group for API Gateway access logs
resource "aws_cloudwatch_log_group" "api_gateway" {
  name              = "/aws/apigateway/agentforge-api-${var.environment}"
  retention_in_days = 365
  kms_key_id        = var.kms_key_arn != "" ? var.kms_key_arn : null

  tags = {
    Name = "agentforge-api-logs"
  }
}

# CloudWatch log group for WebSocket API Gateway access logs
resource "aws_cloudwatch_log_group" "websocket_api_gateway" {
  name              = "/aws/apigateway/agentforge-ws-${var.environment}"
  retention_in_days = 365
  kms_key_id        = var.kms_key_arn != "" ? var.kms_key_arn : null

  tags = {
    Name = "agentforge-ws-logs"
  }
}

# =============================================================================
# API Gateway - WebSocket API (Streaming)
# =============================================================================
# Provides real-time streaming of agent progress to connected clients.
# Clients connect with a task ID and receive step-by-step updates.
# =============================================================================

resource "aws_apigatewayv2_api" "websocket" {
  name                       = "agentforge-ws-${var.environment}"
  protocol_type              = "WEBSOCKET"
  route_selection_expression = "$request.body.action"
  description                = "AgentForge WebSocket API for real-time streaming"

  tags = {
    Name = "agentforge-ws"
  }
}

# --- $connect route ---
resource "aws_apigatewayv2_integration" "ws_connect" {
  api_id             = aws_apigatewayv2_api.websocket.id
  integration_type   = "AWS_PROXY"
  integration_uri    = aws_lambda_function.ws_connect.invoke_arn
  integration_method = "POST"
}

resource "aws_apigatewayv2_authorizer" "websocket_request" {
  count = var.websocket_authorizer_enabled ? 1 : 0

  api_id           = aws_apigatewayv2_api.websocket.id
  authorizer_type  = "REQUEST"
  name             = "agentforge-ws-request-${var.environment}"
  authorizer_uri   = "arn:aws:apigateway:${data.aws_region.current.name}:lambda:path/2015-03-31/functions/${var.websocket_authorizer_lambda_arn}/invocations"
  identity_sources = var.websocket_authorizer_identity_sources
}

resource "aws_apigatewayv2_route" "ws_connect" {
  api_id             = aws_apigatewayv2_api.websocket.id
  route_key          = "$connect"
  authorization_type = var.websocket_authorizer_enabled ? "CUSTOM" : "AWS_IAM"
  authorizer_id      = var.websocket_authorizer_enabled ? aws_apigatewayv2_authorizer.websocket_request[0].id : null
  target             = "integrations/${aws_apigatewayv2_integration.ws_connect.id}"
}

# --- $disconnect route ---
resource "aws_apigatewayv2_integration" "ws_disconnect" {
  api_id             = aws_apigatewayv2_api.websocket.id
  integration_type   = "AWS_PROXY"
  integration_uri    = aws_lambda_function.ws_disconnect.invoke_arn
  integration_method = "POST"
}

resource "aws_apigatewayv2_route" "ws_disconnect" {
  api_id             = aws_apigatewayv2_api.websocket.id
  route_key          = "$disconnect"
  authorization_type = "AWS_IAM"
  target             = "integrations/${aws_apigatewayv2_integration.ws_disconnect.id}"
}

# Deploy the WebSocket API
resource "aws_apigatewayv2_stage" "websocket" {
  api_id      = aws_apigatewayv2_api.websocket.id
  name        = var.environment
  auto_deploy = true

  default_route_settings {
    throttling_burst_limit = 100
    throttling_rate_limit  = 50
  }

  access_log_settings {
    destination_arn = aws_cloudwatch_log_group.websocket_api_gateway.arn
    format = jsonencode({
      requestId    = "$context.requestId"
      ip           = "$context.identity.sourceIp"
      requestTime  = "$context.requestTime"
      routeKey     = "$context.routeKey"
      status       = "$context.status"
      connectionId = "$context.connectionId"
      eventType    = "$context.eventType"
    })
  }

  tags = {
    Name = "agentforge-ws-stage"
  }
}

# Allow API Gateway to invoke the WebSocket connect Lambda
resource "aws_lambda_permission" "ws_connect_apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.ws_connect.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.websocket.execution_arn}/*/*"
}

# Allow API Gateway to invoke the WebSocket disconnect Lambda
resource "aws_lambda_permission" "ws_disconnect_apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.ws_disconnect.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.websocket.execution_arn}/*/*"
}

resource "aws_lambda_permission" "ws_authorizer_apigw" {
  count = var.websocket_authorizer_enabled ? 1 : 0

  statement_id  = "AllowAPIGatewayInvokeWSAuthorizer"
  action        = "lambda:InvokeFunction"
  function_name = var.websocket_authorizer_lambda_arn
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.websocket.execution_arn}/authorizers/${aws_apigatewayv2_authorizer.websocket_request[0].id}"
}
