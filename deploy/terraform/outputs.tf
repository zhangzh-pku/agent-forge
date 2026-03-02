# =============================================================================
# AgentForge - Terraform Outputs
# =============================================================================

# -----------------------------------------------------------------------------
# API Endpoints
# -----------------------------------------------------------------------------

output "api_url" {
  description = "HTTP API endpoint for submitting and querying tasks"
  value       = aws_apigatewayv2_stage.http.invoke_url
}

output "websocket_url" {
  description = "WebSocket API endpoint for real-time task streaming"
  value       = aws_apigatewayv2_stage.websocket.invoke_url
}

# -----------------------------------------------------------------------------
# SQS
# -----------------------------------------------------------------------------

output "sqs_queue_url" {
  description = "SQS queue URL for the task processing queue"
  value       = aws_sqs_queue.tasks.url
}

output "sqs_dlq_url" {
  description = "SQS dead-letter queue URL for failed task messages"
  value       = aws_sqs_queue.tasks_dlq.url
}

# -----------------------------------------------------------------------------
# S3
# -----------------------------------------------------------------------------

output "artifacts_bucket_name" {
  description = "S3 bucket name for task artifacts"
  value       = aws_s3_bucket.artifacts.id
}

# -----------------------------------------------------------------------------
# DynamoDB Table Names
# -----------------------------------------------------------------------------

output "dynamodb_tasks_table" {
  description = "DynamoDB table name for task records"
  value       = aws_dynamodb_table.tasks.name
}

output "dynamodb_runs_table" {
  description = "DynamoDB table name for run records"
  value       = aws_dynamodb_table.runs.name
}

output "dynamodb_steps_table" {
  description = "DynamoDB table name for step records"
  value       = aws_dynamodb_table.steps.name
}

output "dynamodb_connections_table" {
  description = "DynamoDB table name for WebSocket connection records"
  value       = aws_dynamodb_table.connections.name
}
