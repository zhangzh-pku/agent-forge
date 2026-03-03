# AgentForge TODO

## 1. Multi-Tenant Scheduling and Isolation
- [x] Add per-tenant concurrency limits (running tasks and queued tasks).
- [x] Implement fair scheduling across tenants (avoid noisy-tenant starvation).
- [x] Add per-tenant token and cost budgets.
- [x] Add hard circuit breakers (rate, error, budget breach) with clear recovery behavior.
- [x] Expose tenant-level runtime metrics and alerts.

## 2. Production-Grade Storage and Queue Backends
- [ ] Complete DynamoDB/S3/SQS production implementations (replace local-only assumptions).
- [x] Define retry, idempotency, and dead-letter handling strategy for queue consumers.
- [x] Add crash recovery and re-drive flow for stuck/failed runs.
- [x] Add consistency checks and repair tooling for task/run/step state drift.
- [x] Add operational runbooks for partial failures and regional service degradation.

## 3. Model Routing and Cost Optimization
- [x] Build model routing layer (task-type aware model selection).
- [x] Add fallback chain across models/providers with failure policies.
- [x] Add budget-aware routing (quality/cost tradeoff controls).
- [x] Track per-run token/cost usage with attribution by tenant/task.
- [x] Add policy controls (latency-first, quality-first, cost-cap modes).

## 4. Event Replay and Disconnect Recovery
- [x] Persist stream events durably (not only in-memory push).
- [x] Add replay API (resume from sequence offset / timestamp).
- [x] Support client reconnect with gap replay and deduplication.
- [x] Define event retention window and compaction strategy.
- [x] Add end-to-end tests for disconnect/reconnect and missed-event recovery.
