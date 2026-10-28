## 1. 选型与契约

- [x] 1.1 逐条核对 ffmpeg 对 IR 六种属性（opacity / translateX / translateY / scale / scaleX / scaleY）的可行性，结论与依据写入 design ADR-1
- [x] 1.2 定分层：L0 页转场用 ffmpeg `xfade`、不启动服务；L1 元素入场走 Remotion
- [x] 1.3 `Page.plateImage` 可选字段：路径校验、排除出 `planHash`、IR 校验覆盖
- [x] 1.4 `/healthz` 增加 `chromeVersion`（后端可选接口上报，服务层不感知 Chromium）

## 2. 合成器（`renderer/`）

- [x] 2.1 `src/types.ts`：`CompiledTimeline` 的 TS 镜像，字段与 `internal/render/ir.go` 一一对应
- [x] 2.2 `src/motion.ts`：cubic-bezier 求解（Newton + 二分兜底）与属性叠加（opacity 乘、位移加、缩放乘）
- [x] 2.3 `src/Timeline.tsx`：页序、页间转场（fade/slide/zoom）、图层裁剪与变换
- [x] 2.4 `src/index.tsx`：`Composition` + `calculateMetadata`（时长/画布来自 props）
- [x] 2.5 `render.mjs`：逐页 `frameRange` 渲染，stdout 输出 JSON 行进度的，固定 CRF/GOP/像素格式
- [x] 2.6 `still.mjs`：单帧导出，用于排查残影/转场过冲
- [x] 2.7 `tsc --noEmit` 通过、`npm ci` 依赖锁定（`package-lock.json` 入库）
- [ ] 2.8 Studio 人工预览（`npm run studio`）：本机未跑，分辨率/颜色需人眼过一遍
- [ ] 2.9 与 DOM 端（M6-2）抽出共享插值实现，避免两份 cubic-bezier 各自演化

## 3. Go backend

- [x] 3.1 `BuildPlate`：盒外环逐通道中位色补洞 + 极差判定（纯色底 0 色差、渐变底出 warning）
- [x] 3.2 `RemotionBackend.Render`：staging（底板 + `plan.json`）→ exec Node → 逐片段容器校验
- [x] 3.3 `workPath` 二次拦截绝对路径与 `..`（IR 校验之外的最后一道关口）
- [x] 3.4 `cmd/renderer` 支持 `-backend remotion`、`-renderer-script`、`-chrome`、`-render-timeout`
- [x] 3.5 `-selftest`：2 页固定样例走完整链路，失败非零退出；stub backend 明确拒绝
- [x] 3.6 单测：补洞语义、不修改调用方计划、噪声判定、脚本缺失/Node 缺失的错误信息、路径逃逸、MP4 容器校验
- [ ] 3.7 单页失败只降级该页（当前整次渲染失败）：需 M3 的降级矩阵接线后才有意义
- [x] 3.8 `clip.startMs` 与触发锚点相加（ADR-10）：此前 `startMs` 被静默丢弃，错落编排全部塌缩到 0ms
- [x] 3.9 嵌套元素按层出图（ADR-9）：`BuildLayerAssets` 只抹「被本层包住的子元素」，填色取自原图；IR 增加可选 `layer.image`，合成器直出
- [x] 3.10 失败/取消不再毒化 `planHash`（ADR-11）：此前终态非成功仍占着 `byHash`，同一份计划失败一次就永远渲染不出来；取消立刻释放，重试分配新 jobId + 新目录
- [x] 3.11 `DELETE /v1/render/{id}` 回实际状态：对已成功任务点取消，此前硬编码回 `canceled`，与实际不符
- [x] 3.12 排队上限（ADR-12）：`MaxQueued`（默认 8）只约束 `queued`；超限返回 429 + `Retry-After`（码 `queue_full`），在读 body 之前先判、锁内入队前再判；被拒提交不建 job、不消耗重试编号
- [x] 3.13 终态保留（ADR-12）：`JobTTL`（默认 30 分钟，从落终态起算）+ 请求路径上的惰性回收；过期删记录、释放 `byHash`、删产物目录；`attempts` 用 live 计数，归零即删
- [ ] 3.14 重启后 `/work` 下的孤儿目录无人回收（`jobs` 只在内存）：需持久化索引或启动扫描，先评审是否接受有状态

## 4. 镜像

- [x] 4.1 `deploy/renderer/Dockerfile`：Go 构建段 + base（Node/Chrome/字体）+ app 段，非 root
- [x] 4.2 `CHROME_VERSION` 单一事实源 + `pin-chrome.sh`（下载并写 `chrome.sha256`）+ 构建期摘要校验
- [x] 4.3 `compose.yml`：只读 rootfs、tmpfs `/work`、`network_mode: none`、CPU/内存/PID 限额、selftest 服务
- [x] 4.4 `.dockerignore`：排除 `node_modules` 与 `web/`，避免上下文膨胀
- [ ] 4.5 生成并提交 `chrome.sha256`（本机代理下载失败，curl 56）
- [ ] 4.6 镜像首次 `docker build` + `--selftest`（本机无容器环境）
- [ ] 4.7 解决 `textnorm@v0.2.2` 校验和不一致对 `GOWORK=off` 构建的阻塞（见 README「已知阻塞」）
- [ ] 4.8 字体子集化（`pyftsubset` 常用字表）：当前镜像装全量 Noto CJK，约 9MB

## 5. 验收

- [x] 5.1 5 页 / 30s 样片在本机 Chrome 上真实出片（5 × 6s，共 900 帧；首轮 1m58s，修 ADR-10 后重渲 1m29s）
- [x] 5.2 片段核对：5 个 `segment_*.mp4` 均为 1920×1080 / 6.000s / 180 帧 / `ftypisom` 容器
- [x] 5.3 残影验证：同一帧「有底板 / 无底板」对照，无底板时标题条在 t=67ms 已完全不透明（动画视觉失效）
- [ ] 5.4 容器内跑同一份 30s 样片（依赖 4.6）
- [ ] 5.5 5 页样片观感盲评（对照参考片，按视觉基线 §19 的评分口径）
- [ ] 5.6 30 页 / 5 分钟长片压测（并发、内存峰值、OOM 边界）
- [x] 5.7 逐帧核对 t=0 无静态副本残影（纯白底板），收敛曲线 24.73 → 0（1000ms 内）
- [x] 5.8 编排忠实度：修 ADR-9 后三条 bullet 依次在 ≈800/1000/1150ms 出现（声明值 700/920/1140），卡片区域为卡片底色而非白条
- [ ] 5.9 部分重叠（互不包含）的兄弟元素仍会互相带出：单张栅格推不出逐元素 alpha，属模型限制

## 6. 文档

- [x] 6.1 `design.md` 决策记录（ADR-1 选型核对表、ADR-6 残影、ADR-7 编码参数、ADR-9 嵌套裁切、ADR-10 起点语义）
- [x] 6.2 `deploy/renderer/README.md`：构建、自检、版本格式、已知阻塞
- [x] 6.3 路线图更新（M2 从「阻塞于容器」改为「本机已出片，剩镜像构建」）
- [ ] 6.4 方案文档回写（`docs/` 已移出仓库，改在变更归档时并入 `openspec/specs/`）

## 落地状态（2026-10-28）

**已完成：选型、合成器、Go backend、镜像定义、样片出片与逐帧核对（本机）。**

本轮最重要的产出不是 Dockerfile，而是**在真渲染时发现并修掉的三个缺陷**：

1. **ADR-6 残影**（已修）：底图已烘焙元素，抬升成图层后原位置仍留着本体 → `fadeIn` 视觉完全无效，
   `slideIn`/`pop` 出现双影。证据：同一帧（t=67ms），无底板时标题条已 100% 不透明；有底板时约 25%。
   修法：staging 用「盒外 1px 环的中位色」填平元素区域，产物作为 `page.plateImage`（可选字段，不入 `planHash`）。
2. **ADR-10 `startMs` 丢失**（已修）：`compileClip` 只取触发锚点，`clip.startMs` 被静默丢弃，
   编排的 700/920/1140ms 全部塌缩到 0ms。修法：`start = 锚点 + startMs`，补 2 个用例。
3. **ADR-9 嵌套元素被大图层带出**（已修）：卡片层裁切图里烘着三条件 bullet，bullet 完全跟随卡片的
   不透明度。证伪：把 bullet 的 `startMs` 改成 3000 重出同帧，2073600 像素零差异。
   修法：`BuildLayerAssets` 为每层产出「抹掉被自己包住的子元素」的位图，填色取自原图，
   IR 增加可选 `layer.image`，合成器直出。修后起播点 ≈800/1000/1150ms，与声明值一致。

**已知未修（ADR-9 边界）：部分重叠的兄弟元素。** 「包含」关系已经处理（`BuildLayerAssets` 只抹被本层包住的
子元素，填色取自原图），但两个部分重叠、互不包含的兄弟元素仍会互相带出。单张栅格推不出逐元素 alpha，
要彻底解决需上游提供分层素材；这条是模型限制，不是实现遗漏（5.9）。

**未完成：镜像构建与容器内验收（4.5–4.8、5.4–5.6）。** 本机无 Docker，且 Chrome 包下载被代理阻断，
`textnorm` 的 `go.sum` 校验和问题会让干净环境构建失败。这两条都登记在
`deploy/renderer/README.md`，不在本变更里擅自绕过。
