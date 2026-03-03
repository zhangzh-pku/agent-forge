# API Reference

## Authentication

All REST endpoints require authenticated tenant + user identity.

- Header mode (local/dev): `X-Tenant-Id`, `X-User-Id`
- Trusted mode (aws recommended): `X-Authenticated-Tenant-Id`, `X-Authenticated-User-Id`

```
X-Tenant-Id: tenant_abc    (required in header mode)
X-User-Id: user_01          (required in header mode)
```

Authentication is enforced by `AuthMiddleware` in `pkg/api/middleware.go`. The current implementation uses simple header extraction; it is designed to be replaceable with JWT or IAM-based authentication.

---

## Endpoints

### POST /tasks

Create a new agent task. Returns immediately after persisting the task and enqueueing it for execution.

**Headers**

| Header           | Required | Description |
|------------------|----------|-------------|
| `X-Tenant-Id`    | Yes      | Tenant identifier. |
| `X-User-Id`      | Yes      | User identifier (header mode). |
| `Idempotency-Key`| No       | Idempotency key (alternative to body field). |

**Request Body**

```json
{
  "prompt": "Build a REST API that returns weather data for a given city.",
  "model_config": {
    "model_id": "claude-sonnet-4-20250514",
    "temperature": 0.7,
    "max_tokens": 4096
  },
  "idempotency_key": "client-req-001"
}
```

| Field              | Type   | Required | Description |
|--------------------|--------|----------|-------------|
| `prompt`           | string | Yes      | The task prompt for the agent. |
| `model_config`     | object | No       | LLM configuration. See Model Config below. |
| `idempotency_key`  | string | No       | Client-supplied key for idempotent creation. Also accepted via the `Idempotency-Key` header. |

**Model Config**

| Field         | Type   | Description |
|---------------|--------|-------------|
| `model_id`    | string | LLM model identifier. |
| `temperature` | float  | Sampling temperature. |
| `max_tokens`  | int    | Maximum output tokens. |

Validation rules:
- `model_id` must be in the allowed model list (`AGENTFORGE_ALLOWED_MODEL_IDS` or built-in defaults).
- `temperature` must be in `[0, 2]`.
- `max_tokens` must be `>= 0` (`0` means provider default).

**Response** `201 Created`

```json
{
  "task_id": "task_a1b2c3d4",
  "run_id": "run_e5f6g7h8"
}
```

**Idempotency Behavior**: If a task with the same `(tenant_id, idempotency_key)` already exists, the endpoint returns the existing `task_id` and `run_id` with a `201` status. No duplicate task is created.

---

### GET /tasks/{task_id}

Retrieve the current state of a task.

**Headers**

| Header        | Required | Description |
|---------------|----------|-------------|
| `X-Tenant-Id` | Yes      | Must match the task's tenant. |

**Response** `200 OK`

```json
{
  "task_id": "task_a1b2c3d4",
  "tenant_id": "tenant_abc",
  "user_id": "user_01",
  "status": "RUNNING",
  "active_run_id": "run_e5f6g7h8",
  "abort_requested": false,
  "prompt": "Build a REST API that returns weather data for a given city.",
  "model_config": {
    "model_id": "claude-sonnet-4-20250514",
    "temperature": 0.7,
    "max_tokens": 4096
  },
  "created_at": "2025-03-15T10:30:00Z",
  "updated_at": "2025-03-15T10:30:05Z"
}
```

**Task Status Values**: `QUEUED`, `RUNNING`, `SUCCEEDED`, `FAILED`, `ABORTED`.

**Error** `404 Not Found` if the task does not exist or belongs to a different tenant.

---

### GET /tasks/{task_id}/runs/{run_id}/steps

List execution steps for a specific run, with pagination.

**Headers**

| Header        | Required | Description |
|---------------|----------|-------------|
| `X-Tenant-Id` | Yes      | Must match the task's tenant. |

**Query Parameters**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `from`    | int  | `0`     | Starting step index (inclusive). |
| `limit`   | int  | `200`   | Maximum number of steps to return. |

**Response** `200 OK`

```json
{
  "steps": [
    {
      "run_id": "run_e5f6g7h8",
      "step_index": 0,
      "type": "llm_call",
      "status": "OK",
      "input": "I'll start by creating the project structure...",
      "output": "",
      "ts_start": "2025-03-15T10:30:01Z",
      "ts_end": "2025-03-15T10:30:03Z",
      "latency_ms": 2000,
      "token_usage": {
        "input": 150,
        "output": 200,
        "total": 350
      },
      "checkpoint_ref": {
        "memory": {
          "s3_key": "memory/tenant_abc/task_a1b2c3d4/run_e5f6g7h8/step_00000000.json.gz",
          "sha256": "e3b0c442...",
          "size": 2048
        },
        "workspace": {
          "s3_key": "workspaces/tenant_abc/task_a1b2c3d4/run_e5f6g7h8/step_00000000.tar.gz",
          "sha256": "a1b2c3d4...",
          "size": 51200
        }
      }
    },
    {
      "run_id": "run_e5f6g7h8",
      "step_index": 1,
      "type": "tool_call",
      "status": "OK",
      "input": "Creating main.go...",
      "output": "OK",
      "ts_start": "2025-03-15T10:30:03Z",
      "ts_end": "2025-03-15T10:30:04Z",
      "latency_ms": 1000,
      "token_usage": null,
      "checkpoint_ref": { "..." : "..." }
    }
  ]
}
```

**Step Types**: `llm_call`, `tool_call`, `observation`, `final`.

**Step Status Values**: `OK`, `ERROR`. When status is `ERROR`, the `error_code` and `error_message` fields are populated.

---

### POST /tasks/{task_id}/abort

Request cancellation of a running task. The abort is cooperative: the engine checks the abort flag before each step and stops at the next opportunity.

**Headers**

| Header        | Required | Description |
|---------------|----------|-------------|
| `X-Tenant-Id` | Yes      | Must match the task's tenant. |

**Request Body**

```json
{
  "reason": "User requested cancellation"
}
```

| Field    | Type   | Required | Description |
|----------|--------|----------|-------------|
| `reason` | string | No       | Human-readable reason for the abort. |

**Response** `200 OK`

Returns the updated task object with `abort_requested: true`:

```json
{
  "task_id": "task_a1b2c3d4",
  "status": "RUNNING",
  "abort_requested": true,
  "abort_reason": "User requested cancellation",
  "abort_ts": "2025-03-15T10:31:00Z",
  "...": "..."
}
```

The task status transitions to `ABORTED` asynchronously once the engine processes the flag.

---

### POST /tasks/{task_id}/resume

Resume a task from a previously checkpointed step. Creates a new run that restores the agent's memory and workspace from the specified checkpoint and continues execution.

**Headers**

| Header        | Required | Description |
|---------------|----------|-------------|
| `X-Tenant-Id` | Yes      | Must match the task's tenant. |

**Request Body**

```json
{
  "from_run_id": "run_e5f6g7h8",
  "from_step_index": 5,
  "model_config_override": {
    "model_id": "claude-sonnet-4-20250514",
    "temperature": 0.3,
    "max_tokens": 8192
  }
}
```

| Field                   | Type   | Required | Description |
|-------------------------|--------|----------|-------------|
| `from_run_id`           | string | Yes      | The run to restore from. |
| `from_step_index`       | int    | Yes      | The step index whose checkpoint to restore. |
| `model_config_override` | object | No       | Override the model configuration for the new run. Falls back to the task's original config if omitted. |

**Response** `200 OK`

```json
{
  "task_id": "task_a1b2c3d4",
  "run_id": "run_new_x9y0z1"
}
```

The new run:
- Has `parent_run_id` set to `from_run_id`.
- Has `resume_from_step_index` set to `from_step_index`.
- Starts in `RUN_QUEUED` status and is enqueued for worker pickup.
- The task status is reset to `QUEUED`.
- Any pending `abort_requested` flag is cleared so the new run executes normally.

---

### GET /health

Health check endpoint. Returns a simple `200 OK` status. Not authenticated.

**Response** `200 OK`

```json
{"status":"ok"}
```

---

## Built-in Tools

The engine provides a set of built-in filesystem tools that are available to agents during execution. These tools operate within the workspace sandbox and respect path traversal protections and quota limits.

| Tool | Description | Arguments |
|------|-------------|-----------|
| `fs.write` | Write a file to the workspace | `path` (string), `content` (string) or `content_base64` (string) |
| `fs.read` | Read a file from the workspace | `path` (string) |
| `fs.list` | List files in a directory | `dir` (string, default `.`) |
| `fs.stat` | Get file metadata (name, path, size, is_dir) | `path` (string) |
| `fs.delete` | Delete a file from the workspace | `path` (string) |
| `fs.export` | Export workspace files as a tar.gz artifact | `paths` (string array) |

All file paths are relative to the workspace root (`/tmp/agentforge/{run_id}/`). Absolute paths and paths containing `..` are rejected.

**Workspace Limits** (configurable):
- Maximum total size: 50 MB
- Maximum file count: 2,000

---

## WebSocket Events

### Connection Management

WebSocket connections are managed via two internal endpoints used by the API Gateway Lambda authorizer and disconnect handler:

- `POST /ws/connect` - Registers a connection (called by `cmd/wsconnect`).
- `POST /ws/disconnect` - Removes a connection (called by `cmd/wsdisconnect`).

Connections are stored in DynamoDB with a 2-hour TTL and indexed by `task_id` via a GSI.

### Event Envelope

All WebSocket events use the `StreamEvent` envelope format:

```json
{
  "task_id": "task_a1b2c3d4",
  "run_id": "run_e5f6g7h8",
  "seq": 42,
  "ts": 1710500400,
  "type": "token_chunk",
  "data": { "..." : "..." }
}
```

| Field     | Type   | Description |
|-----------|--------|-------------|
| `task_id` | string | The task this event belongs to. |
| `run_id`  | string | The run this event belongs to. |
| `seq`     | int64  | Monotonically increasing sequence number for ordering. |
| `ts`      | int64  | Unix timestamp of when the event was generated. |
| `type`    | string | Event type (see below). |
| `data`    | object | Event-specific payload. |

### Event Types

#### `token_chunk`

Emitted as the LLM generates tokens (streamed output). **Note:** This event type is reserved for real LLM streaming integration. The current mock LLM does not emit token-level chunks; it will be activated when a streaming LLM client is connected.

```json
{
  "type": "token_chunk",
  "data": {
    "text": "I'll create the project"
  }
}
```

#### `step_start`

Emitted when a new step begins in the ReAct loop.

```json
{
  "type": "step_start",
  "data": {
    "step_index": 3
  }
}
```

#### `step_end`

Emitted when a step completes.

```json
{
  "type": "step_end",
  "data": {
    "step_index": 3,
    "type": "tool_call",
    "latency_ms": 1500
  }
}
```

#### `tool_call`

Emitted when the engine invokes a tool.

```json
{
  "type": "tool_call",
  "data": {
    "tool": "write_file",
    "args": "{\"path\": \"main.go\", \"content\": \"package main...\"}"
  }
}
```

#### `tool_result`

Emitted when a tool execution completes.

```json
{
  "type": "tool_result",
  "data": {
    "tool": "write_file",
    "output": "OK"
  }
}
```

#### `complete`

Emitted when the agent finishes successfully with a final answer.

```json
{
  "type": "complete",
  "data": {
    "final_answer": "I've built the REST API with the following endpoints..."
  }
}
```

#### `error`

Emitted when the engine encounters a fatal error (e.g., LLM failure, max steps reached). The run transitions to `FAILED` status. Clients can also poll `GET /tasks/{task_id}` or check the steps endpoint for error details.

```json
{
  "type": "error",
  "data": {
    "error_code": "LLM_ERROR",
    "message": "upstream model returned 500"
  }
}
```

---

## Additional Runtime Endpoints

### GET /tasks/{task_id}/runs/{run_id}

Returns run-level metadata including cumulative token and cost attribution.

**Response** `200 OK`

```json
{
  "task_id": "task_a1b2c3d4",
  "run_id": "run_e5f6g7h8",
  "status": "SUCCEEDED",
  "last_step_index": 8,
  "total_token_usage": {
    "input": 1200,
    "output": 900,
    "total": 2100
  },
  "total_cost_usd": 0.00735
}
```

### GET /tasks/{task_id}/runs/{run_id}/events/replay?from_seq=100&from_ts=1710500400&limit=200

Replays persisted stream events for reconnect and missed-event recovery.

**Behavior**
- `from_seq`: returns events with `seq > from_seq`
- `from_ts`: optional lower-bound timestamp
- `limit`: max events returned (default 200, max 2000)

### POST /tasks/{task_id}/runs/{run_id}/events/compact

Compacts old persisted events for the run.

**Request**

```json
{
  "before_ts": 1710500000
}
```

**Response**

```json
{
  "removed": 42
}
```

### GET /tenants/{tenant_id}/runtime

Returns tenant runtime counters used for multi-tenant scheduling and isolation.

### GET /tenants/{tenant_id}/alerts?limit=50

Returns recent tenant alerts (queue depth, rate limiting, budget breach, breaker transitions).

---

## WebSocket Reconnect Replay

`POST /ws/connect` supports replay hints:

```json
{
  "connection_id": "conn_123",
  "task_id": "task_a1b2c3d4",
  "run_id": "run_e5f6g7h8",
  "last_seq": 120
}
```

On reconnect, AgentForge replays gap events (`seq > last_seq`) before continuing live pushes.
