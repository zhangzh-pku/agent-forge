# Review Remaining TODO (Consolidated)

更新时间：2026-03-04 (UTC)  
范围来源：`docs/cc-review-plan.md`、`docs/codex-review-plan.md`、`docs/codex-subplan-security.md`、`docs/codex-subplan-reliability.md`、`docs/codex-subplan-oss.md`

说明：
- 本清单仅汇总“剩余待办”，用于复核。
- 原始 plan 文件暂不删除。
- `Source` 列用于回溯原始条目 ID。
- Source 前缀：`CC`=`cc-review-plan`，`MAIN`=`codex-review-plan`，`SEC`=`codex-subplan-security`，`REL`=`codex-subplan-reliability`，`OSS`=`codex-subplan-oss`。

## P0 - 生产阻断与高风险安全项

| ID | Source | TODO |
|---|---|---|
| R-001 | `CC-S6` | Terraform 增加 WAF：`aws_wafv2_web_acl` + `aws_wafv2_web_acl_association`。 |
| R-002 | `CC-S7` | 为关键 Lambda（至少 worker）设置 `reserved_concurrent_executions`，与上游配额对齐。 |
| R-003 | `CC-S8`, `REL-S6`, `SEC-NICE-03` | 补齐关键告警基线：当前仅 recovery 告警已落地，还需 DLQ depth、worker error rate、API 5xx、关键安全拒绝事件。 |
| R-004 | `CC-D2` | `OPENAI_API_KEY` 从明文环境变量迁移到 AWS Secrets Manager（启动期拉取）。 |
| R-007 | `SEC-MUST-01`, `SEC-MUST-02`, `CC-S1..S3` | Terraform 增加并绑定 `aws_apigatewayv2_authorizer`（HTTP + WebSocket），形成可验证的 JWT/claims 身份链路（当前无显式 authorizer 资源）。 |

## P1 - 可靠性、引擎与 API 行为

| ID | Source | TODO |
|---|---|---|
| R-101 | `CC-E1` | 处理 `finish_reason="length"`：历史裁剪后重试，连续失败终止。 |
| R-102 | `CC-E2` | 实现对话滑动窗口/摘要策略，抑制上下文无限增长。 |
| R-103 | `CC-E3` | `fs.read` 增加可配置读取上限（默认 512KB）并标记截断。 |
| R-104 | `CC-E4` | `fs.export` goroutine 生命周期治理（`ctx.Done`/`errgroup`）。 |
| R-106 | `CC-E6` | Step 大字段（Input/Output）超过阈值时落 S3，仅存引用。 |
| R-108 | `CC-E8` | 流式请求使用独立 `http.Client`（`Timeout=0`）并依赖 context 控时。 |
| R-109 | `CC-E9` | `shouldFallbackOnError` 默认改为 false，仅白名单错误触发 fallback。 |
| R-110 | `CC-E10` | 429 重试读取 `Retry-After` 作为 backoff 下限。 |
| R-111 | `CC-D8` | SQS 消费支持 batch 并发处理（有界 worker pool）。 |
| R-112 | `CC-D10` | FIFO `MessageGroupId` 从 tenant 迁移到 task/run 粒度，减少队头阻塞。 |
| R-113 | `CC-D12` | `ListTasks/ListRuns` 从 Scan 迁移到基于 GSI 的 Query。 |
| R-114 | `CC-D13` | `Workspace.Restore` 改为临时目录解压校验后原子替换。 |
| R-115 | `CC-D14` | Resume 状态机收紧：排除 `TaskStatusRunning`。 |
| R-116 | `CC-D15` | `deadConnections` 增加 TTL/LRU，避免无限增长。 |
| R-117 | `CC-D16` | `IsGoneError` 改为 typed 错误判断（`errors.As` + APIError）。 |
| R-118 | `CC-A2` | 增加 `/health/ready`，检查 DynamoDB/SQS 可达性。 |
| R-119 | `CC-A3` | 优雅关闭增加 `context.WithTimeout`。 |
| R-120 | `CC-A4` | 嵌入式 worker 增加 drain/wait，确保 in-flight 收敛。 |
| R-121 | `CC-A5` | HTTP server 增加 `ReadHeaderTimeout`。 |
| R-122 | `CC-O1` | 增加结构化请求日志（method/path/status/latency/request_id/tenant）。 |
| R-123 | `CC-O2`, `CC-N13` | 接入 OTel/X-Ray 追踪（应用与 Terraform）。 |
| R-124 | `CC-O3` | 暴露 `/metrics`（请求、错误、延迟、队列等核心指标）。 |
| R-125 | `CC-O4` | Logger 增强（等级、时间戳、写入语义一致性）。 |
| R-126 | `CC-O5` | Terraform 增加 CloudWatch Dashboard。 |
| R-128 | `REL-S4` | Dynamo 可靠性集成测试扩展到 claim/transition/compaction/unprocessed-items。 |
| R-129 | `REL-S6`, `MAIN-Should-7` | 暴露并接线可靠性指标/告警（claim_conflict/finalize_fail/push_error/recovery_count/dlq_depth）。 |
| R-130 | `REL-S8` | 事件推送改为有界并发 + 单连接超时 + 限流。 |
| R-132 | `SEC-SHOULD-04`, `CC-N17` | IAM 权限最小化收敛（含审计流程），减少广泛 `Scan`/过宽 S3 动作。 |

## P2 - 测试、发布与工程化补齐

| ID | Source | TODO |
|---|---|---|
| R-201 | `CC-T1` | DynamoStore 测试从“基础集成”补齐到完整 CRUD/条件写/事务语义覆盖。 |
| R-202 | `CC-T2` | 增加 S3 LocalStack 集成测试（Put/Get/Exists/PresignedURL/加密）。 |
| R-203 | `CC-T3`, `REL-N1` | SQS 集成/契约测试补齐（重复投递、DLQ、删除失败、续租失败等）。 |
| R-204 | `CC-T4` | `make test` 与 `-race` 策略统一（按约定强制）。 |
| R-205 | `CC-T5` | 新增 `finish_reason="length"` 场景回归测试。 |
| R-206 | `CC-T7` | 新增 `fs.export` goroutine 泄漏回归测试。 |
| R-207 | `CC-T8` | 新增 streaming abort 中断测试。 |
| R-208 | `CC-T9` | 新增 API 并发幂等竞态测试。 |
| R-209 | `CC-T10` | 新增 `task/estimation.go` 全函数测试。 |
| R-210 | `CC-T11` | 新增 health/ready endpoint 测试。 |
| R-211 | `CC-C4`, `MAIN-Nice-1` | 发布自动化：`.goreleaser.yml` + `release.yml`（tag 触发）。 |
| R-212 | `CC-C5` | 建立并发布首个 semver 基线 tag（如 `v0.1.0`）。 |
| R-213 | `CC-C11` | 增加生产可用多阶段 `Dockerfile`。 |
| R-214 | `CC-C12` | 增加 `docker-compose.yml`（taskapi/worker + optional LocalStack）。 |
| R-215 | `CC-C13` | 增加 `examples/quickstart.sh`。 |
| R-217 | `CC-C15` | 增加 `docs/openapi.yaml`。 |
| R-218 | `CC-C16` | API 路由版本化（`/v1`）。 |
| R-219 | `CC-C17` | `internal/` 迁移（`pkg/config`, `pkg/util`, `pkg/ops`）。 |
| R-220 | `CC-C18` | `go.mod` module path 与真实仓库路径对齐。 |
| R-221 | `CC-C19` | 清理历史中误提交的构建产物（`bin/`）。 |
| R-222 | `OSS-S3` | 补 Terraform 专项文档（建议 `docs/terraform.md`）并在 docs 索引链接。 |

## P3 - 长期增强与加固

| ID | Source | TODO |
|---|---|---|
| R-301 | `CC-N1` | `OPENAI_BASE_URL` 做 SSRF 防护（scheme 白名单+禁止 IP 字面量）。 |
| R-302 | `CC-N2` | Prompt injection 基础防护（长度限制/可选 deny-list）。 |
| R-303 | `CC-N3` | EventStore 增加 tenant 维度隔离策略。 |
| R-305 | `CC-N5` | `Workspace.Snapshot` 降低持锁时间。 |
| R-306 | `CC-N6` | workspace 默认目录权限从 `0755` 收敛到 `0700`。 |
| R-307 | `CC-N7` | `Workspace.Delete` 支持目录删除语义。 |
| R-308 | `CC-N8` | `Workspace.Snapshot` 显式阻断 symlink traversal。 |
| R-309 | `CC-N9` | SQS primary queue retention 从 1 天调整到 14 天。 |
| R-310 | `CC-N10` | S3 lifecycle rules（转 IA/过期策略）。 |
| R-311 | `CC-N11` | S3 access logging。 |
| R-312 | `CC-N12` | Lambda VPC 化与 VPC endpoints。 |
| R-313 | `CC-N15` | pricing table 外置配置化。 |
| R-314 | `CC-N16` | Chunker 错误历史/计数增强（非仅 last error）。 |
| R-315 | `CC-N18` | 统一 step/event sort key 宽度策略。 |
| R-316 | `CC-N19` | `PresignedURL` 先 `HeadObject`，对不存在 key 返回 `ErrNotFound`。 |
| R-317 | `CC-N20` | 增加 benchmark（Chunker/MemoryStore 并发/Engine.Execute）。 |
| R-318 | `SEC-NICE-01` | S3 强制 TLS 拒绝策略 + CloudWatch Log Group KMS CMK。 |
| R-319 | `SEC-NICE-02` | 安全扫描门禁补齐剩余项：`gosec` 已接入硬门禁，`checkov` 已接入软门禁；下一步为收敛现有 Terraform 检查项并切换 `checkov` 为硬门禁，同时补 Must/Should 安全回归测试。 |
| R-320 | `REL-N2` | 建立每日一致性巡检与周报。 |
| R-321 | `REL-N3` | 分层 retention 与自动 compaction 计划任务。 |
| R-322 | `REL-N4`, `MAIN-Nice-4` | 季度 game day 故障演练机制化（计划、执行、复盘闭环）。 |
