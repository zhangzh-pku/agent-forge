# AgentForge

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

Every request must include an `X-Tenant-Id` header. Tenant isolation is enforced at
the data layer -- all queries are scoped by tenant ID. The auth middleware is
intentionally simple (header-based) and designed to be replaced with JWT or IAM
validation.

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
  wsconnect/        WebSocket $connect Lambda handler
  wsdisconnect/     WebSocket $disconnect Lambda handler

pkg/
  api/              HTTP handlers, auth middleware, WebSocket handlers
  artifact/         Artifact store interface + in-memory implementation
  engine/           ReAct execution loop, LLM client interface, tool registry, worker
  memory/           Memory snapshot serialization (save/load to artifact store)
  model/            Domain types (Task, Run, Step, Connection, StreamEvent, etc.)
  queue/            Queue interface + in-memory implementation
  state/            Metadata store interface + in-memory implementation
  stream/           Stream events, chunker (time/byte flush), pusher interface
  task/             Task lifecycle service (create, get, abort, resume)
  util/             ID generation, structured logging
  workspace/        VFS with quotas, snapshot/restore, path safety

deploy/
  terraform/        Infrastructure-as-Code skeleton (DynamoDB, SQS, S3, Lambda, API GW)

docs/               Architecture, API, and checkpoint documentation
```

## Quick Start

### Prerequisites

- **Go 1.22+**
- No AWS account required for local development.

### Run the tests

```bash
go test ./...
```

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
  -H "X-Tenant-Id: acme" | jq .
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
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible base URL |
| `AGENTFORGE_LLM_MODEL` | `gpt-4o-mini` | Default model when request omits `model_config.model_id` |
| `AGENTFORGE_LLM_TIMEOUT_SECONDS` | `60` | HTTP timeout for LLM requests |

When a task includes `model_config.model_id`, that value overrides
`AGENTFORGE_LLM_MODEL` for the run.

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

## API Reference

All endpoints require the `X-Tenant-Id` header. `X-User-Id` is optional
(defaults to `anonymous`).

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
