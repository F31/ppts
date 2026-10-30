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

- [ ] 4.1 `internal/usage` 新增 `KindAnimationMinutes`
- [ ] 4.2 导出受理时预占、渲染完成时按真实时长结算；失败释放
- [ ] 4.3 超限返回明确原因与恢复时间（不静默排队）
- [ ] 4.4 用例：预占/结算/释放、超限拒绝、并发预占不超卖

## 5. 轨道编辑 UI（第 3 段）

- [ ] 5.1 元素级时间轴与关键帧拖拽（受 `capabilities.trackEditor` 控制）
- [ ] 5.2 撤销/重做 ≥20 步（本地栈，不落库）；方向键 10ms / Shift 50ms
- [ ] 5.3 批量套用带撤销；空状态文案（§7.6）
- [ ] 5.4 前端门禁：`contrast.mjs`、`i18n-check.mjs`、`tsc`、`vite build`

## 6. 归档

- [ ] 6.1 `go build ./... && go vet ./... && go test ./internal/... ./cmd/...`
- [ ] 6.2 `openspec validate --all`
- [ ] 6.3 部署文档：`PPTS_ANIMATION_ROLLOUT` 的收口步骤与「salt 不得轮换」禁令
- [ ] 6.4 能力规格吸收进 `openspec/specs/animation-rollout`；路线图 M4 行更新
