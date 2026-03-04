# AgentForge

[![CI](https://github.com/zhangzh-pku/agent-forge/actions/workflows/ci.yml/badge.svg)](https://github.com/zhangzh-pku/agent-forge/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/zhangzh-pku/agent-forge)](./LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/zhangzh-pku/agent-forge)](./go.mod)
[![Go Report Card](https://goreportcard.com/badge/github.com/zhangzh-pku/agent-forge)](https://goreportcard.com/report/github.com/zhangzh-pku/agent-forge)

A cloud-native agent execution engine written in Go. AgentForge accepts tasks via
HTTP, executes them through a ReAct (Reason + Act) loop with LLM-driven tool use,
checkpoints every step for reliable abort/resume, and streams progress to clients
over WebSockets.

```
HTTP API --> SQS Queue --> Worker --> ReAct Loop --> Step Checkpointing
                                        |               (memory + workspace)
                                        v
                                  WebSocket Streaming --> Client
```

## Architecture Overview

AgentForge is composed of three independently deployable components:

| Component | Entry point | Role |
|-----------|-------------|------|
| **Task API** | `cmd/taskapi/` | HTTP server -- accepts task submissions, queries, abort/resume requests |
| **Worker** | `cmd/worker/` | Consumes messages from an SQS queue (or in-memory queue) and runs the ReAct engine |
| **WebSocket handlers** | `cmd/wsconnect/`, `cmd/wsdisconnect/` | AWS API Gateway Lambda handlers for `$connect` / `$disconnect` routes |
| **Recovery job** | `cmd/recovery/` | Stale-run redrive and optional consistency check/repair (one-shot or scheduled) |

### Data flow

1. A client sends `POST /tasks` with a prompt. The API writes a **Task** record,
   enqueues an **SQS message**, and returns immediately.
2. The Worker dequeues the message, **claims** the existing Run, and enters the **ReAct loop**.
3. Each iteration of the loop:
   - Calls the LLM with the current memory (conversation history).
   - If the LLM returns tool calls, executes them against the **VFS workspace**.
   - Writes a **Step** record with timing, token usage, and error details.
   - **Checkpoints** both memory and workspace to the artifact store (S3 / in-memory).
   - Pushes a stream event to all connected WebSocket clients.
4. The loop ends when the LLM produces a final answer, the step limit is reached,
   or an abort is requested.
5. Clients can **resume** from any checkpoint, which creates a new Run that
   restores memory and workspace from the selected step.

### Storage

| Store | Production | Local development |
|-------|------------|-------------------|
| **Metadata** (Tasks, Runs, Steps, Connections) | DynamoDB (4 tables) | `state.MemoryStore` -- fully in-memory |
| **Artifacts** (memory snapshots, workspace tar.gz) | S3 | `artifact.MemoryStore` -- fully in-memory |
| **Task queue** | SQS | `queue.MemoryQueue` -- buffered Go channel |

All interfaces are defined in their respective `pkg/` packages, making it
straightforward to swap implementations.

### Multi-tenancy

Every request must include both tenant and user identity.

- Local/dev `header` mode: `X-Tenant-Id` + `X-User-Id`
- Production `trusted` mode: `X-Authenticated-Tenant-Id` + `X-Authenticated-User-Id` (set by upstream auth layer)

Task access is enforced by both tenant and user ownership.

### Streaming

The stream subsystem uses a **time/byte chunker** (default: flush every 100 ms or
2 KB) to batch events before pushing them over WebSocket connections. Events include
`step_start`, `step_end`, `tool_call`, `tool_result`, and `complete`.

### Workspace (VFS)

Each run gets an isolated virtual filesystem under `/tmp/agentforge/<run_id>`.
The workspace enforces:

- **Path traversal protection** -- all paths are resolved relative to the root.
- **Quotas** -- default 50 MB total size and 2000 files.
- **Snapshot/Restore** -- the entire workspace can be archived to a `tar.gz` for
  checkpointing and restored for resume.

## Project Structure

```
cmd/
  taskapi/          HTTP API server
  worker/           SQS / in-memory queue worker
  recovery/         stale-run recovery / consistency repair job
  wsconnect/        WebSocket $connect Lambda handler
  wsdisconnect/     WebSocket $disconnect Lambda handler

pkg/
  api/              HTTP handlers, auth middleware, WebSocket handlers
  artifact/         Artifact store interface + S3 + in-memory implementations
  config/           Runtime and environment configuration loading
  engine/           ReAct execution loop, LLM client interface, tool registry, worker
  memory/           Memory snapshot serialization (save/load to artifact store)
  model/            Domain types (Task, Run, Step, Connection, StreamEvent, etc.)
  queue/            Queue interface + SQS + in-memory implementations
  state/            Metadata store interface + DynamoDB + in-memory implementations
  stream/           Stream events, chunker (time/byte flush), pusher interface
  task/             Task lifecycle service (create, get, abort, resume)
  util/             ID generation, structured logging
  workspace/        VFS with quotas, snapshot/restore, path safety

deploy/
  terraform/        Infrastructure-as-Code skeleton (DynamoDB, SQS, S3, Lambda, API GW)

docs/               Architecture, API, and checkpoint documentation
```

Key docs:
- `docs/configuration.md` - full environment variable reference
- `docs/terraform.md` - Terraform deployment flow, security toggles, and packaging requirements
- `docs/coding-standards.md` - coding and review conventions
- `docs/operations-runbook.md` - incident and partial-failure handling
- `CONTRIBUTING.md` - pull request process and required checks
- `SECURITY.md` - vulnerability reporting policy
- `CODE_OF_CONDUCT.md` - community behavior expectations
- `SUPPORT.md` - support channels and required context

## Quick Start

### Prerequisites

- **Go 1.24+**
- No AWS account required for local development.

### Run the tests

```bash
go test ./...
```

### Run with Docker Compose

```bash
docker compose up --build taskapi
```

Then call the versioned API endpoint:

```bash
curl -s -X POST http://localhost:8080/v1/tasks \
  -H "Content-Type: application/json" \
  -H "X-Tenant-Id: acme" \
  -H "X-User-Id: alice" \
  -d '{"prompt":"List the prime numbers under 20"}' | jq .
```

You can also run the scripted flow:

```bash
./examples/quickstart.sh
```

### Bootstrap Local Env

```bash
cp .env.example .env
```

Use `.env` or exported shell variables for configuration while developing.

### Start the API server locally

```bash
go run cmd/taskapi/main.go
```

The server starts on port 8080 by default. Override with the `PORT` environment
variable:

```bash
PORT=9090 go run cmd/taskapi/main.go
```

In local mode the server starts with an **embedded worker** that shares in-memory
stores. Tasks submitted via the API are automatically processed — no separate
worker process needed.

### Runtime Mode

`cmd/taskapi`, `cmd/worker`, `cmd/recovery`, `cmd/wsconnect`, and `cmd/wsdisconnect` support two
runtime modes:

| `AGENTFORGE_RUNTIME` | Behavior |
|----------------------|----------|
| `local` (default) | Uses in-memory state/artifact/queue backends |
| `aws` | Uses DynamoDB + S3 + SQS production backends |

When `AGENTFORGE_RUNTIME=aws`, set these required variables:

| Variable | Description |
|----------|-------------|
| `TASKS_TABLE` | DynamoDB tasks table |
| `RUNS_TABLE` | DynamoDB runs table |
| `STEPS_TABLE` | DynamoDB steps table (also stores replay events) |
| `CONNECTIONS_TABLE` | DynamoDB websocket connections table |
| `TASK_QUEUE_URL` | SQS queue URL |
| `ARTIFACTS_BUCKET` | S3 bucket for memory/workspace snapshots |
| `ARTIFACT_SSE_KMS_KEY_ARN` | Optional CMK ARN for explicit SSE-KMS on artifact writes |

Optional AWS runtime variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `CONNECTIONS_TASK_INDEX` | `task-index` | DynamoDB GSI name used to query task subscribers |
| `WEBSOCKET_ENDPOINT` | _(empty)_ | API Gateway WebSocket management endpoint |
| `ARTIFACT_PRESIGN_EXPIRES` | `15m` | S3 presigned URL TTL |
| `ARTIFACT_SSE_KMS_KEY_ARN` | _(empty)_ | Optional CMK ARN used when writing artifacts with SSE-KMS |
| `SQS_WAIT_TIME_SECONDS` | `20` | SQS long-poll wait time |
| `SQS_VISIBILITY_TIMEOUT_SECONDS` | `300` | SQS visibility timeout |
| `SQS_MAX_MESSAGES` | `10` | Max messages per receive call |

For complete variable coverage and per-component requirements, see
`docs/configuration.md`.

By default, AgentForge uses a deterministic mock LLM. To run against an OpenAI-
compatible API instead:

```bash
AGENTFORGE_LLM_PROVIDER=openai \
OPENAI_API_KEY=sk-... \
AGENTFORGE_LLM_MODEL=gpt-4o-mini \
go run cmd/taskapi/main.go
```

### Create a task

```bash
curl -s -X POST http://localhost:8080/tasks \
  -H "Content-Type: application/json" \
  -H "X-Tenant-Id: acme" \
  -H "X-User-Id: alice" \
  -d '{"prompt": "List the prime numbers under 20"}' | jq .
```

### Retrieve a task

```bash
curl -s http://localhost:8080/tasks/<task_id> \
  -H "X-Tenant-Id: acme" \
  -H "X-User-Id: alice" | jq .
```

### Health check

```bash
curl http://localhost:8080/health
```

## Local Development

The API server starts with an embedded worker, both sharing the same in-memory
stores. All external dependencies are replaced with in-memory implementations:

| Dependency | In-memory replacement |
|------------|-----------------------|
| DynamoDB | `state.NewMemoryStore()` |
| S3 | `artifact.NewMemoryStore()` |
| SQS | `queue.NewMemoryQueue(1000)` |
| WebSocket push | `stream.NewMockPusher()` |
| LLM (default) | `engine.NewMockLLMClient(...)` -- returns canned responses |

The standalone worker can also be run separately if needed:

```bash
go run cmd/worker/main.go
```

Run recovery one-shot:

```bash
go run cmd/recovery/main.go
```

Run scheduled recovery every 5 minutes:

```bash
AGENTFORGE_RECOVERY_ENABLED=true \
AGENTFORGE_RECOVERY_INTERVAL=5m \
go run cmd/recovery/main.go
```

## Development Workflow

Standard local checks:

```bash
make fmt
make ci
```

Reference:
- `CONTRIBUTING.md`
- `docs/coding-standards.md`

The mock LLM client simulates a three-step tool-use conversation and then returns
a final answer, which is useful for end-to-end testing of the full pipeline without
any external service.

### LLM Provider Configuration

`cmd/taskapi` and `cmd/worker` read LLM settings from environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENTFORGE_LLM_PROVIDER` | `mock` | `mock` or `openai` |
| `AGENTFORGE_LLM_MOCK_STEPS` | `3` | Number of mock tool-call steps before final answer |
| `OPENAI_API_KEY` | _(none)_ | Required when provider is `openai` |
| `OPENAI_API_KEY_SECRET_ARN` | _(empty)_ | Optional Secrets Manager secret ARN for loading `OPENAI_API_KEY` at startup |
| `OPENAI_API_KEY_SECRET_FIELD` | _(empty)_ | Optional JSON field in secret payload that contains API key |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible base URL |
| `AGENTFORGE_LLM_MODEL` | `gpt-4o-mini` | Default model when request omits `model_config.model_id` |
| `AGENTFORGE_LLM_ROUTING_DEFAULT_MODE` | `latency-first` | Router default mode: `latency-first`, `quality-first`, or `cost-cap` |
| `AGENTFORGE_LLM_FALLBACK_PROVIDERS` | _(empty)_ | Comma-separated provider fallback chain (for example `openai,mock`) |
| `AGENTFORGE_ALLOWED_MODEL_IDS` | _(built-in set)_ | Comma-separated model allowlist for `model_config` request validation |
| `AGENTFORGE_LLM_TIMEOUT_SECONDS` | `60` | HTTP timeout for LLM requests |

When a task includes `model_config.model_id`, that value overrides
`AGENTFORGE_LLM_MODEL` for the run.
When both `OPENAI_API_KEY` and `OPENAI_API_KEY_SECRET_ARN` are set, the direct
`OPENAI_API_KEY` value takes precedence.

## AWS Deployment Overview

A production deployment uses the following AWS services:

| Service | Purpose |
|---------|---------|
| **DynamoDB** | Four tables: `Tasks`, `Runs`, `Steps`, `Connections` |
| **S3** | Artifact bucket for memory snapshots and workspace archives |
| **SQS** | Task queue between the API and the worker fleet |
| **API Gateway (WebSocket)** | Real-time streaming to clients via `$connect` / `$disconnect` Lambda routes |
| **Lambda or ECS** | Hosts the API server and worker processes |

A Terraform skeleton is provided in `deploy/terraform/`. Adapt it to your
environment and apply:

```bash
cd deploy/terraform
terraform init
terraform plan
terraform apply
```

Important: the current Terraform module provisions placeholder Lambda artifacts.
Do not treat `terraform apply` output as production-ready until CI/CD replaces
placeholders with real signed build artifacts.

For production/staging, the module now requires explicit Lambda package paths
(`*_lambda_package_path`) so placeholder artifacts cannot be deployed by mistake.
See `docs/terraform.md` for packaging and apply flow.

Terraform now includes a scheduled recovery Lambda (`agentforge-recovery-*`) and
an EventBridge rule (`rate(5 minutes)` by default). Tune with:
- `recovery_enabled`
- `recovery_schedule_expression`
- `recovery_stale_for`
- `recovery_limit`
- `recovery_consistency_check`
- `recovery_consistency_repair`
- `recovery_alarm_actions`

## API Reference

All endpoints require tenant + user identity.

- Local/dev header mode: `X-Tenant-Id` and `X-User-Id`
- Trusted mode (recommended for aws): `X-Authenticated-Tenant-Id` and
  `X-Authenticated-User-Id` injected by an upstream authorizer

### POST /tasks

Create a new task. Supports idempotency via the `Idempotency-Key` header or
`idempotency_key` body field.

**Request body:**

```json
{
  "prompt": "Summarize the latest quarterly report",
  "idempotency_key": "req-abc-123",
  "model_config": {
    "model_id": "gpt-4",
    "temperature": 0.7,
    "max_tokens": 4096
  }
}
```

**Response:** `201 Created` with the Task object.

### GET /tasks/{task_id}

Retrieve a task by ID.

**Response:** `200 OK` with the Task object.

### GET /tasks/{task_id}/runs/{run_id}/steps?from=0&limit=200

List steps for a given run. Supports pagination via `from` (step index offset)
and `limit` (default 200).

**Response:** `200 OK`

```json
{
  "steps": [
    {
      "run_id": "run_abc",
      "step_index": 0,
      "type": "llm_call",
      "status": "OK",
      "latency_ms": 1200,
      "token_usage": { "input": 150, "output": 80, "total": 230 },
      "checkpoint_ref": { "memory": { "s3_key": "..." }, "workspace": { "s3_key": "..." } }
    }
  ]
}
```

### POST /tasks/{task_id}/abort

Abort a running task. The engine checks for abort between steps and exits
gracefully.

**Request body:**

```json
{
  "reason": "User cancelled"
}
```

### POST /tasks/{task_id}/resume

Resume a task from a checkpoint, creating a new Run that restores memory and
workspace state.

**Request body:**

```json
{
  "from_run_id": "run_previous",
  "from_step_index": 5,
  "model_config_override": {
    "model_id": "gpt-4",
    "temperature": 0.5
  }
}
```

**Response:** `200 OK` with the new Task state and Run.

### GET /tasks/{task_id}/runs/{run_id}

Retrieve run-level metadata including cumulative token/cost attribution.

### GET /tasks/{task_id}/runs/{run_id}/events/replay

Replay persisted stream events for reconnect/missed-event recovery.

Query params:
- `from_seq` (optional): replay `seq > from_seq`
- `from_ts` (optional): replay events newer than timestamp
- `limit` (optional, default 200, max 2000)

### POST /tasks/{task_id}/runs/{run_id}/events/compact

Compact old replay events.

```json
{
  "before_ts": 1710500000
}
```

### GET /tenants/{tenant_id}/runtime

Get tenant-level runtime metrics (queued/running counts, budget usage,
breaker state, retries, DLQ counters).

### GET /tenants/{tenant_id}/alerts

Get tenant-level runtime alerts (queue-depth, rate-limit, budget, breaker).

## Multi-Tenant Controls

Memory queue scheduling now includes:

- Per-tenant queued and running concurrency limits.
- Round-robin fairness across tenants.
- Per-tenant token/cost budget admission with reservations.
- Hard circuit breakers for rate/error/budget breach with cooldown recovery.
- Retry/idempotency/DLQ handling and dead-letter re-drive hooks.

Model routing now includes:

- Task-type aware model selection.
- Provider/model fallback chains.
- Policy modes: `latency-first`, `quality-first`, `cost-cap`.
- Per-run token/cost attribution.

## Domain Model

| Entity | Description |
|--------|-------------|
| **Task** | Top-level user request. Statuses: `QUEUED`, `RUNNING`, `SUCCEEDED`, `FAILED`, `ABORTED` |
| **Run** | A single execution attempt of a task. Multiple runs per task when resuming. |
| **Step** | One iteration of the ReAct loop. Types: `llm_call`, `tool_call`, `observation`, `final` |
| **Connection** | A WebSocket connection subscribed to a task's stream events |

## Contributing

1. Fork the repository.
2. Create a feature branch from `main`.
3. Write tests for new functionality -- `go test ./...` must pass.
4. Follow existing code conventions (standard library HTTP, interface-driven design).
5. Open a pull request with a clear description of the change.

## License

This project is licensed under the [Apache License 2.0](https://www.apache.org/licenses/LICENSE-2.0).
