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

## P1 - 可靠性、引擎与 API 行为

| ID | Source | TODO |
|---|---|---|

## P2 - 测试、发布与工程化补齐

| ID | Source | TODO |
|---|---|---|

## P3 - 长期增强与加固

| ID | Source | TODO |
|---|---|---|
| R-312 | `CC-N12` | Lambda VPC 化与 VPC endpoints。 |
| R-315 | `CC-N18` | 统一 step/event sort key 宽度策略。 |
| R-317 | `CC-N20` | 增加 benchmark（Chunker/MemoryStore 并发/Engine.Execute）。 |
| R-318 | `SEC-NICE-01` | S3 强制 TLS 拒绝策略 + CloudWatch Log Group KMS CMK。 |
| R-319 | `SEC-NICE-02` | 安全扫描门禁补齐剩余项：`gosec` 已接入硬门禁，`checkov` 已接入软门禁；下一步为收敛现有 Terraform 检查项并切换 `checkov` 为硬门禁，同时补 Must/Should 安全回归测试。 |
| R-320 | `REL-N2` | 建立每日一致性巡检与周报。 |
| R-321 | `REL-N3` | 分层 retention 与自动 compaction 计划任务。 |
| R-322 | `REL-N4`, `MAIN-Nice-4` | 季度 game day 故障演练机制化（计划、执行、复盘闭环）。 |
