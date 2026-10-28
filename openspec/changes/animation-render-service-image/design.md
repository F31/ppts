# 设计：渲染服务镜像与光栅后端

## 1. 分层与职责

```
cmd/renderer                     进程壳：HTTP + backend 选择 + 优雅退出
  └── internal/render.RemotionBackend   staging（底板/plan.json）→ exec Node → 收产物
        └── renderer/                 Node 工程：Remotion 合成器，只读 plan.json
              └── chrome-headless-shell（镜像内固定版本）
```

Go 侧仍然只认 `Backend` 接口：

```go
type Backend interface {
    Name() string
    Render(ctx context.Context, plan *Timeline, dir string, progress func(done, total int)) error
}
```

不新增第二个接口。**staging 是 backend 的内部实现细节**：它把「IR + 已就位素材」翻译成「合成器能读的目录」，这属于渲染实现，不属于编排契约。

## 2. 关键决策

### ADR-1 后端选型：L0 用 ffmpeg，L1 用 Remotion

| 层 | 能力 | 实现 | 是否启动渲染服务 |
|---|---|---|---|
| L0 | 页转场（fade/slide/zoom） | worker 内 ffmpeg `xfade` | 否 |
| L1 | 元素入场（fadeIn/slideIn/pop/grow/bob） | Remotion（Chromium 逐帧） | 是 |

理由是**逐条能力核对**，不是「Remotion 更现代」这种偏好。本 IR 的属性白名单只有六项，逐个看 ffmpeg 能不能做：

| IR 属性 | 纯 ffmpeg 可行性 | 依据 |
|---|---|---|
| `opacity` | 可以 | `fade=alpha=1` 对独立输入流按 `st/d` 淡入，再 `overlay` 合成 |
| `translateX/Y` | 可以 | `overlay=x='…t…':y='…t…'` 的表达式每帧求值 |
| `scale` | **不行（干净路径不存在）** | ① `zoompan` 的 `z` 定义域是 `[1,10]`，而 `pop` 的起点是 0.9，做不了缩小；② `scale=eval=frame` 允许逐帧尺寸，但输出尺寸每帧变化会迫使下游 `overlay` 重新配置，文档不承诺逐帧尺寸变化的行为，且帧间尺寸抖动带来边缘采样误差；③ 逐帧用 `geq` 重采样在 1080p 下单帧成本数量级高于渲染一帧 |
| `scaleX/scaleY` | 同上 | 同上 |

除属性之外还有三条更硬的理由：

1. **filtergraph 会退化成不可维护的字符串。** 一页最多 40 clip、每 clip 最多 2 个变换，5 页就是数百个 `overlay` 节点串联。这类图没法 code review，出错只能靠肉眼看片。
2. **与 M6-2 的 DOM 后端没有共享插值。** 本变更的目标之一是「同一份 IR 两个后端同帧一致」。Node 侧与 DOM 侧可以共用同一份 TS 插值函数（`renderer/src/motion.ts` 与未来的 `web/src/sharePlayer` 共用实现或逐行对齐）；ffmpeg 侧没法共享，只能靠参数近似。
3. **封面帧/末帧补齐/时间基对齐** 这些细节 ffmpeg 能做，但每一个都要单独调试；Remotion 已经把「帧号 → 画面」这件事做成了 API。

**排除自研 Go 光栅**：L1 的素材是整页 PNG，Go 自己画就要重新实现文本与矢量合成，等于重写解析器（§8.1 原判断）。**排除裸 Chromium + CDP**：Remotion 就是这个方案加上帧时钟、并发调度、编码管线；重造它只有成本没有收益。

**证据**：`prototype/remotion` 已用同一套栈产出 1080p/30s 样片（不同合成，但运行时是同一条：Chromium + Remotion + H.264）。本变更把「已验证的运行时」接到我们自己的 IR 上。

### ADR-2 镜像两段构建

```
stage base   node:22-bookworm-slim
             + fonts-noto-cjk（OFL）
             + chrome-headless-shell（固定版本，构建期下载）
             + python3-fonttools（pyftsubset，仅构建期用）
stage app    COPY renderer/package*.json && npm ci --omit=dev
             COPY renderer/src → /app/src
             USER 10001
```

为什么拆两段：Chrome 是 CVE 主来源、升级最频繁；放在 base 层后，升 Chrome 只重建 base，`node_modules` 与源码层命中缓存。

为什么不用 `apt install chromium`：发行版版本随基础镜像漂移，**同一个 `planHash` 会在不同时间构建的镜像上出不同像素**，而缓存键里没有它——升级后有人拿到旧像素，且无人能复现。

`chrome-headless-shell` 的来源与固定方式：

- 版本写在 `deploy/renderer/CHROME_VERSION`（单一事实源），构建期校验下载包的 SHA-256；
- 运行期通过 `browserExecutable` 指向 `/opt/chrome/chrome-headless-shell`，**禁止** Remotion 在运行时自动下载（容器无外网，下载失败会表现成「渲染超时」这种误导性错误）。

### ADR-3 Chrome 版本与 renderer 版本进 planHash

`planHash` 已经覆盖 `rendererVersion + fontVersion + fps + 画布`（M2 D-hash）。本变更把 `rendererVersion` 的格式固定为：

```
remotion@4.0.539|chrome@131.0.6778.85
```

规则：

- 服务端 `/healthz` 的 `version` MUST 是这个字符串，`chromeVersion` 字段单独暴露便于告警与漏洞台账；
- 客户端只在 `schemaVersion` 不符时拒绝调用（**正确性**问题）；`version` 差异由 `planHash` 吸收（**性能**问题），不阻断。
- 这条区分很重要：把渲染器版本当正确性校验，会让「滚动升级期间新旧 Pod 并存」直接变成导出失败。

### ADR-4 字体

- 镜像内置 Noto Sans SC（OFL）。**微软雅黑不入镜像**——不可再分发（§8.6）。
- 字体清单由 `/healthz` 的 `fonts[{family,version}]` 暴露，`Client.RequireFonts` 已在 M2 实现：缺字体明确报 `ErrFontUnavailable`，不产出方框字。
- 字体版本进 `planHash`：字体换了，同一份计划的像素就变了。
- 子集化（`pyftsubset`）只在构建期跑，不进主仓库依赖；子集产物按「该镜像内所有文案的字符集」生成不现实（文案随用户变），因此本变更**不做运行时子集**，只做「按常用字表子集」把 9MB 降到 ~1.5MB，其余走 fallback 链。

### ADR-5 运行时安全

| 面 | 措施 |
|---|---|
| 身份 | `USER 10001`，非 root；Chromium 因此不需要 `--no-sandbox` |
| 网络 | `network_mode: none`（compose）／NetworkPolicy 默认拒绝；镜像内无外网 |
| 文件系统 | rootfs `read_only: true`；`/work` 为 tmpfs（`size=2g`），产物只落 `/work` |
| 资源 | `cpus: 2`、`mem_limit: 2g`、`pids_limit: 256`；服务内并发上限 2 |
| 输入 | 沿用 M2：`SafeAssetPath` 拒绝绝对路径与 `..`；请求体 ≤16MB；页数 ≤500 |
| 日志 | 不打印文案正文；只打印 jobId、planHash、页数、耗时 |

### ADR-6 残影：元素抬升前必须补洞（本轮新发现）

**现象。** 底图是整页烘焙的 PNG。把某个元素区域「抬升」成独立图层后，原位置仍然留着这个元素本体。于是：

- `fadeIn`（0→1）：图层从透明到不透明，但它下面永远躺着本体——**动画在视觉上完全无效**；
- `slideIn` / `pop` / `grow`：目标位置的静态副本 + 移动副本同时在画面里——**双影**。

这个缺陷在纸面上看不出来（IR 里每个字段都合法），只有真渲染一遍才会暴露。M2 的 stub backend 写的是文本，所以一直没被发现。

**决策。** 由 backend 在 staging 阶段生成**底板**（plate）：把「参与元素级动画」的 Box 用**盒外 1px 环的逐通道中位色**填充，原图另存为图层素材的来源。

```
base page.png ──┬── crop(box) ──► 图层素材（保留原像素）
                └── fill(box, ring median) ──► plate.png
```

合成时：先铺 plate，再把图层素材按 Box 定位叠加，最后按 clip 施加 `opacity/translate/scale`。

为什么放在 backend 而不进 IR：

- **IR 不该猜视觉。** 背景色是像素的性质，不是规格的性质；把 `backgroundColor` 写进 IR 等于让编译期替底图做决定。
- **补洞是确定性派生。** plate 完全由 `baseImage + boxes` 决定，两者都已在 `planHash` 里，因此 plate 路径不参与哈希。
- **放 backend 才能换算法。** 现在是「中位色填充」，将来换成「背景板渲染」或「inpaint」时，IR 与上游零改动。

为什么不让前端渲染两次（带/不带动画元素）：要多一次页面渲染与上传，且前端在导出时不一定知道「这一版哪些元素最终有动画」——那是规格层的决定。

**已知限制。** 照片底或强渐变底上，中位色补丁会留可见色块。staging 时对「环方差 > 阈值」的 Box 记一条 warning（`plate_patch_noisy`），随产物返回；彻底解决要等页面渲染侧输出「不含动画元素的背景板」，登记为 M6-4 的输入。

**IR 变更。** `Page.plateImage`（可选，相对路径，与 `baseImage` 同一条 `SafeAssetPath` 校验）。它是唯一的契约增量，且**向后兼容**：缺省时合成器直接用 `baseImage` 当底板（等价于旧行为，会双影），因此校验器不要求它存在。

### ADR-7 片段编码参数（为 `-c copy` 拼接固定）

| 项 | 值 | 理由 |
|---|---|---|
| 编码 | H.264 / `yuv420p` | 浏览器与剪辑软件都能播；`yuv420p` 是兼容性最广的像素格式 |
| profile | high | 1080p 通用 |
| CRF | 18 | 视觉无损档；片段会二次拼接，不做激进压缩 |
| GOP | 固定 30 帧（`-g 30 -keyint_min 30 -sc_threshold 0`） | 拼接点必须落在关键帧上；场景切换自适应会让 GOP 不可预测 |
| 音轨 | 无 | 音频由 L5 统一混音（M3） |
| 时长 | 帧数 = `round(durationMs × fps / 1000)` | 半帧以内的误差由 L5 与音频对齐时吸收（与 MP4Encoder 现有做法一致） |
| 容器 | `+faststart` | 分享/预览场景需要可边下边播 |

### ADR-8 部署形态与降级

```
animation.backend = remotion | none
```

- `none`：worker 不构造渲染计划，直接走 L0（ffmpeg `xfade`）或静态导出；
- `remotion`：worker 启动时探测 `/healthz`，失败则整进程按 `ErrAnimationUnavailable` 处理；
- 服务不可用 / 单页失败：沿用 M2 D7 的降级矩阵——**动画是增强，不是前置条件**。

### ADR-9 嵌套元素：大图层的裁切会带出内部元素（本轮新发现，未修）

ADR-6 的补洞只解决「元素自己的静态副本」。当**被动画的元素 A 的包围盒包含另一个被动画的元素 B** 时，
A 的裁切图是从 `baseImage` 上按盒取的区域，里面本来就烘着 B 的像素；底板把 B 抹掉了，
但 A 的裁切图又把 B 带了回来。表现是 B 完全跟随 A 的不透明度，自己声明的 `startMs` 看起来失效。

实测（2026-10-28，`tmp_render_demo` 第 0 页：卡片 `1728×560` 内含 3 条 bullet）：

| 证据 | 数值 |
|---|---|
| `baseImage` 在 bullet 处 | `(203,213,225)` |
| `plate` 在 bullet 处 | `(255,255,255)`（已被抹掉） |
| 卡片 clip：`startMs=260, durationMs=600` | t=400ms 时 `opacity≈0.737` |
| t=400ms 实测 bullet 像素 | `(217,224,233)` = `255 − 0.737×(255−203)`，误差 0 |
| 把三条 bullet 的 `startMs` 手改成 3000 后重出同帧 | **像素零差异**（2073600 px 全等） |

即：画面里的 bullet 根本不是从 bullet 图层画出来的，而是卡片裁切图的一部分。

**修法（待评审，需要动 IR）**：为每个元素层产出「只含自己」的位图——从 `baseImage` 出发，
把**其它**元素盒用「在原图上按盒外环采样」的颜色填平，再按本层盒裁切；IR 增加可选字段
`layer.image`，合成器见到它就直出该图、不再做偏移裁切。

- 为什么不能复用 `plateImage`：底板把所有元素都抹了，包括本层自己，图层会变空。
- 为什么填色要基于原图而不是底板：底板的填色是按图层顺序递进算的（卡片先被填成白），
  拿它去填 bullet 会得到白条，而正确值是卡片色 `(241,245,249)`。
- 成本：`BuildPlate` 增加一次「按原图取色」的预计算 + 每层一张 PNG（5 页样例约 +1.3MB 临时文件），
  渲染时间基本不变（少了一次 CSS 偏移裁切）。
- 若暂不修：`assetMode` 应拒绝「动画元素包含动画元素」的嵌套并出诊断，
  避免用户看到「错落编排不生效」却没有任何提示。

### ADR-10 `clip.startMs` 与触发锚点的关系（本轮修正）

修正前 `compileClip` 只取 `resolveTrigger` 的返回值当起点，`clip.startMs` 被静默丢弃：
`onEnter` 一律 0ms，编排里的 700/920/1140ms 错落全部塌缩成同时出现，且没有任何诊断。

正确语义是**锚点 + 偏移**：`start = resolveTrigger(trigger) + clip.startMs`（设计方案附录 B 的
`startMs: 1230` 即 `withSegment` 锚点换算后的绝对值，偏移为 0）。修正后同一份 revision 的 IR 为
`120 / 260 / 700 / 920 / 1140`，与编排一致；新增 `TestOnEnterClipKeepsRelativeOffset`、
`TestSegmentTriggerAddsClipOffset` 两个用例。`TestClipTruncatedAtPageEnd` 原本把 800ms 同时写进
`atTime:800` 与 `startMs:800`，靠「startMs 被忽略」才通过，已改为只表达页尾截断。

## 3. 目录与文件

| 路径 | 内容 |
|---|---|
| `renderer/src/index.ts` | `registerRoot`，`Composition` + `calculateMetadata`（时长/画布来自 props） |
| `renderer/src/Timeline.tsx` | 页序 / 转场 / 图层合成 |
| `renderer/src/motion.ts` | `cubic-bezier` 求解与属性插值（与 DOM 端同源语义） |
| `renderer/src/types.ts` | IR 的 TS 镜像（字段与 Go 结构一一对应） |
| `renderer/render.mjs` | 程序化渲染入口：bundle → selectComposition → renderMedia，stdout 输出 JSON 行 |
| `internal/render/plate.go` | 底板生成：中位色补洞 + 噪声判定 |
| `internal/render/remotion.go` | `RemotionBackend`：staging → exec → 产物回收 |
| `deploy/renderer/Dockerfile` | 两段构建 |
| `deploy/renderer/CHROME_VERSION` | Chrome 版本单一事实源 |
| `deploy/renderer/compose.yml` | 本地/单机部署样例（资源限额、只读 rootfs、tmpfs） |

## 4. 风险

| 风险 | 缓解 |
|---|---|
| Chrome 升级导致像素漂移 | 版本进 `planHash`；`CHROME_VERSION` 单点管理；升级需重跑样片核对 |
| 镜像体积 ~1.5GB | 独立镜像，与 API 解耦；主 API 镜像体积零增长（M1 的硬指标） |
| 中位色补丁在照片底露馅 | `plate_patch_noisy` warning + 样片人工核对；根治靠页面渲染侧背景板（M6-4） |
| Node 与 Go 的 IR 字段漂移 | `renderer/src/types.ts` 与 Go 结构逐字段对齐；`plan.golden.json` 作为两端共用夹具，Node 端加载它做类型断言 |
| 容器内 Chromium 崩溃/OOM | 服务内并发上限 2；单页 60s 超时；崩溃映射为可重试（M2 D7） |
| 无本机容器环境，镜像无法在本机构建 | 镜像构建放进 CI job（`docker build` + `--selftest`）；本轮只验证「合成器能在本机 Chrome 上出片」，镜像层留待 CI 首跑 |

## 5. 验收

1. `go test ./internal/render/...`：底板补洞（纯色底无色差、噪声底出 warning、不改源图、按比例换算）、backend staging 产物结构（底板落盘、计划副本回填、原计划不被改）、脚本/Node 缺失时给出可操作错误、路径逃逸拒绝、MP4 容器校验。
2. `cmd/renderer -selftest -backend remotion`：2 页固定样例，走完整 staging → 渲染 → 校验链路，失败非零退出。本机实测 16.7s 通过。
3. 5 页 / 30s 样片：本机 Chrome 真实出片，逐页 6.000s，总 900 帧。
4. 残影对照：同一帧「有/无底板」两张静帧，用像素包围盒与颜色采样确认差异。

## 6. 验证记录（2026-10-28，本机 Windows + Chrome）

| 项 | 结果 |
|---|---|
| 5 页样片渲染 | 900 帧 / 1m58s（≈7.6 fps），5 个片段各 27–44KB |
| 片段容器 | 全部 `ftypisom`，1920×1080，时长 6.000s（Chrome `loadedmetadata` 读出） |
| `-selftest`（remotion） | 通过，2 页 16.671s |
| 底板补洞 | 纯色底 spred=0、补丁色=底色；渐变底出 `plate patch may be visible` warning |
| 残影对照（t=67ms） | 无底板：标题条像素 (30,58,138) 全不透明（动画失效）；有底板：白色底 + 约 25% 不透明度叠加 |

**30s 样片逐帧核对（用 Remotion 自带 ffprobe/ffmpeg，宽 960 缩略后按帧索引比对）：**

| 检查项 | 结果 |
|---|---|
| 规格 | 5 × 6.000s = 30s，各 180 帧，1920×1080 @ 30fps，共 900 帧（与 `round(ms×fps/1000)` 一致） |
| t=0 画面 | 纯白：底板已抹平全部元素，无「目标位静态副本」残影（ADR-6 生效） |
| 收敛曲线 | 与终态的平均像素差 24.73 → 12.52(100ms) → 5.26(200ms) → 0.52(433ms) → 0.08(667ms) → 0（1000ms） |
| 编排忠实度 | **不通过**。三条 bullet 声明 700/920/1140ms，实测全部在 ~400ms 随卡片出现——根因是 ADR-9 的嵌套裁切，另有 ADR-10 的 `startMs` 丢失去加重了现象 |
| 文字保真 | 未覆盖。样例底图是纯色块占位，没有文字；文字由上游「页面栅格化」决定，不在本后端职责内 |

两条结论要连着读：ADR-10 已修（IR 现在正确携带偏移），但**只要 ADR-9 未修，同一页里的错落编排仍会看起来失效**，
因此 30s 样片目前只能证明「链路能出像素、容器/时长/帧数正确」，不能证明「编排忠实」。

**这两条未完成，且不是本机可解决的：**

1. **镜像从未构建**。本机无 Docker；Chrome for Testing 的 zip 在本机代理下下载失败（curl 56），
   因此 `chrome.sha256` 没有生成，`Dockerfile` 的摘要校验分支没有被执行过。
2. **干净环境 `GOWORK=off` 构建会在 `textnorm@v0.2.2` 处失败**：`go mod download` 报
   `checksum mismatch`（下载到 h1:IZDO…，`go.sum` 记的是 h1:d3rO…）。本机构建成功是因为模块已在
   本地缓存。这条不修，镜像与 CI 的 `-race` 都过不去；修它要动 `go.sum`，属于模块所有者的决定。
