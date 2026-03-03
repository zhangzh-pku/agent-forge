# AgentForge TODO

## 1. Multi-Tenant Scheduling and Isolation
- [ ] Add per-tenant concurrency limits (running tasks and queued tasks).
- [ ] Implement fair scheduling across tenants (avoid noisy-tenant starvation).
- [ ] Add per-tenant token and cost budgets.
- [ ] Add hard circuit breakers (rate, error, budget breach) with clear recovery behavior.
- [ ] Expose tenant-level runtime metrics and alerts.

## 2. Production-Grade Storage and Queue Backends
- [ ] Complete DynamoDB/S3/SQS production implementations (replace local-only assumptions).
- [ ] Define retry, idempotency, and dead-letter handling strategy for queue consumers.
- [ ] Add crash recovery and re-drive flow for stuck/failed runs.
- [ ] Add consistency checks and repair tooling for task/run/step state drift.
- [ ] Add operational runbooks for partial failures and regional service degradation.

## 3. Model Routing and Cost Optimization
- [ ] Build model routing layer (task-type aware model selection).
- [ ] Add fallback chain across models/providers with failure policies.
- [ ] Add budget-aware routing (quality/cost tradeoff controls).
- [ ] Track per-run token/cost usage with attribution by tenant/task.
- [ ] Add policy controls (latency-first, quality-first, cost-cap modes).

## 4. Event Replay and Disconnect Recovery
- [ ] Persist stream events durably (not only in-memory push).
- [ ] Add replay API (resume from sequence offset / timestamp).
- [ ] Support client reconnect with gap replay and deduplication.
- [ ] Define event retention window and compaction strategy.
- [ ] Add end-to-end tests for disconnect/reconnect and missed-event recovery.
