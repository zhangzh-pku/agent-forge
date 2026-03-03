# Configuration Reference

This document is the canonical environment-variable reference for AgentForge.

## Runtime Mode

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENTFORGE_RUNTIME` | `local` | `local` uses in-memory backends; `aws` uses DynamoDB/S3/SQS backends. |

## Common Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Task API listen port (for `cmd/taskapi`). |
| `AGENTFORGE_LLM_PROVIDER` | `mock` | `mock` or `openai`. |
| `AGENTFORGE_LLM_MOCK_STEPS` | `3` | Number of mock steps before final answer. |
| `AGENTFORGE_LLM_MODEL` | `gpt-4o-mini` | Default model ID when task does not specify one. |
| `AGENTFORGE_LLM_TIMEOUT_SECONDS` | `60` | LLM request timeout. |
| `OPENAI_API_KEY` | _(none)_ | Required when provider is `openai`. |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible base URL. |

## AWS Backend Variables

Required when `AGENTFORGE_RUNTIME=aws` for `cmd/taskapi` and `cmd/worker`.

| Variable | Required | Description |
|----------|----------|-------------|
| `TASKS_TABLE` | Yes | DynamoDB table for task records. |
| `RUNS_TABLE` | Yes | DynamoDB table for run records. |
| `STEPS_TABLE` | Yes | DynamoDB table for step and event records. |
| `CONNECTIONS_TABLE` | Yes | DynamoDB table for websocket connection records. |
| `TASK_QUEUE_URL` | Yes | SQS queue URL for task dispatch. |
| `ARTIFACTS_BUCKET` | Yes | S3 bucket for checkpoint artifacts. |
| `CONNECTIONS_TASK_INDEX` | No | Connections task GSI name. Default: `task-index`. |
| `WEBSOCKET_ENDPOINT` | No | API Gateway management endpoint (`https://...`) for pushing stream events. |
| `ARTIFACT_PRESIGN_EXPIRES` | No | Presigned URL TTL. Default: `15m`. |
| `SQS_WAIT_TIME_SECONDS` | No | ReceiveMessage long-poll time. Default: `20`. |
| `SQS_VISIBILITY_TIMEOUT_SECONDS` | No | Visibility timeout. Default: `300`. |
| `SQS_MAX_MESSAGES` | No | Max messages per poll (1-10). Default: `10`. |

## WebSocket Lambda Handlers

`cmd/wsconnect` and `cmd/wsdisconnect` in aws mode require:

- `TASKS_TABLE`
- `RUNS_TABLE`
- `STEPS_TABLE`
- `CONNECTIONS_TABLE`
- optional `CONNECTIONS_TASK_INDEX`

## Local Development

Use `.env.example` as baseline and keep `AGENTFORGE_RUNTIME=local`.
