# 任务：跨场景元素延续（Clip.continues）

> 交付纪律：本变更对应设计方案 §19 批次三的 G8，采用路线 B（手术式跨页延续）。纯增量、可选字段、
> 无 `continues` 时画面与引入前逐像素一致。合并门禁：`go build ./...`、`go vet ./...`、
> `go test ./...`（含编译拆分 Go 单测）、`tsc -b`、`vite build`（含 `internal/share/assets/app.css`
> 同步重建）、`npm run build:share` 重建 `app.js`、播放器续播单测（与 Go 同语义）、
> `share-verify` / `shots-check.mjs` 含延续 fixture。一个里程碑 = 一次 commit + push + 全量验证。

## 0. 侦察（已部分完成）
- [x] 0.1 确认 Clip 时间「一律相对本页起点」（model.go:146）、超页尾被 `DiagClipTruncated` 截断（compile.go:426-472）
- [x] 0.2 确认 `Track.element_id` 已存在（animation-spec:24），可作跨页身份键
- [x] 0.3 确认片段缓存以页为粒度（render-plan:207），路线 B 不破缓存粒度
- [x] 0.4 确认播放器 `rebuildLayers` 换页重建层并施加入场动效（main.ts:285）

## 1. G8-①：IR / 编辑器（animation-spec + animation-track-editing）✅ 已完成
- [x] 1.1 `animation/model.go` 的 `Clip` 增 `Continues bool`（`json:"continues,omitempty"`；omitempty 保既有规格哈希稳定）
- [x] 1.2 `animation-spec/spec.md` MODIFY「三层轨道」与「时间边界与页时长预算」两条 MUST（delta 已写）
- [x] 1.3 `animation-track-editing/spec.md` ADDED「跨页延续可在编辑器表达」（delta 已写）
- [x] 1.4 `animationTrack.ts` 的 `clampClip`/`resizeClipTo`/`moveClipTo` 在 `continues` 时放行溢出；新增 `setClipContinues` 切换标记（`clipGeometry` 自然夹到页边，无需改）
- [x] 1.5 `AnimationTrackTimeline.tsx` 加「跨页延续」开关按钮 + 视觉提示（`.continues` 样式 + 箭头）；i18n 加 `editor.animationClipContinuesOn/Off`；`aria-valuemax` 对延续 clip 放宽
- [x] 1.6 编辑侧 `AnimationClip`（`web/src/animationState.ts`）增 `continues?: boolean`，并纳入 `specFingerprint`/`savePayloadOf`；**注**：编译产物 `shared/types.ts` 的 `Motion` 延续字段（`isContinuation`/`continuationOffsetMs`/`elementId`）留待 G8-②/③
- [x] 1.7 校验放松：`validate.go` V3 时间上界与 V6 页预算对 `continues` 放行（溢出段不计入本页预算，归下一页）
- [x] 1.8 测试：`validate_test.go` 加延续溢出被接受 / 负值起点仍拒；`animation-check.mjs` 加延续夹紧放松与脏判据用例
- [x] 1.9 全量验证：`go build/vet/test ./...` 全绿、`tsc -b`、`npm run build`（含 i18n-check/animation-check/vite）通过

## 2. G8-②：编译拆分（render-plan）✅ 已完成
- [x] 2.1 `render/ir.go` 的 `Motion` 增 `IsContinuation bool`、`ContinuationOffsetMs int64`、`ElementID string`（均 omitempty，非延续计划哈希不变）
- [x] 2.2 `compile.go`：识别 `continues` + 溢出 → 本页段保留**完整** `[from,to]`（`StartMs=S`、`DurationMs=D`、不截断到页尾、`ContinuationSource` 内部标记豁免超页检查）+ 次页段用**负起点平移**（`StartMs=-offset`、`DurationMs=D`、`IsContinuation=true`、`ContinuationOffsetMs=页长-S`）；次页段在 localMs 处进度 `(localMs+offset)/D` 与原 clip 在「页缝+localMs」处一致，cubic-bezier 缓动天然连续（**不再预拆分 Props**，避免二次施加跳变）；次页 `ElementID` 缺失/末页记 `DiagContinuationTargetMissing` 并退化为本页截断；`Track.ElementID` 透传到每段 `Motion.ElementID`
- [x] 2.3 `render-plan/spec.md` MODIFY「时间在编译期解析为页内绝对毫秒」+ ADDED「跨页延续在编译期拆分」要求与场景（骨架阶段已写入）
- [x] 2.4 `compile_test.go` 增 5 个单测：continues 拆分（本页段完整不截断、次页段负起点、offset 正确、两段 Props 完全一致、不变量 `StartMs==-ContinuationOffsetMs`）、非 continues 仍截断且不带 continuation、次页缺失诊断、末页诊断、未溢出不拆分

## 3. G8-③：播放器续播 + 清单 + 渲染器（html-share-bundle + renderer-image）
- [x] 3.1 `share` 清单 `Motion`/SlideLayer 序列化 `elementId` + `isContinuation` + `continuationOffsetMs`（`manifest.go` / `bundle.go` 透传）；`manifest.go` 校验延续段 `continuationOffsetMs` 非负、非延续段不容负起点
- [x] 3.2 播放器 `main.ts` `rebuildLayers`：对 `IsContinuation` 层跳过入场动效；负起点 `StartMs` 已是 `evaluateLayer`/`compileKeyframes` 标准字段，**无需 clipTime 特判**即连续
- [x] 3.3 旧播放器兼容：忽略新字段时次页段退化普通层（负 `StartMs` 仍被 `evaluateLayer` 正常求值，不崩、不露白）
- [x] 3.4 `renderer-image` / Remotion `Timeline.tsx`：负起点平移方案下共用同一 `evaluateLayer`，续播随求值器自动成立（最小改动；未单独实现则降级）
- [x] 3.5 `html-share-bundle/spec.md` ADDED「清单表达跨页延续的层链接」要求与场景（骨架阶段已写入）
- [x] 3.6 `share-baseline` 夹具加一条跨相邻两页延续的元素（`-layers` 路径注入 `cont-N` 源层 + 次页延续段），覆盖编译拆分 + 门禁
- [x] 3.7 播放器续播单测（与 Go 同语义，覆盖缝处连续，`web/animation-check.mjs` + `shared/shots-check.mjs` 增延续 case）
- [x] 3.8 `html-share-quality/spec.md` ADDED「页取样门禁覆盖跨页延续」：新增 fixture，断言页缝视觉连续（骨架阶段已写入；夹具由 3.6 提供）
- [x] 3.9 `npm run build:share` 重建 `app.js`（含续播逻辑；本次播放器源码无需改动，产物无漂移）

## 4. G8-④：收尾归档
- [ ] 4.1 路线图补一行，写明 G8 与 G9 边界（链式多页 / 渲染器续播 / 跨页位置插值均独立）
- [ ] 4.2 `go build/vet/test` + `tsc -b` + `vite build`（同步 `internal/share/assets/app.css`）
- [ ] 4.3 `openspec validate animation-cross-scene-continuation` 通过
- [ ] 4.4 `openspec archive animation-cross-scene-continuation -y` + CI 不受影响确认 + 提交推送
