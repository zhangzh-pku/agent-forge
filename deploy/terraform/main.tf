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
      version = "~> 5.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.0"
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
  message_retention_seconds  = 86400   # 1 day
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
      "dynamodb:Scan",
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
  statement {
    sid    = "S3Read"
    effect = "Allow"
    actions = [
      "s3:GetObject",
      "s3:ListBucket",
    ]
    resources = [
      aws_s3_bucket.artifacts.arn,
      "${aws_s3_bucket.artifacts.arn}/*",
    ]
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
  # Full DynamoDB access - worker reads tasks, writes runs/steps, and
  # looks up connections for streaming.
  statement {
    sid    = "DynamoDBAccess"
    effect = "Allow"
    actions = [
      "dynamodb:GetItem",
      "dynamodb:PutItem",
      "dynamodb:UpdateItem",
      "dynamodb:DeleteItem",
      "dynamodb:Query",
      "dynamodb:Scan",
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
      "sqs:GetQueueAttributes",
    ]
    resources = [aws_sqs_queue.tasks.arn]
  }

  # S3 read/write for storing and retrieving task artifacts
  statement {
    sid    = "S3ReadWrite"
    effect = "Allow"
    actions = [
      "s3:GetObject",
      "s3:PutObject",
      "s3:DeleteObject",
      "s3:ListBucket",
    ]
    resources = [
      aws_s3_bucket.artifacts.arn,
      "${aws_s3_bucket.artifacts.arn}/*",
    ]
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
# These are placeholder definitions. Replace the filename with actual deployment
# packages built by CI/CD. Each function uses a minimal "hello world" zip for
# initial provisioning.
# =============================================================================

# Dummy archive used as a placeholder until real code is deployed via CI/CD.
# NOTE: For a real deployment, replace this with a zip containing a compiled
# Go binary named "bootstrap" (GOOS=linux GOARCH=arm64 go build -o bootstrap).
data "archive_file" "lambda_placeholder" {
  type        = "zip"
  output_path = "${path.module}/placeholder.zip"

  source {
    content  = "#!/bin/sh\necho 'placeholder - deploy real binary'\nexit 1\n"
    filename = "bootstrap"
  }
}

# --- Task API Lambda ---
# Handles REST API requests: create task, get task status, list runs, etc.
resource "aws_lambda_function" "task_api" {
  function_name = "agentforge-task-api-${var.environment}"
  role          = aws_iam_role.task_api_lambda.arn
  handler       = "bootstrap"
  runtime       = "provided.al2023"
  architectures = ["arm64"]
  timeout       = 30
  memory_size   = 256

  filename         = data.archive_file.lambda_placeholder.output_path
  source_code_hash = data.archive_file.lambda_placeholder.output_base64sha256

  environment {
    variables = {
      AGENTFORGE_RUNTIME = "aws"
      ENVIRONMENT       = var.environment
      TASKS_TABLE       = aws_dynamodb_table.tasks.name
      RUNS_TABLE        = aws_dynamodb_table.runs.name
      STEPS_TABLE       = aws_dynamodb_table.steps.name
      CONNECTIONS_TABLE = aws_dynamodb_table.connections.name
      TASK_QUEUE_URL    = aws_sqs_queue.tasks.url
      ARTIFACTS_BUCKET  = aws_s3_bucket.artifacts.id
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

  filename         = data.archive_file.lambda_placeholder.output_path
  source_code_hash = data.archive_file.lambda_placeholder.output_base64sha256

  environment {
    variables = {
      AGENTFORGE_RUNTIME = "aws"
      ENVIRONMENT        = var.environment
      TASKS_TABLE        = aws_dynamodb_table.tasks.name
      RUNS_TABLE         = aws_dynamodb_table.runs.name
      STEPS_TABLE        = aws_dynamodb_table.steps.name
      CONNECTIONS_TABLE  = aws_dynamodb_table.connections.name
      TASK_QUEUE_URL     = aws_sqs_queue.tasks.url
      ARTIFACTS_BUCKET   = aws_s3_bucket.artifacts.id
      WEBSOCKET_ENDPOINT = aws_apigatewayv2_stage.websocket.invoke_url
    }
  }

  tags = {
    Name = "agentforge-worker"
  }
}

# --- WebSocket Connect Lambda ---
# Handles $connect route: validates auth token, stores connection record.
resource "aws_lambda_function" "ws_connect" {
  function_name = "agentforge-ws-connect-${var.environment}"
  role          = aws_iam_role.websocket_lambda.arn
  handler       = "bootstrap"
  runtime       = "provided.al2023"
  architectures = ["arm64"]
  timeout       = 10
  memory_size   = 128

  filename         = data.archive_file.lambda_placeholder.output_path
  source_code_hash = data.archive_file.lambda_placeholder.output_base64sha256

  environment {
    variables = {
      AGENTFORGE_RUNTIME = "aws"
      ENVIRONMENT       = var.environment
      TASKS_TABLE       = aws_dynamodb_table.tasks.name
      RUNS_TABLE        = aws_dynamodb_table.runs.name
      STEPS_TABLE       = aws_dynamodb_table.steps.name
      CONNECTIONS_TABLE = aws_dynamodb_table.connections.name
    }
  }

  tags = {
    Name = "agentforge-ws-connect"
  }
}

# --- WebSocket Disconnect Lambda ---
# Handles $disconnect route: removes stale connection record from DynamoDB.
resource "aws_lambda_function" "ws_disconnect" {
  function_name = "agentforge-ws-disconnect-${var.environment}"
  role          = aws_iam_role.websocket_lambda.arn
  handler       = "bootstrap"
  runtime       = "provided.al2023"
  architectures = ["arm64"]
  timeout       = 10
  memory_size   = 128

  filename         = data.archive_file.lambda_placeholder.output_path
  source_code_hash = data.archive_file.lambda_placeholder.output_base64sha256

  environment {
    variables = {
      AGENTFORGE_RUNTIME = "aws"
      ENVIRONMENT       = var.environment
      TASKS_TABLE       = aws_dynamodb_table.tasks.name
      RUNS_TABLE        = aws_dynamodb_table.runs.name
      STEPS_TABLE       = aws_dynamodb_table.steps.name
      CONNECTIONS_TABLE = aws_dynamodb_table.connections.name
    }
  }

  tags = {
    Name = "agentforge-ws-disconnect"
  }
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
    allow_origins = ["*"]
    allow_methods = ["GET", "POST", "PUT", "DELETE", "OPTIONS"]
    allow_headers = ["Content-Type", "Authorization", "X-Request-ID", "X-Tenant-Id", "X-User-Id", "Idempotency-Key"]
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

resource "aws_apigatewayv2_route" "task_api_default" {
  api_id    = aws_apigatewayv2_api.http.id
  route_key = "$default"
  target    = "integrations/${aws_apigatewayv2_integration.task_api.id}"
}

resource "aws_apigatewayv2_stage" "http" {
  api_id      = aws_apigatewayv2_api.http.id
  name        = var.environment
  auto_deploy = true

  access_log_settings {
    destination_arn = aws_cloudwatch_log_group.api_gateway.arn
    format = jsonencode({
      requestId      = "$context.requestId"
      ip             = "$context.identity.sourceIp"
      requestTime    = "$context.requestTime"
      httpMethod     = "$context.httpMethod"
      routeKey       = "$context.routeKey"
      status         = "$context.status"
      protocol       = "$context.protocol"
      responseLength = "$context.responseLength"
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
  retention_in_days = 30

  tags = {
    Name = "agentforge-api-logs"
  }
}

# CloudWatch log group for WebSocket API Gateway access logs
resource "aws_cloudwatch_log_group" "websocket_api_gateway" {
  name              = "/aws/apigateway/agentforge-ws-${var.environment}"
  retention_in_days = 30

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

resource "aws_apigatewayv2_route" "ws_connect" {
  api_id    = aws_apigatewayv2_api.websocket.id
  route_key = "$connect"
  target    = "integrations/${aws_apigatewayv2_integration.ws_connect.id}"
}

# --- $disconnect route ---
resource "aws_apigatewayv2_integration" "ws_disconnect" {
  api_id             = aws_apigatewayv2_api.websocket.id
  integration_type   = "AWS_PROXY"
  integration_uri    = aws_lambda_function.ws_disconnect.invoke_arn
  integration_method = "POST"
}

resource "aws_apigatewayv2_route" "ws_disconnect" {
  api_id    = aws_apigatewayv2_api.websocket.id
  route_key = "$disconnect"
  target    = "integrations/${aws_apigatewayv2_integration.ws_disconnect.id}"
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
