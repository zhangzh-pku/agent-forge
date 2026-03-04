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
| R-003 | `CC-S8`, `REL-S6`, `SEC-NICE-03` | 补齐关键告警基线：当前仅 recovery 告警已落地，还需 DLQ depth、worker error rate、API 5xx、关键安全拒绝事件。 |

## P1 - 可靠性、引擎与 API 行为

| ID | Source | TODO |
|---|---|---|
| R-113 | `CC-D12` | `ListTasks/ListRuns` 从 Scan 迁移到基于 GSI 的 Query。 |
| R-123 | `CC-O2`, `CC-N13` | 接入 OTel/X-Ray 追踪（应用与 Terraform）。 |
| R-132 | `SEC-SHOULD-04`, `CC-N17` | IAM 权限最小化收敛（含审计流程），减少广泛 `Scan`/过宽 S3 动作。 |

## P2 - 测试、发布与工程化补齐

| ID | Source | TODO |
|---|---|---|
| R-201 | `CC-T1` | DynamoStore 测试从“基础集成”补齐到完整 CRUD/条件写/事务语义覆盖。 |
| R-203 | `CC-T3`, `REL-N1` | SQS 集成/契约测试补齐（重复投递、DLQ、删除失败、续租失败等）。 |
| R-212 | `CC-C5` | 建立并发布首个 semver 基线 tag（如 `v0.1.0`）。 |
| R-219 | `CC-C17` | `internal/` 迁移（`pkg/config`, `pkg/util`, `pkg/ops`）。 |
| R-221 | `CC-C19` | 清理历史中误提交的构建产物（`bin/`）。 |
| R-222 | `OSS-S3` | 补 Terraform 专项文档（建议 `docs/terraform.md`）并在 docs 索引链接。 |

## P3 - 长期增强与加固

| ID | Source | TODO |
|---|---|---|
| R-302 | `CC-N2` | Prompt injection 基础防护（长度限制/可选 deny-list）。 |
| R-303 | `CC-N3` | EventStore 增加 tenant 维度隔离策略。 |
| R-305 | `CC-N5` | `Workspace.Snapshot` 降低持锁时间。 |
| R-307 | `CC-N7` | `Workspace.Delete` 支持目录删除语义。 |
| R-308 | `CC-N8` | `Workspace.Snapshot` 显式阻断 symlink traversal。 |
| R-309 | `CC-N9` | SQS primary queue retention 从 1 天调整到 14 天。 |
| R-310 | `CC-N10` | S3 lifecycle rules（转 IA/过期策略）。 |
| R-311 | `CC-N11` | S3 access logging。 |
| R-312 | `CC-N12` | Lambda VPC 化与 VPC endpoints。 |
| R-313 | `CC-N15` | pricing table 外置配置化。 |
| R-314 | `CC-N16` | Chunker 错误历史/计数增强（非仅 last error）。 |
| R-315 | `CC-N18` | 统一 step/event sort key 宽度策略。 |
| R-317 | `CC-N20` | 增加 benchmark（Chunker/MemoryStore 并发/Engine.Execute）。 |
| R-318 | `SEC-NICE-01` | S3 强制 TLS 拒绝策略 + CloudWatch Log Group KMS CMK。 |
| R-319 | `SEC-NICE-02` | 安全扫描门禁补齐剩余项：`gosec` 已接入硬门禁，`checkov` 已接入软门禁；下一步为收敛现有 Terraform 检查项并切换 `checkov` 为硬门禁，同时补 Must/Should 安全回归测试。 |
| R-320 | `REL-N2` | 建立每日一致性巡检与周报。 |
| R-321 | `REL-N3` | 分层 retention 与自动 compaction 计划任务。 |
| R-322 | `REL-N4`, `MAIN-Nice-4` | 季度 game day 故障演练机制化（计划、执行、复盘闭环）。 |
