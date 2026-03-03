# AgentForge Operations Runbook

This runbook covers partial failures, dead-letter re-drive, consistency repair,
and regional degradation response for AgentForge deployments.

## 1. Partial Failures (Single Run / Tenant)

### Symptoms
- Task stuck in `RUNNING` or `QUEUED` longer than expected.
- Tenant runtime alert spikes (`error_burst`, `breaker_opened`).
- Increased queue retries / DLQ growth.

### Immediate Actions
1. Inspect tenant runtime:
   - `GET /tenants/{tenant_id}/runtime`
2. Inspect recent tenant alerts:
   - `GET /tenants/{tenant_id}/alerts?limit=100`
3. Inspect run status and usage:
   - `GET /tasks/{task_id}/runs/{run_id}`
4. Replay missed stream events for debugging:
   - `GET /tasks/{task_id}/runs/{run_id}/events/replay?from_seq={seq}`

### Recovery
1. Run stale-run recovery (`pkg/ops.Recoverer`) with a stale threshold.
2. Re-drive DLQ messages (`queue.MemoryQueue.RedriveDeadLetters`).
3. Re-check run progression and tenant breaker state.

## 2. Consistency Drift Repair

### Drift Signals
- Task status does not match active run status.
- `last_step_index` does not match latest persisted step.
- `active_run_id` references a missing run.

### Procedure
1. Run consistency scan (`pkg/ops.ConsistencyChecker.Check`).
2. Review each issue and scope (`tenant_id`, `task_id`, `run_id`).
3. Apply targeted repair (`pkg/ops.ConsistencyChecker.Repair`).
4. Verify corrected state via:
   - `GET /tasks/{task_id}`
   - `GET /tasks/{task_id}/runs/{run_id}`
   - `GET /tasks/{task_id}/runs/{run_id}/steps`

## 3. Event Replay / Disconnect Recovery

### Client Reconnect Flow
1. Client reconnects using:
   - `POST /ws/connect` with `task_id`, `run_id`, `last_seq`
2. Server replays gap events (`seq > last_seq`) and resumes live push.
3. If required, client can independently request replay via HTTP endpoint.

### Retention and Compaction
- Events are retained in store memory with default rolling retention.
- Manual compaction endpoint:
  - `POST /tasks/{task_id}/runs/{run_id}/events/compact`
  - Body: `{ "before_ts": <unix_ts> }`

## 4. Regional Service Degradation

### Typical Triggers
- Provider-wide LLM errors (`429/5xx/timeouts`).
- Queue backlog grows across all tenants.
- WebSocket push failures increase.

### Mitigation Checklist
1. Switch routing policy to `latency-first` or `cost-cap`:
   - `model_config.policy_mode`
2. Add fallback providers/models:
   - `AGENTFORGE_LLM_FALLBACK_PROVIDERS`
   - `model_config.fallback_model_ids`
3. Raise or tune tenant breaker cooldown/limits where safe.
4. Temporarily increase worker fleet / consumer throughput.
5. Re-drive affected stale runs after upstream recovery.

## 5. Post-Incident

1. Export and archive:
   - Tenant alerts
   - Run-level token/cost usage
   - DLQ counts and retry depth
2. Reconcile drift with consistency checker.
3. Tighten per-tenant policy thresholds for repeat offenders.
