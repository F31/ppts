# 提案：轨道编辑与分级开放（M4）

## 为什么

今天的动画开关只有一种：部署级 env `PPTS_ANIMATION_ENABLED`。它开，所有租户都能写动画规格；它关，
所有租户都不能。这套机制在 M2/M3 够用，到 M4 有两个硬问题：

1. **无法按租户开放**。设计方案 §7.5 的开放节奏是「先内部租户 → 白名单 → 按租户开启 → 付费套餐」，
   部署级开关只能整台机器一起翻。
2. **无法灰度**。§11.5 要求按租户 ID 哈希分桶 5% → 20% → 50% → 100%，且回滚只需关总开关。
   现在关总开关会连白名单租户一起关掉。

同时 M4 要交付元素级轨道编辑，它必须挂在同一个能力判定下：**界面能不能显示**、**API 能不能写**、
**导出能不能出动画**，三处必须是同一个答案。三处各判一次，必然出现「面板能打开但保存 404」
「对话框说能用、导出来是静态」这类分叉——M3 的验收已经踩过一次（api/worker 两套开关判定）。

## 变更内容

### 1. 租户策略新增 `animation` 段（无迁移）

`tenants.policy` 是 jsonb，新增键不需要迁移：

```json
{
  "animation": {
    "enabled": true,
    "track_editor": true,
    "preview": true,
    "max_minutes": 120,
    "rollout_percent": 20
  }
}
```

字段用指针区分「未设置」与「显式 false」。这不是洁癖：`enabled: false`（运营明确关掉这个租户）
与「没配过」（走灰度）是两种不同的语义，用零值无法区分，而两者的行为差别是「关掉一个客户」
还是「这个客户刚好没进桶」。

### 2. 能力判定收敛到一个函数

`animation.AnimationPolicy.Resolve(tenantID, GateConfig) Capability` 是唯一判定。优先级：

```
部署闸（PPTS_ANIMATION_ENABLED=false）  → 一律关
租户显式 enabled                        → 用显式值（true 绕过灰度，false 压过灰度）
租户设了 rollout_percent                → 按桶
都没设                                  → 用部署级默认百分比
```

返回 `Source`（deploy / tenant / rollout / default / policy）与 `Reason`（可诊断的关闭原因），
前端不再只能显示「勾选框是灰的」。

### 3. 三处入口共用这个判定

| 入口 | 关闭时的行为 |
|---|---|
| `GET /exports/animation-options` | `enabled:false` + `reason`，前端隐藏动画选项 |
| `PUT /slides/{id}/animation`（写规格） | **404**（沿用既有约定：不暴露能力存在） |
| `GET /slides/{id}/animation`（读） | **照常可用**（灰度前要能验契约，编辑器也要能解释为什么不能改） |

导出入口（`internal/api` 里提交导出时）复用同一个判定，杜绝「对话框说能用、导出静默降级」。

### 4. 配额：`animation.minutes`（第 2 段）

复用 `internal/usage` 的预占/结算账本，新增计量种类 `animation_minutes`。超限返回明确原因与
恢复时间（设计 §7.5：不静默排队）。

### 5. 轨道编辑 UI（第 3 段）

元素级关键帧编辑，受 `animation.track_editor` 控制。本段只落能力位，UI 随后。

## 影响

- `internal/tenant/policy.go`：新增 `Animation *animation.AnimationPolicy`（jsonb 新键，无迁移）。
- `internal/api`：能力解析器与三处入口；`animation-options` 响应新增 `capabilities`（`enabled`/`reason`
  保持原样，前端不需改）。
- `cmd/ppts`：新增 `PPTS_ANIMATION_ROLLOUT`（默认 100）与 `PPTS_ANIMATION_ROLLOUT_SALT`。
- 前端：本段无必改项；`capabilities.trackEditor` 供第 3 段使用。

## 一个必须承认的迁移决策

设计方案 §10.3 写的是 `animation.enabled` 租户默认 **false**。本变更**没有**直接把默认值翻成 false，
而是引入部署级默认百分比 `PPTS_ANIMATION_ROLLOUT`，**默认 100**——即「已开启动画通道的部署，
升级后行为不变」。

理由：一次发版就把所有在用租户的动画关掉，是比「默认值不够严格」严重得多的故障。翻转默认值应当是
一次独立的、有通知的发版，前提是所有活跃租户都已写入显式策略。生产建议显式设
`PPTS_ANIMATION_ROLLOUT=0`，再逐个开租户。这条写进 design ADR-3，不藏在代码注释里。

## 不做

- 按用户灰度（租户是成本与支持边界，用户级分桶会让同一份文档在两个同事那里表现不同）。
- 套餐计费系统对接（套餐由运营写进租户策略，M4 不引入 plan 表）。
- `animation.brand_presets`（§7.5 表里属「团队」能力，等团队模型落地）。
