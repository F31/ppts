# ADR-019：迁移与运行角色分离（为 RLS 纵深防御铺路）

- 状态：已部分实现（2026-09-12：RLS 与运行角色已落地；专属 `ppts_migrator` 账号化部署待做）
- 关联：V4.0 §12.1、§15.3；开发计划 §1.4.1、G3-1
- 编号说明：承接 ADR-018，ppts 内续编为 019。

## 背景

当前没有数据库角色/权限 bootstrap，迁移脚本中出现 `GRANT ... TO ppts_app`（`migrations/0003_artifacts.sql:18-19`、`0004_uploads.sql:22-23`），但未定义迁移账号与运行账号的职责边界。若迁移与运行使用同一账号，则该账号为表 owner；PostgreSQL 中**表 owner 默认绕过 RLS**（即使启用 `FORCE ROW LEVEL SECURITY`，owner 仍可能绕过，除非显式 `FORCE` 且非 owner），届时 G3-1 的 RLS 防线会形同虚设。

同时，V4.0 §12.1 要求：运行账号非 owner、非超级用户、非 `BYPASSRLS`；每个业务事务用 `set_config('app.tenant_id', $1, true)` 设置事务局部上下文，缺失即拒绝。

## 决策

引入两个数据库角色并在迁移/部署中固定职责：

| 角色 | 职责 | 属性 |
|---|---|---|
| `ppts_migrator` | 执行迁移，持有表/索引/策略的 owner | 迁移专用，不用于运行业务流量 |
| `ppts_app` | API 与 worker 运行时访问业务表 | `NOSUPERUSER NOBYPASSRLS`，**非表 owner**；仅授予必要的 `SELECT/INSERT/UPDATE/DELETE` |
| `ppts_scheduler`（随 ADR-018） | 跨租户领取任务 | 仅任务调度表/受限函数权限；不访问业务表 |

配套纪律：

1. 迁移由 `ppts_migrator`（或更高权限的发布步骤）执行，运行时**严禁**使用迁移账号。
2. 所有业务事务通过统一封装（如 `withTenant(ctx, tx)`）执行 `set_config('app.tenant_id', $1, true)`；`G3-1` 在此之上启用 `ENABLE`+`FORCE ROW LEVEL SECURITY` 与租户策略。
3. 迁移权限收敛：`GRANT` 语句集中到角色 bootstrap，不在各迁移中零散授权。

## 影响

- G1 收尾即可落地角色 bootstrap（不改业务逻辑），把 RLS 留到 G3-1 实现时"只加策略不改权限模型"。
- 本地开发/测试库需要按同一角色模型初始化，确保 PG 门禁测试能复现"缺失上下文被拒绝"。
- 需要在部署文档/CI 中固化"迁移账号 ≠ 运行账号"。

## 备选方案

- **暂不分离，G3 再一起做**：会导致 G3-1 需要数据/权限迁移与停机窗口，返工成本高，不采用。
- **运行账号使用 `BYPASSRLS` 简化开发**：直接违背 V4.0 §12.1，不采用。

## 验收

- `ppts_app` 非表 owner、`rolsuper=false`、`rolbypassrls=false`（可用 `pg_roles` 断言）。
- 未设置租户上下文时，运行账号访问受保护表被拒绝。
- 迁移账号不出现在运行时连接字符串中。
