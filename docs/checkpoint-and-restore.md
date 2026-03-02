# Checkpoint and Restore

AgentForge checkpoints the agent's full state after every step of the ReAct loop. This enables resuming a task from any previously completed step, even in a different run with different model configuration.

## Memory Snapshot Format

Memory snapshots are stored as **gzipped JSON** files in S3.

### S3 Key

```
memory/{tenant_id}/{task_id}/{run_id}/step_{index:08d}.json.gz
```

The step index is zero-padded to 8 digits (e.g., `step_00000003.json.gz`).

### Schema

The `MemorySnapshot` struct (defined in `pkg/model/memory.go`) contains:

```json
{
  "run_id": "run_abc123",
  "step_index": 3,
  "messages": [
    {"role": "system", "content": "You are an AI agent..."},
    {"role": "user", "content": "Build a REST API for..."},
    {"role": "assistant", "content": "I'll start by creating..."},
    {"role": "user", "content": "[Tool write_file result]: OK"}
  ],
  "scratchpad": "optional free-form reasoning text",
  "tool_state": {
    "files_written": ["main.go", "handler.go"],
    "custom_key": "arbitrary tool-managed state"
  }
}
```

| Field        | Type                    | Description |
|--------------|-------------------------|-------------|
| `run_id`     | string                  | The run that produced this snapshot. |
| `step_index` | int                     | The step index at which this snapshot was taken. |
| `messages`   | array of MemoryMessage  | The full conversation history (system, user, assistant messages and tool results). |
| `scratchpad` | string (optional)       | Free-form text for intermediate reasoning or notes. |
| `tool_state` | map (optional)          | Arbitrary key-value pairs managed by tools, preserved across checkpoints. |

### Serialization

The `memory.Snapshotter` (in `pkg/memory/snapshot.go`) handles serialization:

1. Marshal the `MemorySnapshot` to JSON via `json.Marshal`.
2. Compress with `gzip.NewWriter`.
3. Write to S3 via `artifact.Store.Put`, which returns the SHA256 hex digest and byte size.
4. Return an `ArtifactRef` containing `s3_key`, `sha256`, and `size`.

## Workspace Snapshot Format

Workspace snapshots are stored as **tar.gz** archives in S3.

### S3 Key

```
workspaces/{tenant_id}/{task_id}/{run_id}/step_{index:08d}.tar.gz
```

### Contents

The archive contains the entire contents of the agent's workspace directory at the time of the checkpoint. This includes any files the agent has created, modified, or downloaded during execution.

### Serialization

The workspace snapshot process (in `pkg/engine/workspace_snapshot.go`):

1. Call `workspace.Manager.Snapshot(ctx)` which produces an `io.ReadCloser` streaming a tar.gz of the workspace root.
2. Write the stream to S3 via `artifact.Store.Put`.
3. Return an `ArtifactRef` with `s3_key`, `sha256`, and `size`.

## CheckpointRef Structure

Each `Step` record in DynamoDB includes a `CheckpointRef` (defined in `pkg/model/task.go`) that links to both snapshots:

```json
{
  "memory": {
    "s3_key": "memory/tenant_abc/task_01/run_01/step_00000003.json.gz",
    "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "size": 4096
  },
  "workspace": {
    "s3_key": "workspaces/tenant_abc/task_01/run_01/step_00000003.tar.gz",
    "sha256": "a1b2c3d4e5f6...",
    "size": 102400
  }
}
```

| Field                | Type         | Description |
|----------------------|--------------|-------------|
| `memory`             | ArtifactRef  | Reference to the gzipped JSON memory snapshot. |
| `memory.s3_key`      | string       | Full S3 key for the memory artifact. |
| `memory.sha256`      | string       | SHA-256 hex digest for integrity verification. |
| `memory.size`        | int64        | Size in bytes of the compressed artifact. |
| `workspace`          | ArtifactRef  | Reference to the tar.gz workspace snapshot. |
| `workspace.s3_key`   | string       | Full S3 key for the workspace artifact. |
| `workspace.sha256`   | string       | SHA-256 hex digest for integrity verification. |
| `workspace.size`     | int64        | Size in bytes of the compressed artifact. |

Either field may be `null` if the corresponding checkpoint failed (non-fatal; the engine logs the error and continues).

## Restore Semantics

When a run is resumed from a checkpoint, the engine (in `pkg/engine/engine.go`, `Execute` method) performs the following sequence:

### Step 1: Load the Checkpoint Step

The engine reads the `Step` record for `(parent_run_id, resume_from_step_index)` from DynamoDB to obtain the `CheckpointRef`.

### Step 2: Restore Memory

If `checkpoint_ref.memory` is present:

1. Download the gzipped JSON from S3 using the `s3_key`.
2. Decompress with `gzip.NewReader`.
3. Unmarshal into a `MemorySnapshot`.
4. Update the `run_id` field to the new run's ID (the snapshot was created under the parent run).

If absent, the engine starts with an empty memory and re-injects the system prompt and user prompt.

### Step 3: Restore Workspace

If `checkpoint_ref.workspace` is present:

1. Download the tar.gz archive from S3 using the `s3_key`.
2. Call `workspace.Manager.Restore(ctx, reader)` to extract the archive into the workspace directory.

If absent, the engine starts with an empty workspace.

### Step 4: Continue Execution

The engine sets `startStep = resume_from_step_index + 1` and enters the normal ReAct loop from that point. All subsequent steps are written under the new run ID.

## Resume Flow

The full resume flow spans the API, service layer, worker, and engine.

### 1. Client Request

```
POST /tasks/{task_id}/resume
Headers:
  X-Tenant-Id: tenant_abc
  X-User-Id: user_01

Body:
{
  "from_run_id": "run_previous",
  "from_step_index": 5,
  "model_config_override": {
    "model_id": "claude-sonnet-4-20250514",
    "temperature": 0.3,
    "max_tokens": 4096
  }
}
```

### 2. Service Layer (`task.Service.Resume`)

1. Validates tenant access to the task.
2. Generates a new `run_id`.
3. Creates a new `Run` record with:
   - `parent_run_id` set to `from_run_id`.
   - `resume_from_step_index` set to `from_step_index`.
   - `model_config` set to the override (or inherited from the task if no override).
   - `status` set to `RUN_QUEUED`.
4. Updates the task's `active_run_id` to the new run.
5. Resets the task status to `QUEUED` (from any terminal state).
6. Enqueues an SQS pointer message for the new run.

### 3. Worker Processing

The worker picks up the SQS message and follows the standard flow:

1. Claims the run (`RUN_QUEUED` -> `RUNNING`).
2. Loads the task and run metadata.
3. Creates a fresh local workspace.
4. Passes the run to `Engine.Execute`.

### 4. Engine Restore and Continue

The engine detects `run.ResumeFromStepIndex != nil` and enters the restore path described above. After restoring memory and workspace, it continues the ReAct loop from `step_index + 1` under the new run ID.

### 5. Response

```json
{
  "task_id": "task_01",
  "run_id": "run_new_abc"
}
```

The client can then subscribe via WebSocket to receive real-time stream events for the new run.
