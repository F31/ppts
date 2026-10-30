# 任务：轨道编辑与分级开放（M4）

> 判据统一写在每条任务里；没有实测值的勾不算数。

## 1. 能力判定（第 1 段）

- [x] 1.1 `internal/animation/capability.go`：`AnimationPolicy`（`*bool` / `*int` 区分未设置与显式值）、
      `GateConfig{DeployEnabled, DefaultPercent, Salt}`、`Capability{Enabled, TrackEditor, Preview, Source, Bucket, Reason}`
- [x] 1.2 判定优先级：部署闸 > 租户显式 > 租户 rollout > 部署默认；子能力仅在总开关可用时为真
- [x] 1.3 `Bucket(tenantID, salt)`：SHA-256 前 8 字节取模 100，`\x00` 分隔避免拼接撞桶
- [x] 1.4 用例：部署闸关压过一切；显式 true 在 0% 可用；显式 false 在 100% 仍关；
      未设走 rollout；未设且无 rollout 走默认；子能力不越总闸；空租户 ID 判不可用
- [x] 1.5 用例：同一 ID 1000 次同桶；10000 个 `t_000001` 形 ID 的十分位占比 7%–13%

## 2. 租户策略接线（第 1 段）

- [x] 2.1 `tenant.Policy` 新增 `Animation *animation.AnimationPolicy`（jsonb 新键，无迁移）
- [x] 2.2 用例：往返序列化保真（`null` 与 `false` 不混淆）；旧 policy 无该键时解析为零值

## 3. 三处入口共用判定（第 1 段）

- [x] 3.1 `internal/api/animation_gate.go`：`AnimationGate` 接口 + `tenantAnimationGate`
      （策略读取失败 fail closed，`ErrTenantNotFound` 视为未配置）+ `deployGate`（旧构造等价）
- [x] 3.2 `GET /exports/animation-options`：响应新增 `capabilities`；`enabled`/`reason` 保持兼容
- [x] 3.3 `PUT .../animation` 走 gate：关闭时 404（先取 principal 再判定）
- [x] 3.4 `GET .../animation` 与 `/anchors` 不受影响（灰度前可验契约）
- [x] 3.5 导出入口复用同一判定（`animationUnavailableReason` 增加租户维度）
- [x] 3.6 `cmd/ppts`：`PPTS_ANIMATION_ROLLOUT`（默认 100，兼容既有部署）与
      `PPTS_ANIMATION_ROLLOUT_SALT`；非法值告警并回落
- [x] 3.7 用例：关闭后导出静态且 0 副作用；在途任务不受影响；策略读失败 fail closed 且原因可区分
- [x] 3.8 前端：`export.ts` 透传 `capabilities`（本段不做 UI 控制，第 3 段用）

## 4. 配额 `animation.minutes`（第 2 段）

- [x] 4.1 `internal/usage` 新增 `KindAnimationMinutes`；账期限定 `Kind.Period/Base/PeriodOf`、
      `MonthPeriod`（UTC）、`MonthReset`。账期编进计量种类，跨月是额度表新行——
      这样"按月"与"单行条件更新防超卖"能同时成立（design ADR-7）
- [x] 4.2 导出受理时按时间轴总时长预占（`internal/api/animation_quota.go`）；
      渲染完成按**实际渲了动画的页**结算（`internal/app/animation_quota.go`，
      降级页不计费、整体退回静态结算 0）；失败终态与取消释放（worker 新增 `OnFailed`，
      与既有 `OnCanceled` 对称；重试期间保留预占）
- [x] 4.3 超限 429 + `code=animation_quota_exceeded` + `limitMinutes`/`usedMinutes`/
      `requestedMinutes`/`reservedMinutes`/`resetAt` + `Retry-After`；前端翻成
      「将于 {date} 恢复 / 可以先导出静态版」
- [x] 4.4 用例：预占（5 分钟/上限 60）、超限拒绝（上限 4 < 5）且不建任务、
      建任务失败后释放、静态导出不占额度、**30 并发上限 10 每单 4 → 只成功 2 单（预占 8）**；
      结算侧 5 条（降级页排除、整体静态结算 0、跨月按任务创建时间、账本报错不阻断、无账本 noop）；
      `OnFailed` 三条（永久失败 / 重试耗尽 / 仍有重试机会不触发）

## 5. 轨道编辑 UI（第 3 段）

- [x] 5.1 元素级时间轴（`src/AnimationTrackTimeline.tsx`）：每条元素轨道一行，色块按页时长定位；
      拖动改起点、拖右边缘改时长、色块可聚焦并支持键盘微调；受 `capabilities.trackEditor` 控制，
      未开通只说一句说明、页时长未知时只读（不给一个必然 400 的输入）
- [x] 5.2 撤销/重做：本地栈深 24（≥20），一次拖拽只记一步（pointerdown 记、move 不记），
      Ctrl/Cmd+Z 与 Shift+Ctrl/Cmd+Z；方向键 10ms / Shift 50ms；
      **拖动派生起点的片段同时改 trigger 为 `atTime:<ms>`**（否则保存成功、成片不变）
- [x] 5.3 空状态文案（§7.6）：无可编排元素时给出说明。
      **批量套用未做**：需要页列表多选 + N 页写入的失败回滚语义，留作独立变更（design「不做（第 3 段）」）
- [x] 5.4 前端门禁：`tsc -b` 通过、`vite build` 通过、`contrast.mjs` 全绿、
      `i18n-check` 中英 1333/1333 对等且无新增零引用、
      `animation-check` **26/26**（新增 10 条：拖动转 atTime、双端夹紧、时长截断、
      键盘步长、几何百分比、撤销栈深与重做清空）

## 6. 归档

- [ ] 6.1 `go build ./... && go vet ./... && go test ./internal/... ./cmd/...`
- [ ] 6.2 `openspec validate --all`
- [ ] 6.3 部署文档：`PPTS_ANIMATION_ROLLOUT` 的收口步骤与「salt 不得轮换」禁令
- [ ] 6.4 能力规格吸收进 `openspec/specs/animation-rollout`；路线图 M4 行更新
