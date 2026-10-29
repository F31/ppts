# 提案：动画渲染 MVP（CompiledTimeline IR + 编译器 + 渲染服务契约）

## 为什么做

M1 已经把「规格是否合法」锁死在服务端（V1–V12），视觉基线把「拿什么评观感」写成了可校验的代码。但两者之间还缺一环：**没有任何东西把一份合法规格翻译成渲染器真正能执行的东西**。

设计方案 §8.3 给了一个 `RenderPlan` 的骨架，里面仍然保留 `"type": "slideIn"`。这意味着 Remotion 与 DOM 两个后端各自实现一遍「slideIn 是什么曲线、位移多少、从哪开始」。两份实现一定会漂移——这不是理论风险，是 §16 风险表里已经登记的那一条（「双目标动效漂移」→ SSIM < 0.95）。**只要后端认得动效名，两个后端就不可能逐帧相同。**

本变更把动效名从 IR 里彻底移除：编译器把 catalog 里的语义展开成 `属性 → from/to + cubic-bezier`，后端只做插值。两个后端插同一组数，一致性是构造出来的，不是测出来的。

## 目标

1. `compiled-timeline/v1`：目标无关的 IR，只含数值（像素盒、毫秒、属性关键帧、缓动四点），不含业务语义、不含动效名（`effect` 只作审计留痕，规范禁止后端读取）。
2. 编译器：`Animation.Revision + 页几何 + 时间轴 + 预设 → IR`。EMU→px、`withSegment`→绝对毫秒、强度→幅度、轨道→图层，全部在这一步完成。
3. 渲染服务契约：`POST /v1/render`、`GET /v1/render/{jobId}`、`DELETE`、`/healthz`，幂等键 `planHash`，无状态，产物只写工作目录。Go 侧提供可插拔 backend SPI，本变更只带一个确定性 stub backend（供契约测试），真实光栅后端（Remotion/Chromium）随镜像另行落地。
4. HTTP 客户端：能力探测（字体/版本）、错误分类（4xx 永久失败 / 5xx 与超时可重试）、退避重试、`ErrAnimationUnavailable` 降级信号。

## 非目标

- 不接线 `internal/app/export.go`（属 M3，PR4）。本变更不改变任何既有导出行为。
- 不做片段拼接、不加音频、不做 ffmpeg `xfade`（属 L5）。
- 不做真实光栅化（Chromium/Remotion 镜像），也不承诺「30s 样片」验收——那需要容器环境，见 tasks.md 的风险项。
- 不新增 proto/Connect 服务：渲染服务是对内 HTTP，契约以 Go 类型 + JSON schema 固化。

## 与既有变更的关系

- 依赖 `animation-spec-core`（M1）的 `Revision/Track/Clip` 与 catalog。
- 依赖 `animation-visual-baseline` 的 `design.Theme`（画布）与运动质感档案（速度分层/节拍），但**不依赖其预览页**。
- 解除 `html-share-bundle-static` 的前置等待：M6-2 的 DOM 后端从本变更的 IR 取数，不再自己解释规格。

## 平台决策

`intensity` 只缩放**幅度**，不缩放时长。规格里的 `durationMs` 是用户能看见、能编辑、已被 V5 校验的值；编译期再按强度改它，等于让预览与规格各说一套，还可能把 clip 推出页尾。强度改变的是「动得多夸张」，不是「动多久」。
