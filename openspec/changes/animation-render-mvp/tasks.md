# 任务：动画渲染 MVP

## 1. 动效属性展开（PR1）

- [x] 1.1 `internal/animation/plan.go`：`PlanMotion(effectID string, p Params, easing string, intensity float64) (MotionPlan, error)`，覆盖 catalog 全部动效
  - `fadeIn` → opacity 0→1
  - `slideIn` → opacity 0→1 + `translate{direction}` offset→0
  - `pop` → opacity 0→1 + scale `1-(1-scaleFrom)`→1
  - `grow` → scale（axis=x 用 `translateX` 充零宽？否：grow 只用 scale + opacity）
  - `highlight` → opacity 0.45→1（不引入新属性）
  - `bob` → `translateY` 0→`-offset`（循环语义由 `Loop` 标记，IR 只给单程）
  - `fadeOut` → opacity 1→0
- [x] 1.2 缓动名 → cubic-bezier 四点表：`linear/easeOut/easeInOut/spring`；`spring` 用 1.42 控制点（与视觉基线 §6 一致，过冲 ≤8%）
- [x] 1.3 测试：catalog 中每个 effect MUST 有展开（缺一个即失败）；未知 effect 报错；`scaleFrom` 强度语义单测（`intensity=0` 时 scale 仍为 1）

## 2. IR 与校验（PR1）

- [x] 2.1 `internal/render/ir.go`：`Timeline/Page/Layer/Motion/Prop/Easing/Transition/Box/Font/Diagnostic`，JSON tag 全显式
- [x] 2.2 `internal/render/validate.go`：design.md §4 的 12 条不变量，错误带字段路径
- [x] 2.3 `internal/render/hash.go`：规范化副本 + SHA-256，覆盖 schema/画布/fps/字体/页时长/运动数值；排除 `warnings` 与路径前缀
- [x] 2.4 测试：hash 稳定性（两次相同）、1px 变化改变 hash、路径前缀不影响 hash

## 3. 编译器（PR1）

- [x] 3.1 `Geometry`：`Canvas{Width,Height}` + `SlideWidthEMU/SlideHeightEMU`；缺省按高度 6858000 推断并记 `geometry_inferred`
- [x] 3.2 `CompilePage(in PageInput) (Page, []Diagnostic, error)`：EMU→px、轨道→图层、触发解析、强度缩放、转场
- [x] 3.3 诊断：`anchor_missing`、`box_clamped`、`segment_unresolved`、`duration_tier_conflict`、`geometry_inferred`、`clip_truncated`
- [x] 3.4 测试：EMU 换算（16:9 与 4:3）、`withSegment` 绝对毫秒、越界钳制、缺失锚点跳过、强度只改幅度、转场首屏为 none
- [x] 3.5 golden：一份 3 页样例 spec 的 IR 全文入库（`testdata/`），字节级比对，防止后续无意改动语义

## 4. 渲染服务（PR2）

- [x] 4.1 `internal/render/service.go`：`Service` + `Backend` SPI（`Render(ctx, plan, dir, progress)`）
- [x] 4.2 `POST /v1/render`（`planHash` 幂等、400/413/422 映射、body 上限 16MB）
- [x] 4.3 `GET /v1/render/{id}`（state/progress/artifacts/err）、`DELETE`（尽力取消）、`GET /healthz`（版本/backend/字体/并发余量）
- [x] 4.4 `cmd/renderer`：监听地址、工作目录、并发上限、优雅退出
- [x] 4.5 `stub` backend：按页时长写占位片段 + 进度回调，供契约测试
- [x] 4.6 路径安全：`baseImage` 拒绝绝对路径、盘符、`..`；workDir 隔离
- [x] 4.7 测试：幂等（backend 只调一次）、400 不建 job、取消、healthz 字段、路径逃逸

## 5. 客户端（PR2）

- [x] 5.1 `internal/render/client.go`：`Submit/Get/Cancel/Health`，`ErrAnimationUnavailable` 降级信号
- [x] 5.2 重试：5xx/408/429/超时 → 2 次指数退避；4xx → 永久失败
- [x] 5.3 能力探测：`sync.Once` 缓存 healthz，字体缺失 → `ErrFontUnavailable`
- [x] 5.4 测试（httptest）：成功、400 不重试、503 重试两次、连接失败 → `ErrAnimationUnavailable`、超时

## 6. 验收门禁

- [x] 6.1 `go build ./...`、`go vet ./...`、`go test ./internal/render/... ./internal/animation/...` 全绿
- [x] 6.2 `go list -f '{{.Imports}}' ./internal/render` 的直接导入只有标准库 + `internal/animation` + `internal/design`（不含 app/api/media/project；传递链见 6.8）
- [x] 6.3 `go list -f '{{.Imports}}' ./cmd/renderer` 的直接导入只有标准库 + `internal/render`
- [x] 6.4 `internal/app/export.go`、`internal/media/`、`web/` 零改动（`git diff --stat` 断言）
- [x] 6.5 `go.mod` 无新增依赖
- [x] 6.7 回写设计方案偏差（design.md §6）并在 roadmap 更新 M2 状态
- [ ] 6.6 真实样片（`智讲PPT-客户方案讲解.pptx`，30s）——**阻塞于容器环境**：本机无 ffmpeg、无 Remotion 镜像，stub backend 不产出真实视频。不计入本变更完成条件，转入 `animation-render-service-image` 变更
- [x] 6.8 拆分模型/持久化：`animstore`（SQLite/PostgreSQL）与 `animdefault`（依赖 project 的默认规格生成）移出模型包，解除渲染器对 `internal/db`+`pgx`+`sqlite` 的传递依赖。实测二进制 17.10MB → 10.19MB，`cmd/renderer/deps_test.go` 断言传递闭包不含存储栈
