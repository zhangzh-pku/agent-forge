# AgentForge Architecture

## System Overview

AgentForge is a serverless agent execution platform built on AWS. It accepts user-submitted prompts, executes them through a ReAct (Reasoning + Acting) loop backed by an LLM, and streams results back to clients in real time.

### Data Flow

```
Client
  |
  v
HTTP API (Task API Lambda / cmd/taskapi)
  |
  |-- writes Task + Run to DynamoDB
  |-- enqueues pointer message to SQS
  |
  v
SQS Queue
  |
  v
Worker (cmd/worker)
  |-- claims Run (idempotent RUN_QUEUED -> RUNNING)
  |-- creates local workspace
  |-- registers filesystem tools
  |
  v
Engine (ReAct Loop)
  |-- calls LLM
  |-- executes tool calls
  |-- checkpoints memory + workspace to S3 after each step
  |-- writes Step metadata to DynamoDB
  |-- pushes stream events to WebSocket connections
  |
  v
DynamoDB (metadata) + S3 (artifacts) + WebSocket (real-time events)
```

## Components

### Task API (`cmd/taskapi`)

The HTTP entry point. Handles task creation, retrieval, abort, resume, and step listing. Authentication supports:

- local/dev `header` mode (`X-Tenant-Id`, `X-User-Id`)
- production `trusted` mode (`X-Authenticated-Tenant-Id`, `X-Authenticated-User-Id`) injected by an upstream authorizer

- **Package**: `pkg/api` (handlers, middleware, WebSocket handlers)
- **Package**: `pkg/task` (service layer for lifecycle operations)

### Worker (`cmd/worker`)

A long-running process that consumes SQS messages and drives task execution. Each message is a lightweight pointer (`SQSMessage`) containing `tenant_id`, `task_id`, `run_id`, `submitted_at`, and `attempt`.

The worker:

1. Claims the run via conditional write (`ClaimRun`), preventing duplicate processing.
2. Transitions the task status to `RUNNING`.
3. Loads the task and run metadata from DynamoDB.
4. Creates an isolated local workspace for the run.
5. Registers filesystem tools into a `ToolRegistry`.
6. Instantiates the `Engine` and calls `Execute`.
7. Finalizes the run and task status based on the engine result.

- **Package**: `pkg/engine` (`Worker` struct, `worker.go`)

### Engine (`pkg/engine`)

The core ReAct loop orchestrator. Configurable via `Config` (default `MaxSteps: 50`).

Each iteration of the loop:

1. Checks the abort flag on the task in DynamoDB.
2. Checks for context cancellation.
3. Pushes a `step_start` stream event.
4. Calls the LLM with the current memory (conversation messages).
5. If the LLM returns tool calls, executes them via the `ToolRegistry` and appends results to memory.
6. Checkpoints memory (gzipped JSON to S3) and workspace (tar.gz to S3).
7. Writes a `Step` record to DynamoDB with the `CheckpointRef`.
8. Pushes a `step_end` stream event.
9. If the LLM returns no tool calls with `finish_reason == "stop"`, writes a final step and pushes a `complete` event.

The loop terminates when:
- The LLM produces a final answer (success).
- An abort is requested (aborted).
- The context is cancelled (failed).
- `MaxSteps` is reached (failed).

Dependencies:
- `LLMClient` - interface for LLM chat completion
- `ToolRegistry` - registry of executable tools
- `state.Store` - DynamoDB metadata persistence
- `artifact.Store` - S3 artifact persistence
- `stream.Pusher` - WebSocket event delivery
- `memory.Snapshotter` - memory checkpoint serialization

### Workspace (`pkg/workspace`)

Provides an isolated filesystem sandbox for each run. The `Manager` interface supports:

- `Snapshot(ctx) -> io.ReadCloser` - produces a tar.gz of the workspace directory.
- `Restore(ctx, io.Reader)` - extracts a tar.gz into the workspace directory.
- `Cleanup()` - removes the workspace directory.

The default implementation is `LocalManager`, which creates a temporary directory under `/tmp` namespaced by run ID.

### Memory (`pkg/memory`)

Handles serialization and deserialization of the agent's working memory. A `MemorySnapshot` contains:

- `messages` - the full conversation history (`role` + `content` pairs).
- `scratchpad` - optional free-form text for intermediate reasoning.
- `tool_state` - arbitrary key-value state preserved across checkpoints.

The `Snapshotter` compresses snapshots to gzipped JSON and stores/loads them via `artifact.Store`.

### Stream (`pkg/stream`)

Delivers real-time events to WebSocket clients. The `Pusher` interface sends `StreamEvent` envelopes to individual connections. The `aws_pusher.go` implementation uses the API Gateway Management API to post to WebSocket connections. Dead connections are detected and cleaned up automatically.

The `Chunker` (in `chunker.go`) handles buffering and flushing of token chunks for efficient delivery.

## DynamoDB Table Schemas

AgentForge uses four DynamoDB tables (Tasks, Runs, Steps, Connections), each with composite primary keys (pk + sk). All tables use on-demand billing and server-side encryption with KMS.

### Tasks

| Attribute | Value |
|-----------|-------|
| PK        | `TASK#{task_id}` |
| SK        | `META` |

Stores: `task_id`, `tenant_id`, `user_id`, `status`, `active_run_id`, `abort_requested`, `abort_reason`, `abort_ts`, `idempotency_key`, `prompt`, `model_config`, `created_at`, `updated_at`.

### Runs

| Attribute | Value |
|-----------|-------|
| PK        | `TASK#{task_id}` |
| SK        | `RUN#{run_id}` |

Stores: `task_id`, `run_id`, `tenant_id`, `status`, `parent_run_id`, `resume_from_step_index`, `model_config`, `started_at`, `ended_at`, `last_step_index`.

All runs for a task are co-located under the same partition key, enabling efficient queries.

### Steps

| Attribute | Value |
|-----------|-------|
| PK        | `RUN#{run_id}` |
| SK        | `STEP#{step_index}` (zero-padded to 8 digits) |

Stores: `run_id`, `step_index`, `type`, `status`, `input`, `output`, `ts_start`, `ts_end`, `latency_ms`, `token_usage`, `checkpoint_ref`, `error_code`, `error_message`.

The sort key uses zero-padded step indices (e.g., `STEP#00000003`) to ensure correct lexicographic ordering for range queries.

### Connections

| Attribute | Value |
|-----------|-------|
| PK        | `CONN#{connection_id}` |
| SK        | `META` |

Stores: `connection_id`, `tenant_id`, `user_id`, `task_id`, `run_id`, `connected_at`, `ttl`.

**GSI on task_id**: Enables lookup of all active WebSocket connections subscribed to a given task, used by the engine to fan out stream events.

## S3 Key Structure

All artifacts are stored in a single S3 bucket with tenant-scoped prefixes.

### Memory Snapshots

```
memory/{tenant_id}/{task_id}/{run_id}/step_{index:08d}.json.gz
```

Example: `memory/tenant_abc/task_01/run_01/step_00000003.json.gz`

Format: gzipped JSON of `MemorySnapshot`.

### Workspace Snapshots

```
workspaces/{tenant_id}/{task_id}/{run_id}/step_{index:08d}.tar.gz
```

Example: `workspaces/tenant_abc/task_01/run_01/step_00000003.tar.gz`

Format: tar.gz archive of the entire workspace directory.

## State Machine

### Task Lifecycle

```
QUEUED --> RUNNING --> SUCCEEDED
                  \--> FAILED
                  \--> ABORTED
```

- `QUEUED`: Task created, SQS message enqueued.
- `RUNNING`: Worker has claimed the run and the engine is executing.
- `SUCCEEDED`: Engine completed with a final answer.
- `FAILED`: Engine encountered an error or hit max steps.
- `ABORTED`: Client requested abort and the engine honored it.

On resume, the task transitions from any terminal state back to `QUEUED`.

### Run Lifecycle

```
RUN_QUEUED --> RUNNING --> SUCCEEDED
                      \--> FAILED
                      \--> ABORTED
```

- `RUN_QUEUED`: Run created, waiting for a worker to claim it.
- `RUNNING`: Worker claimed the run via conditional write (`ClaimRun`).
- `SUCCEEDED` / `FAILED` / `ABORTED`: Terminal states set by the worker after engine completion.

The `ClaimRun` transition uses a DynamoDB conditional write (expected status = `RUN_QUEUED`) to guarantee exactly-once claim semantics.

## Idempotency Design

AgentForge provides idempotency at multiple levels:

### Task Creation

Clients may supply an `Idempotency-Key` header or `idempotency_key` in the request body. The `task.Service.Create` method:

1. Queries DynamoDB for an existing task with the same `(tenant_id, idempotency_key)` pair.
2. If found, returns the existing `task_id` and `run_id` without creating duplicates.
3. If not found, creates the task with a conditional write. If a race condition causes `ErrAlreadyExists`, it falls back to fetching the winner.

### Run Claim

The `ClaimRun` operation uses a conditional write requiring `status == RUN_QUEUED`. If SQS delivers the same message twice, the second worker's claim fails with `ErrConflict` and the message is acknowledged without reprocessing.

### Step Writes

`PutStep` uses conditional writes to prevent duplicate step records. If a step with the same `(run_id, step_index)` already exists, the write is a no-op.
