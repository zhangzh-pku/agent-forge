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
| `AGENTFORGE_AUTH_MODE` | `header` (`local`) / `trusted` (`aws`) | `header` reads `X-Tenant-Id` + `X-User-Id`; `trusted` reads `X-Authenticated-Tenant-Id` + `X-Authenticated-User-Id`. |
| `AGENTFORGE_LLM_PROVIDER` | `mock` | `mock` or `openai`. |
| `AGENTFORGE_LLM_MOCK_STEPS` | `3` | Number of mock steps before final answer. |
| `AGENTFORGE_LLM_MODEL` | `gpt-4o-mini` | Default model ID when task does not specify one. |
| `AGENTFORGE_LLM_ROUTING_DEFAULT_MODE` | `latency-first` | Router default policy mode: `latency-first`, `quality-first`, or `cost-cap`. |
| `AGENTFORGE_LLM_FALLBACK_PROVIDERS` | _(empty)_ | Comma-separated fallback providers (e.g. `openai,mock`) used by routed LLM fallback chain. |
| `AGENTFORGE_ALLOWED_MODEL_IDS` | _(built-in set)_ | Comma-separated allowlist used to validate `model_config.model_id` and fallback model IDs. |
| `AGENTFORGE_LLM_TIMEOUT_SECONDS` | `60` | LLM request timeout. |
| `OPENAI_API_KEY` | _(none)_ | Required when provider is `openai`. |
| `OPENAI_API_KEY_SECRET_ARN` | _(empty)_ | Optional Secrets Manager secret ARN used to resolve OpenAI API key at startup. |
| `OPENAI_API_KEY_SECRET_FIELD` | _(empty)_ | Optional JSON field name in the secret payload when secret value is a JSON object. |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible base URL. |
| `AGENTFORGE_OTEL_ENABLED` | `false` | Enables OpenTelemetry tracing initialization in app entrypoints. |
| `AGENTFORGE_OTEL_SERVICE_NAME` | entrypoint default | Overrides OTel `service.name` resource attribute. |
| `AGENTFORGE_OTEL_EXPORTER` | `none` | OTel exporter mode: `none` or `stdout`. |
| `AGENTFORGE_OTEL_SAMPLE_RATIO` | `1.0` | Trace sampling ratio in `[0,1]` for OTel tracer provider. |

## Recovery Scheduler Variables

Used by `cmd/recovery`, and optionally by `cmd/taskapi` when enabling background recovery.

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENTFORGE_RECOVERY_ENABLED` | `false` | Enables background scheduler inside `cmd/taskapi`. |
| `AGENTFORGE_RECOVERY_INTERVAL` | `0` | Scheduler interval. `0` means run one pass on startup. |
| `AGENTFORGE_RECOVERY_STALE_FOR` | `10m` | Stale threshold for queued/running runs. |
| `AGENTFORGE_RECOVERY_LIMIT` | `200` | Max runs/tasks scanned per pass. |
| `AGENTFORGE_RECOVERY_TENANT_ID` | _(empty)_ | Optional tenant scope; empty scans all tenants. |
| `AGENTFORGE_RECOVERY_CONSISTENCY_CHECK` | `false` | Enables drift check pass after stale-run recovery. |
| `AGENTFORGE_RECOVERY_CONSISTENCY_REPAIR` | `false` | Applies consistency repairs (requires consistency check enabled). |

Notes:
- `cmd/recovery` always executes at least one pass on startup.
- `AGENTFORGE_RECOVERY_ENABLED=true` mainly controls the background scheduler inside `cmd/taskapi`.

## AWS Backend Variables

Required when `AGENTFORGE_RUNTIME=aws` for `cmd/taskapi`, `cmd/worker`, and `cmd/recovery`.

| Variable | Required | Description |
|----------|----------|-------------|
| `TASKS_TABLE` | Yes | DynamoDB table for task records. |
| `RUNS_TABLE` | Yes | DynamoDB table for run records. |
| `STEPS_TABLE` | Yes | DynamoDB table for step and event records. |
| `CONNECTIONS_TABLE` | Yes | DynamoDB table for websocket connection records. |
| `TASK_QUEUE_URL` | Yes | SQS queue URL for task dispatch. |
| `ARTIFACTS_BUCKET` | Yes | S3 bucket for checkpoint artifacts. |
| `ARTIFACT_SSE_KMS_KEY_ARN` | No | Optional CMK ARN used by `S3Store.Put` SSE-KMS (`aws:kms`). Empty means use bucket default key. |
| `CONNECTIONS_TASK_INDEX` | No | Connections task GSI name. Default: `task-index`. |
| `WEBSOCKET_ENDPOINT` | No | API Gateway management endpoint (`https://...`) for pushing stream events. |
| `AGENTFORGE_EVENT_RETENTION` | No | Event retention window for replay in aws mode. Default: `24h`; set `0` to disable retention filtering and event TTL hints. |
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
- optional `AGENTFORGE_EVENT_RETENTION`

## Local Development

Use `.env.example` as baseline and keep `AGENTFORGE_RUNTIME=local`.

Notes:
- `OPENAI_API_KEY` takes precedence over `OPENAI_API_KEY_SECRET_ARN` when both are set.
