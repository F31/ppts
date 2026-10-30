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

### ADR-1 后端选型：转场与元素动画同一次合成

| 层 | 能力 | 实现 | 是否启动渲染服务 |
|---|---|---|---|
| L0 | 页转场（fade/slide/zoom） | 合成器在**入场页开头**绘制前一页末帧（`renderer/src/Timeline.tsx`） | 与 L1 同批 |
| L1 | 元素入场（fadeIn/slideIn/pop/grow/bob） | Remotion（Chromium 逐帧） | 是 |

> **修订（2026-10-31）**：本表原写「L0 由 worker 内 ffmpeg `xfade` 完成，不启动渲染服务」，
> 与实现和已归档规格都不符——全仓没有任何 `xfade` 调用，`openspec/specs/render-plan/spec.md`
> 明写「转场在该页开头绘制前一页的末帧」，片段缓存也据此把前一页纳入键（`segkey.go`）。
> 走 xfade 会**改变时长语义**（xfade 是重叠，会让成片变短；本设计的转场只占入场页开头 N ms、
> 不改变任何一页时长），两套时序并存必然对不齐。因此转场与元素动画共用一次合成。
> 代价见 tasks 7.1：动画通道关闭时（`backend=none`）转场一并失效。
>
> 这处错能留这么久，有个次要原因：路线图里「L0」另指**PPTX→页图**的上游阶段
> （「渲染器只吃底图 + 元素矩形」），与 ADR-1 这张表的分层编号撞名。读到「L0」时
> 默认想成了上游，就没再追问「worker 里到底哪段代码在做 xfade」——而答案是零处。

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
- 当前固定值（2026-10-28 生成并提交）：`156.0.8078.73` / `linux64` / `chrome-headless-shell`，
  126,938,225 字节，`sha256=77eb8d52798efc7a857ab61abcc62a456d8ea9635b02341d06dffee2761768c8`。
  该摘要经三重核对：与 `Content-Length` 一致、与 GCS 返回的 `x-goog-hash: md5=` 一致、zip 内容为
  287 个条目且含 `chrome-headless-shell-linux64/chrome-headless-shell`（不是错误页被存成 zip）。
- `chrome.sha256` 是 `sha256sum -c` 的输入，**必须是 LF**：`.gitattributes` 里固定
  `deploy/renderer/chrome.sha256 text eol=lf`。`core.autocrlf=true` 的 Windows 检出会把
  `\r` 带进校验行，而 Linux 侧的 coreutils 不会像 MSYS 那样吞掉它。

`pin-chrome.sh` 有一个 Git Bash 专属坑（已修，2026-10-28）：`sha256sum "$path"` 在文件名含反斜杠时
会给摘要加前导 `\` 作为转义标记（Git Bash 的 `mktemp -d` 返回 `C:\...\Temp/tmp.x`，正好触发），
写出来就是 `\<hex>`，构建期 `sha256sum -c` 报格式错误。改为 `sha256sum < "$path"` 从 stdin 读
（文件名是 `-`，不触发转义），并在写文件前断言 `^[0-9a-f]{64}$`——这个文件是校验值，
格式错了必须在生成时炸，而不是等构建机拉完 121MB 才失败。

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

- 镜像内置 Noto Sans CJK SC（OFL）。**微软雅黑不入镜像**——不可再分发（§8.6）。
- 字体清单由 `/healthz` 的 `fonts[{family,version}]` 暴露，`Client.RequireFonts` 已在 M2 实现：缺字体明确报 `ErrFontUnavailable`，不产出方框字。
- 字体版本进 `planHash`：字体换了，同一份计划的像素就变了。
- 子集化（`pyftsubset`）只在构建期跑，不进主仓库依赖；子集产物按「该镜像内所有文案的字符集」生成不现实（文案随用户变），因此本变更**不做运行时子集**，只做「按常用字表子集」，其余走 fallback 链。

> **2026-10-28 修正（见 ADR-15）**：本条写的「9MB 降到 ~1.5MB」两个数字都不对——实测全量是
> **91MB**（4 个 TTC，含 Serif 与五个地区字形），子集后是 **21.9MB**（两个 OTF）。
> 「按常用字表」也过于含糊：具体边界落在 `deploy/renderer/charset.txt`，分 `must` / best-effort
> 两档，并由构建期断言保证 `must` 全覆盖。字体版本改为由字集摘要推导，不再手写。

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

- `none`：worker 不构造渲染计划，直接走静态导出——**转场也随之失效**（`backend=none`
  时不发起任何渲染请求，见 tasks 7.1）。**2026-10-31 决策：接受这个代价**（①），
  代价改为一句用户可见的说明：导出对话框在 `renderer_not_configured` 时明确写出
  「静态版不含页间转场，页与页之间为硬切」，`web/animation-check.mjs` 断言这句话还在。
  不选②（只含转场也起 Chromium，与 ADR-1 的成本初衷相反）与③（xfade 会缩短每页时长）；
- `remotion`：worker 启动时探测 `/healthz`，失败则整进程按 `ErrAnimationUnavailable` 处理；
- 服务不可用 / 单页失败：沿用 M2 D7 的降级矩阵——**动画是增强，不是前置条件**。

### ADR-9 嵌套元素：大图层的裁切会带出内部元素（本轮修复）

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

**修法（已落地）**：`BuildLayerAssets` 为每个元素层产出「只含自己」的位图，IR 增加可选字段
`layer.image`，合成器见到它就直出该图、不再做偏移裁切（缺省仍走旧路径，向后兼容）。

三条规则不是随手定的，都是被实测逼出来的：

1. **只抹被本层包住的子元素**（`other.rect.In(self.rect)` 且尺寸不同）。
   第一版抹了「所有相交元素」，结果子层素材里的父层填色把自己的画面整块盖掉——子元素直接消失。
   反过来若父层不抹子元素，就回到原 bug。同尺寸的两个元素互不抹，否则两边都被抹空。
2. **填色 MUST 基于原图，不能用底板**。底板的填色是按图层顺序递进算的（卡片先被填成白），
   拿它去填 bullet 会得到白条；正确值是卡片色 `(241,245,249)`。修复后实测 t=500ms 组件区域为
   `(242,246,249)`，确认取到的是卡片色。
3. **图层必须父在前、子在后**。父层的子元素已被抹掉，父层若画在子层之后会把子元素整块盖掉。
   当前编译产物按 `tracks` 顺序出层，正好满足；这条约束写进了 `BuildLayerAssets` 的文档注释。

**未覆盖**：两个部分重叠、互不包含的兄弟元素仍会互相带出（无法从单张栅格推断逐元素 alpha）。
这属于模型限制，不是实现遗漏；要彻底解决需要上游提供分层素材。

**修复后实测**（同一份样片，逐帧采样三条 bullet 的中心像素）：

| t | b0 | b1 | b2 |
|---|---|---|---|
| 700ms | `242,246,249`（卡片色，尚未出现） | 同左 | 同左 |
| 900ms | `206,216,226`（已接近终值） | `242,246,249` | `242,246,249` |
| 1200ms | `204,214,226` | `204,214,226` | `222,231,238`（刚开始） |
| 1600ms | `204,214,226` | `204,214,226` | `204,214,226` |

起播点 ≈800 / 1000 / 1150ms，与编排的 700 / 920 / 1140ms 在 30fps 采样精度内一致（easeOut 前段变化快，
700ms 那一帧还没跨过采样点）。

### ADR-10 `clip.startMs` 与触发锚点的关系（本轮修正）

修正前 `compileClip` 只取 `resolveTrigger` 的返回值当起点，`clip.startMs` 被静默丢弃：
`onEnter` 一律 0ms，编排里的 700/920/1140ms 错落全部塌缩成同时出现，且没有任何诊断。

正确语义是**锚点 + 偏移**：`start = resolveTrigger(trigger) + clip.startMs`（设计方案附录 B 的
`startMs: 1230` 即 `withSegment` 锚点换算后的绝对值，偏移为 0）。修正后同一份 revision 的 IR 为
`120 / 260 / 700 / 920 / 1140`，与编排一致；新增 `TestOnEnterClipKeepsRelativeOffset`、
`TestSegmentTriggerAddsClipOffset` 两个用例。`TestClipTruncatedAtPageEnd` 原本把 800ms 同时写进
`atTime:800` 与 `startMs:800`，靠「startMs 被忽略」才通过，已改为只表达页尾截断。

### ADR-11 任务生命周期：终态非成功必须释放 `planHash`（本轮修正）

并发审计前，`byHash`（`planHash → jobId`）一旦写入就永不删除。后果是**同一份计划只要失败或取消过一次，
就再也不会被渲染**：重试命中旧 job，直接拿到 `failed` / `canceled`。

这不是理论问题。真实渲染会因 Chrome 崩溃、超时、OOM 失败，用户最常见的动作就是重试；取消则常是误触。
一个瞬时故障足以让某份讲稿在该进程生命周期内永久不可渲染，而服务对此没有任何提示。

修法有三条互相咬合的决定：

1. **只有成功才配享受幂等复用。** `finish` 在 `failed` / `canceled` 时删除 `byHash[hash]`
   （带 `byHash[hash] == jobID` 校验，避免误删新任务的映射）。
2. **取消立刻释放。** `Cancel` / `DELETE` 在置 `canceled` 的同时释放，而不是等 goroutine 退到 `finish`——
   否则客户端刚看到 canceled 就重试，仍会命中这个尚未收尾的 job。
3. **重试分配新 id + 新目录。** 只释放映射还不够：被取消的 goroutine 可能正在 `RemoveAll` 自己的目录，
   若重试沿用 `ShortID(hash)` 作目录名，就会和这一轮抢同一个路径（旧轮删掉新轮刚写的片段）。
   因此 `allocID` 在第二次及以后追加 `-r2`、`-r3`……，两轮各写各的目录，不需要任何等待或锁顺序假设。

代价：重试的 jobId 不再等于 `ShortID(hash)`。仓库内没有任何地方自行推导 jobId（客户端只用响应里的值），
所以这是纯内部约定变化。旧 job 仍可按原 id 查状态，"每次尝试是一个 job" 也比"同一 id 反复改写历史"更诚实。

顺带修掉 `DELETE` 的响应：此前无论实际状态如何都回 `canceled`，对已成功的任务点取消会让客户端
以为产物随时会消失。现在回 `j.state` 真值。

**当时未修、已在 ADR-12 落地（tasks 3.12 / 3.13）**：排队 goroutine 无上限且各自持有整份 plan；
`jobs` / `byHash` / `attempts` 三张表只增不删。ADR-12 定下 `MaxQueued` / `JobTTL` 与对应的拒绝语义。

### ADR-12 容量上限与终态保留（本轮落地，对应 tasks 3.12 / 3.13）

上一轮把「失败/取消不可重试」修掉之后，服务已经能正确工作，但还**不能长时间工作**：内存与磁盘都单调增长，
而且增长没有上限。这一轮把两个决策定下来并落地。

#### 1. 排队上限：`MaxQueued`（默认 8），超限 429

原实现的信号量在 `run` 里才获取，于是「已经受理」与「开始渲染」之间没有任何约束：
24 个客户端同时提交不同计划，就会有 24 条 goroutine 各自攥着一份整计划（单份上限 4MB）排队。

- **上限口径**：只数 `queued`。`running` 已由 `MaxConcurrent` 约束，把两者相加当上限会让并发调整互相影响
  （把并发从 2 提到 4 反而挤掉两个排队位）。
- **拒绝语义**：HTTP `429` + `Retry-After`，错误码 `queue_full`。选 429 而不是 503 —— 客户端契约里
  429 本来就归入「可退避重试」，503 表达的是「服务坏了」，而队列满只是流量超过容量，重试就能过。
- **拒绝时机**：在读请求体**之前**先判一次，锁内入队前再判一次。前者避免为注定被拒的请求读 4MB，
  后者堵住「预检到入队之间又挤进来几个」的竞态。判定排在 `allocID` 之前——被拒的提交不该消耗重试编号。
- **代价（必须写清）**：超载期间「同一份计划的幂等重复提交」也可能拿到 429。这是有意取舍：
  容量优先于去重，且 429 对客户端本就是可重试信号。非超载时幂等语义完全不变。

#### 2. 取消必须真的放掉计划

只加上限还不够：`run` 的闭包捕获了 `plan`，而取消的 goroutine 仍会堵在 `s.sem <- struct{}{}` 上。
「提交后立刻取消」的风暴可以让队列计数归零、却留下任意多条协程各持一份计划——上限被绕开。

两条修法：

- `plan` 从闭包移到 `job`，`run` 抢到槽位后再取；`Cancel` / `DELETE` 置终态时把 `j.plan` 置 nil。
- 抢槽位改成 `select { case s.sem <- ...: case <-ctx.Done(): }`，被取消的排队任务立即落终态退出，
  不再等前面所有任务跑完。

于是内存边界变成可陈述的：**排队期 ≤ `MaxQueued` 份计划，运行期 ≤ `MaxConcurrent` 份**。

#### 3. 终态保留：`JobTTL`（默认 30 分钟），从落终态起算

`jobs` / `byHash` / `attempts` 三张表原来只增不删。真实渲染每个 job 还会在磁盘留下几十 MB 产物，
磁盘比内存更早见底。

- **计时起点是 `finishedAt`，不是受理时刻**。一次 15 分钟的长渲染如果从受理算起，刚成功就会被回收。
- **过期动作**：删 `jobs[id]`、释放 `byHash`（带 `byHash[hash] == jobID` 校验）、删产物目录。
  目录删除放在锁外，避免 `RemoveAll` 期间把其它请求挡在入队判定之外（触发本次回收的那个请求仍会等它完成）。
- **`attempts` 用 live 计数**：任务被回收时减一，归零即删整条。否则它自己就是第三张只增不减的表。
- **契约**：TTL 是「任务可查询 / 产物可取」的窗口。调用方 MUST 在 TTL 内取走产物；
  过期后 `GET` 返回 404，与「从未存在」不可区分——这是刻意的，不引入新的「已过期」状态。
- **副作用（已可接受）**：`attempts` 条目删净后，同一份计划再次提交会重新拿到 `ShortID(hash)` 作 id。
  这与进程重启后的行为一致（服务没有持久化，id 本就只在进程生命周期 + TTL 内有意义），
  且目录在回收时已删除、`run` 起跑前还会再 `RemoveAll` 一次，不存在读到上一轮残留的路径。

#### 4. 回收不起后台 goroutine

`Service` 没有 `Close` 生命周期。起一条 ticker 就等于给每个测试进程留一条永不退出的协程，
而回收只在有请求时才有意义——没有请求，就没有新的内存与磁盘增长。因此改成**请求路径上的惰性回收**
（`maybeSweep`，按 `RetentionSweep` 默认 1 分钟节流），测试直接调 `sweepExpired(now)` 精确控制时点。

#### 5. 观测口径修正

`/healthz` 的 `running` 此前把 `queued` 也算进去，于是「到底几个渲染进程在吃内存」这个最该看数的指标是假的。
现在 `running` 只数真正在跑的，`queued` / `maxQueued` / `retained` 各自单列。

#### 已知限制

- ~~**重启即丢索引**：进程重启后 `/work` 下的目录成为孤儿~~ —— 已由 ADR-13 解决（启动清扫）。
- **`queuedLocked` 是 O(存活 job 数) 扫描**：TTL 内的 job 数量有限（默认 30 分钟窗口），实测可忽略；
  若未来 TTL 拉长到数小时，应改为维护计数器。

### ADR-13 重启后的孤儿目录：启动清扫，不引入持久化索引（本轮落地，对应 tasks 3.14）

ADR-12 的 `JobTTL` 解决了「服务一直跑着」时的磁盘增长，但管不到跨进程：`jobs` 只在内存，
进程一重启，`/work` 下上一轮留下的 job 目录就再没有任何记录能回收它们。容器反复重启（部署、OOM、节点漂移）
会让磁盘单调增长。

#### 1. 为什么不做持久化索引

看起来更「完整」的做法是把 job 元数据写进 `/work/<id>/job.json`，启动时恢复。评估后放弃：

- **恢复出来的 id 没有消费者**。契约里 id 只在「进程生命周期 + `JobTTL`」内有效，重启后客户端手里的 id
  本就该按 404 处理。把产物留在盘上，没有任何人能凭 id 取到它。
- **要恢复就得回答一堆新问题**：重启时正在渲染的任务标 failed 还是 canceled？半成品目录算不算产物？
  `byHash`/`attempts` 怎么重建？每一个都是新的状态机分支，而收益是零。

所以本服务坚持**无状态**：重启 = 上一轮的一切作废。

#### 2. 清扫的范围：只删本服务分配的名字

`NewService` 在 `MkdirAll` 之后、返回之前扫一遍 `WorkDir` 顶层，删除名字匹配
`^[0-9a-f]{24}(-r[1-9][0-9]*)?$` 的目录（`ShortID` + 重试后缀，见 ADR-11）。

只认自己造的名字，是为了让 WorkDir 能和别的东西共处。`cmd/renderer -selftest` 用
`os.MkdirTemp(workDir, "selftest-")` 造目录、运维可能放 dump、文件系统自带 `lost+found`——
重启不是「把不认识的目录都删掉」的理由。副作用是**目录名形态即契约**：以后若改 `ShortID` 的长度，
清扫规则必须同步改，否则老目录会永久残留（下次改这里时记得看这条）。

单个目录删失败（Windows 句柄未释放、权限不足）不阻塞启动：记进 `OrphanReport.Failed` 打日志，
下次重启再试。一个删不掉的目录不该让整个服务起不来——能起来报错比起不来更好诊断。

#### 3. 多副本共享 WorkDir 必须显式关掉

启动清扫的隐含前提是**单副本**。若多个渲染进程共用一个 `WorkDir`，先起的那个会把其他副本
正在写的目录删掉，表现为「偶发产物丢失」，极难排查。这类部署必须 `-keep-orphans`
（`ServiceConfig.KeepOrphans`）。

没有用「只删 mtime 超过 N 分钟的目录」当保护：它挡不住「另一个副本 20 分钟前开始、现在还在写」的目录，
只会给出虚假的安全感。共享存储的正确形态是每个副本一个子目录，那时清扫也不该跨副本生效——
所以这里选择显式开关，而不是一个看起来能自适应的阈值。

#### 4. 观测

清扫结果走 `ServiceConfig.Logf`（`cmd/renderer` 接到 `log.Printf`）：

```
render: 启动清扫删除 3 个孤儿目录，释放 128.4 MiB
```

体积小于 1 MiB 时退到 KiB——日志里出现「释放 0.0 MiB」会让人以为清扫没生效。
清扫不产生新指标，也不进 `/healthz`：它是一次性启动动作，没有「当前值」可报。

### ADR-14 底图怎么进服务：只读资产根 + 起跑时复制（本轮修复，对应 tasks 5.4）

#### 1. 现象

容器里提交同一份 5 页 / 30s 样片（12KB 计划，5 张 1920×1080 PNG）时，任务稳定失败：

```
{"state":"failed",
 "error":"render: page 0 base image: open /work/<job-id>/p000.png: no such file or directory"}
```

这是**契约缺环**，不是配置问题：M2 的渲染服务单测全部用 `StubBackend`，它不读底图，
所以「底图从哪来」这条路径从未被走过。任何带元素级图层的计划都会卡在 staging。

#### 2. 根因：两条各自正确的约束叠在一起

1. `run()` 起跑前 `RemoveAll(dir)`（ADR-11 的重试语义要求上一轮半成品必须消失），
   于是提交时计划里的相对路径在起跑那一刻必然不存在；
2. 合成器用 Remotion 的 `staticFile()` 取图，它的根是 `plan.json` 所在目录（工作目录），
   **读不到目录之外的任何路径**——这条是 Remotion 的安全设计，不该绕。

两者相加：调用方无论把底图放在哪里，服务都看不见。

#### 3. 方案比较

| 方案 | 结论 |
|---|---|
| 计划内嵌 base64 | 16MB 请求上限装不下真实页面图（30 页 × 2–3MB），且 planHash 会随二进制膨胀 |
| 允许 `baseImage` 为绝对路径 | 直接破坏 `SafeAssetPath`，等于开放任意文件读取 |
| 持久化索引 / 预置 job 目录 | job id 服务端生成，调用方提交前拿不到；恢复索引是 ADR-13 已否决的方向 |
| **只读资产根 + 起跑时复制** | 采用：不动 IR、不动哈希、不动合成器 |

#### 4. 语义

`ServiceConfig.AssetDir`（`-assets-dir`）是**只读**资产根，起跑时把计划引用的底图复制进 job 目录：

- `baseImage` 同时相对**资产根**（读）与**工作目录**（写）解析，两侧都走同一套路径校验；
  `..`、绝对路径、盘符一律拒绝。
- **只复制计划真正引用的文件**，按相对路径去重。工作目录常是 2g tmpfs，而资产根可能装着
  几十页无关页面。
- 用 `os.Lstat` 判类型，**符号链接一律拒绝**。这条是实测补的：最初写 `os.Stat`，
  它会跟随链接，一个指向 `/etc/passwd` 的链接会被判成普通文件复制进来——路径字符串合法，
  内容却在资产根之外。
- 复制不改变计划字节，因此 **`planHash` 不受影响**：换资产根不会让同一份计划变成另一份缓存。
- 资产根在 `NewService` 校验存在且是目录：配错应当让服务起不来，而不是让每个任务各失败一次。
- 不传 `-assets-dir` 时保持旧行为（不搬运）。这条路径只对「计划里没有元素级图层」的场景可用。

产物目录因此是**自包含**的：底图副本、`plates/`、`layers/`、`plan.json`、`segment_*.mp4`
都在同一个目录内，`JobTTL` 到期只删这一个目录，不会留下指向别处的引用。

#### 5. 已知代价

- `artifacts` 列表是「job 目录里的全部文件」，因此现在也会列出底图副本与 `plates/`/`layers/`。
  这是既有行为（`plan.json` 早就被列进去了），客户端本来就该按扩展名取 `segment_*.mp4`；
  真要收紧应单独定义产物清单，不在本条里顺手改契约。
- 资产根由**调用方**负责就位（上游页面栅格化的产物），服务只读、不清理、不校验新鲜度。

### ADR-15 字体子集：边界写进 `charset.txt`，版本由字集摘要推导（本轮落地，对应 tasks 4.8）

#### 1. 起点：两个数字都对不上

ADR-4 写的是「装 Noto Sans SC，子集化把 9MB 降到 ~1.5MB」。实测镜像里是：

| 文件 | 体积 |
|---|---|
| `NotoSerifCJK-Bold.ttc` | 27MB |
| `NotoSerifCJK-Regular.ttc` | 26MB |
| `NotoSansCJK-Bold.ttc` | 20MB |
| `NotoSansCJK-Regular.ttc` | 19MB |
| `NotoSerif/Sans` 合计 + `Liberation` | **91MB** |

91MB，不是 9MB。四个 TTC 里装着 SC/TC/JP/KR/HK 五个地区的字形（每个 TTC 10 个字面），
而服务实际只可能用到「中文 + 常见符号」。

#### 2. 先证伪「字体到底有没有被用到」

合成器 `renderer/src/Timeline.tsx` 里只有 `<Img>`，没有任何文本节点；页面底图是上游烘焙好的 PNG。
所以**这个服务不排版文字**。实验核对：把 `/usr/share/fonts` 整个 bind-mount 成空目录
（`fc-list` 输出 0 个字体），用同一份 5 页样片重渲，5 个片段的 sha256 与装全量字体时**逐一相同**。

```text
SAME segment_000.mp4 7083697…  SAME segment_001.mp4 f0380b1…
SAME segment_002.mp4 69ceb32…  SAME segment_003.mp4 d8c149b…
SAME segment_004.mp4 a0a2956…
```

这条实验的边界必须说清：样片是**色块占位**、没有真实字形，所以它证明的是
「当前合成路径不读字体」，**不能**证明「字体对未来的真实文案无所谓」。

那为什么还要留字体？因为规范（`specs/renderer-image`）要求镜像内置可再分发字体、
`/healthz.fonts` 必须上报、字体版本必须进 `planHash`。这三条是**契约**：调用方据此判断
「这份文案的字体，渲染端是否认得」，将来加文字图层（字幕/标题动画）时它是前提。
所以处置方式不是删字体，而是**把 91MB 压到与用途相称的规模，并把边界写成可验证的东西**。

#### 3. 字集边界：`deploy/renderer/charset.txt` 是唯一事实源

边界不是拍出来的，是量出来的：

| 方案 | 体积/字面 | 结论 |
|---|---|---|
| GB2312（6763 字） | 3.37MB | 否：`喆 堃 犇 垚 玥 珺 赟 燚 弢 韡 祎` 全部覆盖不了，真实人名会变方框 |
| GBK（21003 字） | 10.74MB | 可用，但覆盖依赖某个编解码表实现 |
| **URO（4E00-9FFF，20992 码位）** | **10.78MB** | 采用：与 GBK 覆盖几乎相同，且是 Unicode 的稳定区间 |

最终 `charset.txt` 收：ASCII、Latin-1、希腊/西里尔、通用标点、货币、℃ №、箭头、①②、
制表符、■▲●、★☆、CJK 标点、假名、URO、全角。**不收** CJK Ext-A/Ext-B：生僻字与异体字
走 fallback，要收就改这一个文件。

两档语义是这轮的第二个修正。最初把 2000-206F 整段标成「必须覆盖」，构建直接失败：
Noto Sans CJK SC 在该区间缺 78 个码位（`‗ ‛ ‣ ‱ ‽ ⁀` 这类生僻符号）。于是改成：

- `must`：汉字、CJK 标点、全角、假名、拉丁、常用标点（`– — “ ” … •` 等逐个列出）。
  缺一个 → 构建失败。
- 其余：**best-effort**，字体有就收、没有就跳过。缺的交给 Chrome 的 fallback 链（Liberation Sans）。
  实测 774 个 best-effort 码位在源字体里不存在，它们本来也画不出来，标成 must 只会让构建永远红。

同一次失败还暴露了另一件事：URO 末尾 `9FF0-9FFF` 共 16 字是 Unicode 11 新增的，
本字体（2.004）没收录。这 16 个码位单独降成 best-effort 并在文件里注明——**不假装覆盖**。

#### 4. 版本号不再手写

原来的链条是：人肉在 Dockerfile CMD 里写 `-fonts "Noto Sans CJK SC@20230817"`。
改字集的人改的是 `charset.txt`，不会记得回来改 CMD，而 `planHash` 恰恰靠这个版本号
区分像素版本——一次遗忘就是「字集变了、缓存键没变、旧片段被当成新片段返回」。

现在：

```text
charset.txt --sha256--> subset-fonts.py --> /etc/ppts/fonts.version
                                            --> entrypoint.sh 拼出 -fonts
                                            --> planHash
```

版本号形如 `2.004-sub03e7e8d7`（`2.004` 是字体自身的 Version，后 8 位是摘要）。
改 `charset.txt` 一个字，版本号就变，`planHash` 就变。**结构上不可能漂**。
`Dockerfile` 的 `CMD` 里因此不再有 `-fonts`，由 `deploy/renderer/entrypoint.sh` 注入
（`exec` 交给 Go 进程，信号照常到达 PID 1）。

#### 5. 构建门禁

`subset-fonts.py` 不满足下面任一条就以非零退出，构建失败：

1. `charset.txt` 里标 `must` 的码位，源字体有 → 子集产物也必须有（防止「声明覆盖、实际缺字」出厂）；
2. 子集后 `name` 表仍报 `Noto Sans CJK SC`（fontconfig 靠它匹配；改了名会让 `/healthz.fonts` 与事实不符）；
3. `base` 段 `fc-cache -f` 之后 `fc-list` 必须精确匹配到该家族名——**装了但 fontconfig 认不出来**
   的表现是「`/healthz` 报着字体、Chrome 却走 fallback」，这种失败必须在构建期暴露。

`python3-fonttools` 只活在 `fontsubset` 段，运行时镜像里没有 python。

#### 6. 结果（实测）

| 项 | 改前 | 改后 |
|---|---|---|
| `/usr/share/fonts` | 91MB（4 个 TTC、20 个文件、19 个家族） | **24MB**（2 个 OTF + Liberation，5 个家族） |
| 家族数 | Noto CJK SC/TC/JP/KR/HK × Sans/Serif × Regular/Bold | `Noto Sans CJK SC`（Regular/Bold） |
| `/healthz.fonts` | `Noto Sans CJK SC@20230817`（手写） | `Noto Sans CJK SC@2.004-sub03e7e8d7`（推导） |
| 镜像 | 1.36GB | **1.23GB** |
| 运行时 python | 无 | 无（`fonttools` 只在校验 stage） |

像素等价是逐字节验的：同一份 5 页样片在新旧镜像上各出 5 段，sha256 全部相同
（`7083697…` / `f0380b1…` / `69ceb32…` / `d8c149b…` / `a0a2956…`）；
另外把子集 OTF 挂进旧镜像的 `/usr/share/fonts/opentype/noto` 重跑，同样逐字节相同；
自检 `selftest ok: backend=remotion pages=2 elapsed=9.42s`。

**这条等价性结论的适用范围要收窄**（2026-10-29 补记）：当时那份样片是用色块占位的，
画面里一个字形都没有。所以它证明的是「合成器不依赖字体」，**不能**证明「子集字体的字形与
原字体一致」。后者要靠带真实文字的样片来验，见下面 §7 的空格缺陷。

镜像体积那条的取得方式要说明：本机代理只有 ~25–70KB/s，`base` 段那 121MB 的 Chrome
下载跑了两小时没完成，最终用于验收的镜像是「按新 Dockerfile 构建出的字体侧镜像 +
从旧镜像 `COPY /opt/chrome`」拼出来的等价物。Chrome 下载与摘要校验那一步本身未改动，
在 4.6 / 4.5 已验过；体积数字因此是**测算值**，不是那次干净构建的输出。

#### 7. 补记：子集把空格丢了（2026-10-29 修，对应「视频丢信息」）

用子集字体真排一版中文页面时，凡是空格的位置全是方框（`5□段□sha256□`）。
查下来缺的就是两个码位：

| 码位 | 字符 | 类别 | 为什么被扔 |
|---|---|---|---|
| U+0020 | 空格 | Zs | 属于 `NON_DRAWING`，被排除出 `required`，而 `populate` 只喂了 `required` |
| U+3000 | 全角空格 | Zs | 同上 |

根因是断言口径与产出集合被混成了一个变量：断言只该看「可绘制」码位（否则 U+2000 这类永远不达标），
但**子集本身不能只收可绘制码位**。空格不出字形，却决定词间距与中英混排的断行位置；
从 cmap 里消失后，用这份字体排版就是方块，或者整行宽度变化。

三处修改：

1. `populate` 改为喂「源字体真实拥有的全部声明码位」（`must` + best-effort + 不可绘制但源里有的）；
   断言仍只针对可绘制码位，两个集合分开。
2. 子集后自检从「must 都在」改成「wanted 都在」，顺带把不可绘制的那部分也管住。
3. **版本号改为覆盖「实际保留码位集」**，不再只取 `charset.txt` 的摘要。否则这次修脚本
   而 `charset.txt` 一个字没动 → 版本不变 → `planHash` 不变 → 缓存把旧片段当新片段返回。

结果：每字面码位 22209 → 22215（+6：`0020`、`00A0`、`2000–200A` 中源里存在的几个、`3000`），
版本从 `sub4d24a894` 变成 `sub03e7e8d7`；同一版带空格/全角空格的中文页面重排，方框归零。

留一条可复用的教训：**只覆盖「能画出来的东西」的断言，正好漏掉「不画但影响排版」的东西。**
落字面的字符集要按「排版输入」而不是「字形表」来收。

#### 8. 已知边界

- **Ext-A/Ext-B 与 9FF0-9FFF 不在边界内**：这些字在字体里确实没有，命中就是方框。要收就改 `charset.txt`
  并把对应行标 `must`，构建会告诉你字体支不支持。
- **符号区靠 fallback**：希腊字母、箭头等只收到字体有的那部分。fallback 命中 Liberation Sans，
  不是方框，但与「声明必须覆盖」是两回事，别混。
- **子集不含运行时子集化**：镜像内字体是固定的，同一份计划在任何一台渲染机上出同样的字。
  按文案运行时子集化需要缓存与并发管理，换来的只有几百 KB，不做。

### ADR-16 资源限额重估：三处估算被压测同时推翻（本轮修复，对应 tasks 5.6）

#### 1. 起点：这些数字都是估的

`compose.yml` 里的 `cpus: 2` / `mem_limit: 2g` / `pids_limit: 256`，加上镜像 CMD 里的
`-concurrency 2`，都是按「单 job 峰值内存 ~2GB」拍出来的。注释里这句话本身就是估值，
此前没有任何一次长片压测验证过它。5.6 的目的就是把它变成可复现的测量。

#### 2. 口径：为什么用 cgroup 而不是宿主 ps

- **峰值内存读容器内 `/sys/fs/cgroup/memory.peak`**（cgroup v2）。宿主采样会漏掉瞬时尖峰，
  而且 `docker stats` 两秒一次，正好错过每页收尾时的抬升。
- **`/work` 用 tmpfs 时，写进去的每个字节都记在同一个 memory cgroup 里**。这一点决定了
  「工作目录挂 tmpfs」不是免费的：5 分钟长片在 `/work` 里写出 264MB（底图副本、`plates/`、
  `layers/`、30 个片段），这 264MB 与 Chrome 抢同一份 `mem_limit`。所以峰值必须按
  容器整体量，不能只量 Chrome。
- **OOM 只认 `memory.events` 的 `oom_kill` 与 `State.OOMKilled`**：渲染器被 SIGKILL 时
  Node 侧的报错是 `FFmpeg quit with code 1 (signal SIGKILL)`，看不出是内存问题。
- 用例：30 页 × 10s = 300s / 9000 帧；底图叠细纹理。纯色块会让编码器几乎不干活，
  压测出来的「很快」是假的（5.4 的样片就属于这种）。

#### 3. 测量结果

同一台机器（WSL2，24 核 / 15GB），容器 `--network none --read-only`，`/tmp` 512m tmpfs：

| 用例 | 核 | 任务×帧并发 | mem_limit | pids_limit | 视频长 | 墙钟 | 峰值内存 | PIDs 峰值 | 结果 |
|---|---|---|---|---|---|---|---|---|---|
| a1 | 2 | 1×1 | 4g | 无 | 50s | 212s | 2095MB | 243 | 成功 |
| a6 | 2 | 1×1 | 2g | 无 | 50s | 213s | 1949MB | 242 | 成功（余量 51MB） |
| a2p | 2 | 1×2 | 4g | 无 | 50s | 174s | 2293MB | 264 | 成功 |
| a2 | 2 | 1×2 | 4g | **256** | 50s | 139s | 2216MB | 257 | 失败（第 4 页后 EPIPE） |
| a5 | 2 | 1×2 | **2g** | 无 | 50s | 21s | 2048MB | 234 | **OOM** |
| a3p | 2 | **2×2** | 4g | 无 | 100s | 56s | 4096MB | 482 | **OOM（两个任务都失败）** |
| e8 | 2 | 2×2 | 8g | 无 | 100s | 374s | 3624MB | 542 | 成功 |
| p4 | **4** | 1×2 | 4g | 无 | 50s | 131s | 2200MB | 263 | 成功 |
| b | 2 | 1×2 | 4g | 无 | **300s** | **943s** | 2666MB | 389 | 成功（30 段） |
| f | 4 | 2×2 | 6g（tmpfs `/work`） | 1024 | 600s | 948s | 4826MB | 783 | 成功（60 段） |

片段核验（b 的同一份计划，抽 5 段逐段 ffprobe）：全部 1920×1080 / `yuv420p` / 30fps /
300 帧 / 10.000s，单段 6.4–7.6MB（细纹理底图，属偏重的一侧；纯色 PPT 底图会小一个量级）。

#### 4. 三处被推翻的估算

1. **`mem_limit: 2g` 不够，而且差得不是一点**：单任务、单帧并发在 2g 下峰值 1949MB，
   只剩 51MB 余量（2.5%）；同一份计划把帧并发开到 2，第 21 秒就 OOM。30 页长片的峰值是
   2666MB。**2g 是在拿「刚好等于峰值」当限额**，任何一次 Chrome 小版本升级都会把它顶穿。
2. **`pids_limit: 256` 不够**：单任务 5 页峰值 264、30 页 389，都超过 256。撞上限制的表现
   不是清晰的错误，而是 Chrome 的 clone 失败后 Node 报 `write EPIPE`、渲染在第 4 页崩掉，
   日志里没有任何一句提到进程数。这个限额必须按**最长片子**留余量，不能只按并发数算。
3. **默认 `-render-timeout 15m` 装不下 5 分钟长片**：2 核下 30 页 / 300s 实测 943s（15m43s），
   默认值会在第 27 页左右把任务掐断，而且失败原因是超时，看起来像「渲染卡住」而不是「配置太紧」。

#### 5. 计数器发现：`-concurrency` 有两个身份，成本是乘积

`cmd/renderer` 把同一个 `-concurrency` 既喂给 `ServiceConfig.MaxConcurrent`（同时跑几个任务），
又喂给 `RemotionBackend.Concurrency`（每个任务并行渲几帧）。于是 `-concurrency 2` 在满载时是
**2 任务 × 2 帧 = 4 个并发 Chrome 渲染**。实测：4g 下两个任务并发 56 秒 OOM（峰值顶到 4096MB，
`oom_kill 2`），8g 下同样的两个任务峰值 3624MB 才通过。

顺带量到一个反直觉的结论：**帧并发从 1 提到 2 只快 22%（212s → 174s），却多花约 200MB**。
在 2 核上这个旋钮的收益很低——瓶颈是 CPU，不是帧流水线。

#### 6. 修正取值与依据

| 项 | 原值 | 新值 | 依据 |
|---|---|---|---|
| `mem_limit` | 2g | **6g** | 2 任务 × 30 页并发实测峰值 4826MB（§3 用例 f），余量 21%。单任务 4g 够（2666MB），但默认并发是 2 |
| `pids_limit` | 256 | **2048** | 实测 2×30 页峰值 783、单任务 30 页 389；用例 f 用 1024 已通过（余量 31%），但 PIDs 随页数单调增长，取值放大到 2.6× 余量 |
| `cpus` | 2 | **4** | 单任务 4 核 131s vs 2 核 174s（1.33×）；且 4 核下两个任务才能把 4 个并发渲染位喂饱（§7） |
| `-render-timeout` | 15m（默认常量） | **按片长推导**：`6 × 片长 + 5min`，夹在 `[调用方给定值, 6h]` | 常量装不下长片（5 分钟片实测 943s > 900s）；按内容放宽又必须夹天花板，`durationMs` 没有上界 |

超时改成推导而不是再拍一个更大的常数：`RemotionBackend.exec` 用 `contentDuration(plan)` 算片长，
`renderTimeout(base, content) = clamp(content×6 + 5min, base, 6h)`。6 倍是实测 3.1× 的两倍余量，
5 分钟固定开销盖住 staging（底图复制 + 底板/图层生成，30 页约 200s）与 webpack 冷启动（约 40s）。

**验证方式**：把 `-render-timeout` 压到 **1s** 跑同一份 50s 计划，仍然成功（155s 出 5 段）。
若放宽没生效，1s 会在第一帧就杀掉子进程；这条用例比「读代码确认」有用——它同时证明
「地板值不会挡住长片」与「推导用的是计划里的时长，不是页数或别的估计量」。

`cpus` 与 `mem_limit` 的关系要写清楚：这两个值是**同一台机器的两种切法**，不是独立旋钮。
把 `cpus` 降到 2 而保留 `-concurrency 2`，吞吐会掉到 3.1× 实时；把 `mem_limit` 压到 4g 而
保留 2 个任务，第二个任务会在第 21 秒被 OOM 杀掉（§3 用例 a5 / a3p）。

#### 7. 容量模型

单任务（`-concurrency 2` = 2 个渲染位）用不满 4 核，两个任务才饱和。按 10s/页、1080p30、细纹理底图：

| 场景 | 核 | 墙钟 | 折合每页 | 相对实时 |
|---|---|---|---|---|
| 1 任务，50s 片 | 2 | 174s | 35s | 3.5× |
| 1 任务，50s 片 | 4 | 131s | 26s | 2.6× |
| 1 任务，300s 片（30 页） | 2 | 943s | 30s | 3.1× |
| 2 任务，各 300s 片（60 页） | 4 | 948s | 13.5s（摊到两任务） | 1.6× |

最后一行是这一节最有用的一条：**4 核上两个任务并发时，聚合吞吐是单任务的近 2 倍**
（0.63 vs 0.34 秒视频/秒墙钟），所以 `-concurrency 2` 在 4 核上是省机器的，不是浪费。
反过来，两个任务抢 2 核时会互相拖慢到 1.6× 实时以下——`cpus` 与并发数必须一起调。

粗估公式（4 核、两任务）：`每任务墙钟 ≈ 5min + 页数 × 14s`。

#### 8. 已知边界

- **PIDs 随页数单调增长**：5 页 264 → 30 页 389（单任务，约 +5/页），2×30 页 783。曲线是锯齿上行
  而不是锯齿平顶，像是有东西没在页边界回收。本 ADR 只按实测留 2.6× 余量（2048），没有断言这是泄漏；
  以后做 100 页以上的片子要重新量。定位方法：渲染期间每 2s 采 `docker stats` 的 PIDs 列，看它是锯齿还是单调上升。
- **`/work` 挂 tmpfs 时，产物占的是内存**：2×30 页写出 527MB（底图副本、`plates/`、`layers/`、60 个片段），
  这 527MB 与 Chrome 抢同一个 `mem_limit`。要跑更长的片子，要么给 `mem_limit` 加余量，
  要么把 `/work` 换成有容量上限的卷（代价：Chrome 的落盘 CVE 有了可写面，见 compose 第 1 条）。
- **`-concurrency` 的双重身份没有拆**：拆成 `-jobs` 与 `-frames` 是更干净的模型，但会改变
  命令行契约与 compose 写法，不在本轮范围内。当前取值下两者同为 2，是可用而非理想。
- **x264 用 34 线程跑在 2–4 核上**：Remotion 调 ffmpeg 时按宿主机核数开线程，容器 `--cpus`
  限制下是超订状态。它不影响正确性，但会放大 PIDs 与调度抖动；要压这一项得改编码参数（ADR-7），
  属于画质/体积的取舍，本轮不动。

### ADR-17 图层绘制顺序是语义，不是巧合（本轮修复，对应「视频丢信息」）

**症状**：同一份计划的某些页，画面上的要点文字整块消失；任务 `succeeded`，没有 warning，
没有 error，`planHash` 正常，只有人眼看得出。

**机制**：`BuildLayerAssets` 为了 ADR-9 会把「被父层包住的子元素」从父层位图里抹掉，
再单独出一张子层图；合成器 `PageView` 按 `page.layers` 的**数组顺序**绘制。
父层一旦排在子层之后，就会把刚画上去的子元素整块盖回底色。

原来的代码里只有一句注释：「当前编译产物按 track 顺序出层，正是这个顺序」。
而 track 顺序来自编排结果（`animation.Revision.Tracks`），IR 契约里没有任何东西保证
「父在子前」——API 直接 POST 上来的计划更不会保证。**写在注释里的约束不是约束。**

**证据**（同一份计划，只把 `p1-card` 从第 2 位挪到最后一位，几何/时长/内容一字未改）：

| 区域 | 源页墨量 | 改序后墨量 | 改序后整页平均差 |
|---|---|---|---|
| item0 文字 | 5.70% | **0.00%** | 3.83（原顺序为 0.67） |
| item1 文字 | 6.07% | **0.00%** | |
| item2 文字 | 6.03% | **0.00%** | |

**修法**：`orderLayersParentsFirst`（`internal/render/layerorder.go`）做稳定冒泡：
只在**确实存在包含关系**时把外层提前，并列/部分重叠的层保持原序（互换会带来真实的 z-order 变化，
那是另一件事）；同 rect 的层互不交换（`BuildLayerAssets` 本身也互不抹除，交换只会让结果不稳）。
两处调用：

- `Compile` 产出 IR 时排序——IR 是对外契约，别的消费方也按数组顺序画；
- `RemotionBackend.stage` 再兜一次——外部直接 POST 的计划不经过编译器。

**验证**：同一份乱序计划重渲，三处文字墨量从 `0.00%` 回到 `5.70% / 6.09% / 6.03%`（与源页一致），
整页平均差 3.83 → 0.67（0.67 是 H.264 的编码噪声水平，见 §ADR-18）。

**代价与边界**：排序会让 `layers` 数组顺序可能与请求里的不同（staging 阶段在算 hash 之后），
所以「内容相同但顺序不同」的两份计划仍命中同一份缓存——修完之后它们的渲染结果确实相同，这个行为是正确的。
`Validate` 不检查顺序（它是只读的），约束由唯一的画布消费者兜底。

### ADR-18 出片保真基线：末帧与源底图的差异上限（本轮建立）

「视频有没有丢信息」需要一个能重复的判据，否则每次只能靠人看。做法：取每段的**末帧**
（入场动画已回到恒等），与源页 PNG 逐像素比：

| 页 | 平均绝对差 | 最大差 | 差 >32 的像素占比 | 最差元素区域内 >32 |
|---|---|---|---|---|
| 0 | 1.23 | 58 | 0.138% | 1.20% |
| 1 | 0.67 | 66 | 0.117% | 0.09% |
| 2 | 0.59 | 63 | 0.344% | 0.29% |
| 3 | 0.56 | 55 | 0.089% | 0.07% |
| 4 | 1.23 | 37 | 0.000% | 0.00% |

结论：整页平均差 ≤1.3、差 >32 的像素 <0.4%，全部落在 H.264 编码噪声里，**没有结构性丢失**。
另一条独立判据是「墨量」：元素区域内偏离中位色的像素占比。中位色只从**源图**取——
渐变区两边各取中位会得到不同基准，算出来的是基准漂移而不是像素差异（第一版就是这么虚报了 3.7 个百分点）。
统一基准后 5 页最差的一处是 p3 的渐变图区：37.96% vs 38.90%（差 0.94 个百分点，同区域差 >32 的像素仅 0.068%），
其余区域差 <0.08 个百分点。ADR-17 里 `0.00%` 那一组就是用这个指标抓出来的。

这份基线怎么用：改合成器/底板/图层算法后跑同一样片比对，判据落在 `scripts/fidelity/`：

| 指标 | 阈值 | 实测（正确实现） |
|---|---|---|
| 整页平均绝对差 | ≤ 2.0 | 0.56–1.23 |
| 差 >32 的像素占比 | < 1% | < 0.35% |
| 元素区域墨量比（视频/源页） | ≥ 0.8 | ≥ 0.98 |

超任一条 `compare.py` 以非零退出。样片由 `gen_pages.py` 生成、`plan/` 编译成计划，
字体取自镜像（`docker cp` 出来的子集 OTF），所以「字体缺字」会直接出现在画面上。
**色块样片不做回归用**——它对字形与空格完全不敏感，用它做回归等于没做（ADR-15 §7 就是这么漏掉空格的）。

### ADR-19 「Studio 人工预览」换成可复跑的逐像素核对（本轮落地，对应 tasks 2.8）

2.8 原文是「`npm run studio` 人工预览：分辨率/颜色需人眼过一遍」。两个问题：Studio 是交互式界面，
无头环境（以及 CI）里根本打不开；而「人眼看颜色」的分辨率大约 2–3 个色阶，恰好是色彩管理最容易
出错、也最容易被放过的地方。

关键事实是：Studio 预览的画面不是另一个渲染器，它就是 `renderer/src/index.tsx` 注册的
`Timeline` composition——同一份代码、同一个 Chrome、同一套 props。所以这一步可以换成等价且更严的检查：

1. `node renderer/still.mjs --plan <job>/plan.json --frame <N> --out f.png`（与 Studio 同一 composition）；
2. `python3 scripts/review/still-check.py --still f.png --page <job>/pages/pNNN.png --settled`
   （动效进行中用 `--confined-to X,Y,W,H`）。

判定分两种，对应人眼在这件事上真正能看出的两类错：

- **稳定帧必须与源页逐像素相同**：平均差 ≤1.0 且差 >32 的像素 <0.5%。PNG 没有编码噪声，
  所以只留抗锯齿级余量；色偏、缩放、黑边、裁切都会立刻超限。
- **动效进行中的帧，差异必须全部落在该元素框内**（±8px）。框外出现差异意味着整页缩放/位移/裁切——
  这正是人眼最不易察觉、后果最严重的一类错（画面「动」着的时候人眼只跟踪动的东西）。

2026-10-29 实测（5 页样片、1920×1080、本机 Chrome 无头）：5 页稳定帧全部**逐像素相同**
（平均差 0.000、差 >32 的像素 0），样本里特意包含深色页（`#0E1B2E`）与渐变页——只在浅色底上
看不出色偏的实现，在深色底上会被放大；动效进行中的第 10 帧差异 bbox `x[119,985] y[331,424]`，
完全落在标题框 `[116,288,1320,130]` 内。

反向验证：拿 p000 的帧去比 p001 → 平均差 19.7、差 >32 的像素 7.8%，两条判据同时报错；
把元素框缩到宽度 300 → 23545 个像素落在框外报错。检查不是空转。

**这条只证明「画布换算与颜色」**：`still.mjs` 直出的帧没有经过 staging 的抹除图层（`layer.image`），
元素层叠在完整底图上，所以稳定帧与源页相同是预期结果。它不替代 ADR-18 的成片比对——
「子元素有没有被父层吃掉」仍然只能靠成片末帧去查。

未覆盖：Studio 自身的交互面板（时间轴、props 编辑器）不是交付物，不影响成片，未纳入。

### ADR-20 部分重叠的兄弟元素：修不掉，但不能再沉默（本轮落地，对应 tasks 5.9）

ADR-9 的抹除只在**包含关系**下成立：父层位图里挖掉子元素的矩形，父层就不带子元素。
两个兄弟元素若**部分重叠、谁都不包含谁**，两张层位图会各自烘进对方落在重叠区的像素，
动谁另一个就跟着拖影。tasks 5.9 一直把它记作「模型限制」，但此前是**完全静默**的：
任务 `succeeded`、无 warning、planHash 照常，画面错了没人知道——这正是本项目反复踩的
那类坑（ADR-15 丢空格、ADR-17 吃子元素都是同一个形态）。

不修它的理由要说清：单张栅格推不出逐元素 alpha，要根除必须在**出图前**就知道每个像素
属于谁，那等于要求源文件按元素分别渲染——超出「以底图 + 元素矩形为输入」这条契约。
所以本轮只做可观测：

- 编译期在 `orderLayersParentsFirst` 之后跑 `partialOverlaps`，命中就出一条
  `sibling_overlap_partial` 诊断，带**第一对**元素 id（其余按条数汇总），页面级仍是一条，
  和其他诊断一样不阻断渲染；
- 重叠面积门槛 **5%**（相对较小的一块）。文本框包围盒普遍带内边距，标题盒与副标题盒
  常有一两像素交叠且交叠区没有墨——纯几何判定会天天误报，告警也就没人看了；
- 负例同样被测：包含关系（抹除算法覆盖）、完全不相交、边距级交叠、0×0 几何都不报。

端到端验证：5 页样片编译后仍只有 `duration_tier_conflict`（无误报）；把 p0 的副标题框
上移到与标题框重叠 38px 后，plan 立刻多出
`page 0 sibling_overlap_partial: 1 对兄弟元素互不包含但部分重叠（如 p0-title / p0-sub）`。

**2026-10-31 收口（tasks 5.9）**：这条按「已知限制」关闭，不再作为未闭环缺陷挂着。
依据是规格已经把它写成可执行的边界，而不是一句备注：`renderer-image` 的「嵌套图层先抹掉
子元素再抬升」要求诊断**必须产出**、「兄弟元素部分重叠」场景明确残影**可以出现、不按回归
处理**。也就是说，后来的人知道它会错、能查到它为什么错、也不会把它当成新引入的 bug 去修。

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
| `internal/render/janitor.go` | 启动清扫：删除上一轮进程留下的孤儿 job 目录（ADR-13） |
| `internal/render/assets.go` | 底图只读资产根：起跑时把引用的底图复制进 job 目录（ADR-14） |
| `deploy/renderer/Dockerfile` | 三段构建（Go / 字体子集 / 运行时） |
| `deploy/renderer/CHROME_VERSION` | Chrome 版本单一事实源 |
| `deploy/renderer/charset.txt` | 字体字集边界单一事实源（`must` / best-effort 两档，ADR-15） |
| `deploy/renderer/subset-fonts.py` | 构建期子集化 + 覆盖率/家族名断言，推导字体版本 |
| `deploy/renderer/entrypoint.sh` | 把 `/etc/ppts/fonts.version` 拼成 `-fonts` 后 exec Go 服务（ADR-15） |
| `deploy/renderer/compose.yml` | 本地/单机部署样例（资源限额、只读 rootfs、tmpfs） |

## 4. 风险

| 风险 | 缓解 |
|---|---|
| Chrome 升级导致像素漂移 | 版本进 `planHash`；`CHROME_VERSION` 单点管理；升级需重跑样片核对 |
| 镜像体积 ~1.5GB | 独立镜像，与 API 解耦；主 API 镜像体积零增长（M1 的硬指标） |
| 中位色补丁在照片底露馅 | `plate_patch_noisy` warning + 样片人工核对；根治靠页面渲染侧背景板（M6-4） |
| Node 与 Go 的 IR 字段漂移 | `renderer/src/types.ts` 与 Go 结构逐字段对齐；`plan.golden.json` 作为两端共用夹具，Node 端加载它做类型断言 |
| 容器内 Chromium 崩溃/OOM | 服务内并发上限 2；单页 60s 超时；崩溃映射为可重试（M2 D7）。**限额已按 5.6 实测重估**：6g / 2048 PIDs / 4 核，并对 `mem.events` 的 `oom_kill` 与 PIDs 撞顶单独记录（ADR-16） |
| ~~无本机容器环境，镜像无法在本机构建~~ | **已解除**（2026-10-28）：WSL2 的 docker 29.4.1 里构建成功，`--selftest` 13.954s 通过（§6 验证记录）。daemon 的代理指向死端口 7892 时不改 daemon，改用 buildx `docker-container` + `network=host` 绕开 |
| 多副本共用一个 workdir 时启动清扫误删 | 单副本是默认形态；共享存储部署必须 `-keep-orphans`，且各副本应各用一个子目录。ADR-13 第 3 节写明了为什么不用 mtime 阈值做保护 |
| 资产根里的底图被同名覆盖 / 未就位 | 服务只读、复制不改计划字节；缺图时任务失败并报出页号与路径，而不是产出空白画面（ADR-14） |

## 5. 验收

1. `go test ./internal/render/...`：底板补洞（纯色底无色差、噪声底出 warning、不改源图、按比例换算）、backend staging 产物结构（底板落盘、计划副本回填、原计划不被改）、脚本/Node 缺失时给出可操作错误、路径逃逸拒绝、MP4 容器校验。
2. `cmd/renderer -selftest -backend remotion`：2 页固定样例，走完整 staging → 渲染 → 校验链路，失败非零退出。本机实测 16.7s 通过。
3. 5 页 / 30s 样片：本机 Chrome 与容器内各出片一次，逐页 6.000s，总 900 帧；容器两次渲染字节相同。
4. 残影对照：同一帧「有/无底板」两张静帧，用像素包围盒与颜色采样确认差异。

## 6. 验证记录（2026-10-28，本机 Windows + Chrome）

| 项 | 结果 |
|---|---|
| 5 页样片渲染 | 900 帧 / 1m58s（≈7.6 fps），5 个片段各 27–44KB |
| 片段容器 | 全部 `ftypisom`，1920×1080，时长 6.000s（Chrome `loadedmetadata` 读出） |
| `-selftest`（remotion） | 通过，2 页 16.671s |
| 底板补洞 | 纯色底 spred=0、补丁色=底色；渐变底出 `plate patch may be visible` warning |
| 残影对照（t=67ms） | 无底板：标题条像素 (30,58,138) 全不透明（动画失效）；有底板：白色底 + 约 25% 不透明度叠加 |
| `chrome.sha256`（tasks 4.5） | `156.0.8078.73` / 126,938,225 字节 / `sha256=77eb8d52…`；与 `Content-Length`、GCS `x-goog-hash: md5=`、zip 结构（287 项含 209MB 可执行文件）三重一致；三次独立下载同一摘要 |

换行规则的验证用的是对照实验，不是「看着像生效」：同一次 `git checkout-index -f`
（等价于 clone 的检出路径，走 smudge 转换）下，无规则的 `README.md` 变成 `w/crlf`（81 个 CR），
带 `text eol=lf` 的 `pin-chrome.sh` 保持 `w/lf`（0 个 CR）。
注意 `git checkout -- <file>` **看不出**这个问题——它走的是不触发换行转换的恢复路径，
这条弯路值得记下来，免得下次误判成「规则没用」。

**30s 样片逐帧核对（用 Remotion 自带 ffprobe/ffmpeg，宽 960 缩略后按帧索引比对）：**

| 检查项 | 结果 |
|---|---|
| 规格 | 5 × 6.000s = 30s，各 180 帧，1920×1080 @ 30fps，共 900 帧（与 `round(ms×fps/1000)` 一致） |
| t=0 画面 | 纯白：底板已抹平全部元素，无「目标位静态副本」残影（ADR-6 生效） |
| 收敛曲线 | 与终态的平均像素差 24.73 → 12.52(100ms) → 5.26(200ms) → 0.52(433ms) → 0.08(667ms) → 0（1000ms） |
| 编排忠实度 | 修复前**不通过**：三条 bullet 声明 700/920/1140ms，实测全部在 ~400ms 随卡片出现（ADR-9 嵌套裁切 + ADR-10 `startMs` 丢失）。修复后通过：起播点 ≈800/1000/1150ms，见 ADR-9 末表 |
| 文字保真 | 未覆盖。样例底图是纯色块占位，没有文字；文字由上游「页面栅格化」决定，不在本后端职责内 |

两条结论要连着读：ADR-10 修好「偏移没有被丢掉」，ADR-9 修好「子元素不会被父层带出来」——
必须两个都在，错落编排才会真的错落。任缺其一，画面都会「看起来正常但不忠实」。

**容器内验收（tasks 5.4，2026-10-28，WSL2 docker 29.4.1 / `ppts/renderer:dev` / Chrome 156.0.8078.73）：**

提交的是与本机同一份 5 页 / 30s 计划（`planHash=81587e89…`），走 HTTP `/v1/render` 全链路
（资产根复制 → staging → Remotion → 产物校验），不是直接调 `render.mjs`。

| 检查项 | 结果 |
|---|---|
| 任务终态 | `succeeded`，progress 5/5；提交到终态 54s（本机同计划 1m29s，容器更快） |
| 片段规格 | 5 个片段均 1920×1080 / 30fps / **180 帧** / **6.000000s** / 33,641–52,579 字节 |
| t=0 画面 | 与纯白逐像素相同（非零包围盒 `None`，平均通道差 0.00）——ADR-6 底板在容器里同样生效 |
| 编排忠实度 | 起播点 title ≈167ms（声明 120）、card ≈300ms（260）、bullet0/1/2 ≈733/933/1167ms（700/920/1140），**全部落在 1 帧（33ms）内** |
| 收敛 | 与终态平均差 32.17(t=0) → 15.61(300ms) → 1.95(1000ms) → 0.01(1500ms) → 0.00(2000ms) |
| 确定性 | 同一份计划两次独立容器渲染，5 个片段 **sha256 完全相同**（`70836971…`/`f0380b17…`/`69ceb322…`/`d8c149b1…`/`a0a2956b…`） |
| 缺图 | 任务 failed 并报出页号与路径，不是产出空白片段 |
| 符号链接 | 指向容器内 `/etc/passwd` 的链接被拒（`资产是符号链接…拒绝`），job 目录无残留副本 |

**两组数字不能直接比**：上面这一轮的均值在 1920×1080 全分辨率上按三通道算，
本机那轮用的是 960 宽缩略图的单通道采样，所以「t=0 平均差 32.17 vs 24.73」不是差异，
是口径不同。可比的只有**声明值 vs 实测值**（都以帧为单位）与**同口径的两次容器运行**（字节相等）。

**这两条未完成，且不是本机可解决的：**

1. **镜像从未构建**。本机无 Docker；Chrome for Testing 的 zip 在本机代理下下载失败（curl 56），
   因此 `chrome.sha256` 没有生成，`Dockerfile` 的摘要校验分支没有被执行过。
2. ~~**干净环境 `GOWORK=off` 构建会在 `textnorm@v0.2.2` 处失败**~~ —— **2026-10-28 已修，结论见下**。

   查证过程：`sum.golang.org` 对该模块的权威记录是 `h1:IZDOaanuOcR+DCHpQvAZBlTzVOm0y+ua7OxkAdVaohw=`
   （tree txn 67923328），与实际下载的 zip 一致；`go.sum` 里写的 `h1:d3rO7+xP4Axx…` 既不是权威值、
   也不是任何一次真实下载的结果。又因 sumdb 是追加式的、记的是首次见到的摘要，**上游从未重发过内容**
   ——所以这不是「tag 被重推」的供应链问题，而是 `go.sum` 写入时就写错了。同类错误不止一处：
   v0.2.0（记 `jlXXdnN3…`，权威 `EVY0tJSe…`）、v0.2.1（记 `acBoEnv5…`，权威 `EYKjPfeS…`）同样对不上，
   说明这几个摘要从未对着发布包验证过，`GOWORK=off` 这条轨道实际**从未成功构建过**。

   修法：按 sumdb 值改写 `go.sum` 的 v0.2.2 行。修后 `go mod download` 通过、`go mod verify` 报
   `all modules verified`、`go mod tidy -diff` 无差异，`GOWORK=off go build ./...` 与
   `GOWORK=off go test ./...` 均跑通（42 包）。发布包与本地 `./textnorm` 源码逐文件比对：59 个文件
   哈希全等，仅缺仓库根的 `.gitignore`/`LICENSE`，即两条轨道内容一致，不存在版本漂移。

   连带影响：工作流顶层本来就设了 `GOWORK: "off"`，所以 `test` / `crossbuild` / `pg` / `s3` / `corpus`
   这些要构建的 job 自 2026-10-10 起应当一直是红的——这条被当作「仅 `-race` 的阻塞」记录了很久，
   实际范围大得多。

**并发验证的边界（本轮，必须说清楚）：**

`go test -race` 在本机跑不了：Windows 上 race detector 依赖 cgo，而本机 `CGO_ENABLED=0` 且没有任何
C 工具链（`gcc` / `cc` / `zig` / `clang` 均不存在）。原先还有第二层阻塞（`go.sum` 校验和，见上面第 2 条），
现已解除，因此本轮在 CI 增加了 `race` job（`.github/workflows/ci.yml`，范围为
`./internal/render/... ./cmd/renderer/... ./internal/animation/...`）。

需要如实说明的是：**本机没有 `gh`、也无法观测 GitHub Actions 的执行结果**，所以这个 job 属于
「按已验证的本地命令写成、但尚未见到绿灯」的状态，不能当作已有 race 结论。本轮能给出的仍只有两样替代物：

1. **人工审计**：逐行检查 `service.go` 的全部共享状态（`jobs` / `byHash` / `attempts` / `job.*` 字段
   一律在 `s.mu` 下读写）、`RemotionBackend.chromeOnce`（`sync.Once`）、`limitedBuffer`（由 `cmd.Wait()`
   建立 happens-before）。审计发现并修掉了 ADR-11 的三个缺陷。
2. **压力用例** `TestConcurrentSubmitCancelConsistency`：24 个不同计划并发提交（`MaxConcurrent=4`）、
   并发取消一半、再并发重复提交同一计划 8 次；断言全部落终态、成功任务的产物真的在盘上、
   取消任务的目录真的消失、信号量归零。它**不能**替代 race detector——数据竞争不会让这个用例失败，
   它只能抓到丢任务、死锁、终态与磁盘不一致、槽位泄漏这类可观测后果。

补跑方式：推一次 `main` 或在 PR 上看 `race` job；本地等价命令（需 gcc、`CGO_ENABLED=1`）为
`go test -race -count=1 ./internal/render/... ./cmd/renderer/... ./internal/animation/...`。
该 job 首次跑绿后，才可以把 M1 tasks 6.2 勾掉——在那之前它仍是「未验证」。

### ADR-24 单页降级的执行位置下沉到渲染服务（本轮落地，对应 tasks 3.7）

ADR-23（已归档的 `animation-export-e2e`）定的是「替换单元是静态段，不是跳过该页」，
但当时执行位置在**上游**：渲染服务少给一段，`app.RenderAnimation` 发现 artifacts 里没有
`segment_NNN.mp4`，就把该页记进 `Missing`，由导出侧换成静态段。

本轮把替换下沉到渲染服务内部（`renderer/degrade.mjs` + `render.mjs`），理由是上游补段
必须自带一套「静态图 → 与页时长等长的视频」的编码路径，而它手里只有导出的 page PNG，
与渲染器用的 `plateImage`（去残影底板）不是同一张图 —— 两处出来的「静态画面」会在
残影、字体、边距上不一致，且时长口径要各自维护。渲染器自己出静态段，产物是自洽的：
artifacts 齐全、时长就是计划里的 durationMs、任何调用方都能直接拼接。

**冻结一页的做法是「把每条 clip 压成零长」，不是删 clips。**
`clipProgress` 对 `durationMs <= 0` 的定义是「起点之后恒为 1」，于是任意帧取到的都是
`to` 值，即该页末帧状态。删 clips 会让 `evaluateLayer` 退回恒等（opacity=1、无位移），
对 `fadeOut` 的出场元素就会「该消失的还留着」——那不是末帧，是和成片对不上的另一张画。
转场不做处理：它属于页间视觉，且 `PageView(prev, tMs=prev.durationMs)` 取的正是上一页末帧。

**踩到的坑：只换 `inputProps` 不换 `composition` 会静默重渲原动画。**
`renderMedia` 用的是 `selectComposition` 时算好的 props，第一版就是这样：降级事件照发、
片段照出、任务 `succeeded`，唯一异常是降级页仍在动。判据只能来自帧比对 ——
第 8 帧与末帧差 **42926 像素**，差异框 `x 40–441 / y 39–123` 正好是标题条 box 加上
slideIn 的残余位移。改用 `selectComposition({inputProps: still})` 重新计算后归零
（mean 0.072、差 >8 的像素 **0**）。这条与 ADR-18 是同一个教训：**能自动判失败的东西，
必须对真实输入敏感**；「片段都在 + 任务成功」对这个缺陷完全不敏感。

**留痕链路**（降级不报错，所以必须能从外部查到）：

```
render.mjs stdout {"type":"degraded","page":1,"code":"page_render_failed"}
  → RemotionBackend.exec 收集成 RenderReport.Degraded
  → Service 写进 job，JobStatus.degradedPages（HTTP 与进程内共用一个构造点 statusOf）
  → app.RenderAnimation 记一条 warn：animation segment degraded to still (page=1, reason=…)
```

`page_skipped` 是第二道兜底：静态重试也失败时该页没有片段，上游仍按 ADR-23 换静态段。
所以下沉不是替换 ADR-23，而是让它在正常情况下不再被触发。

`Backend.Render` 因此改成返回 `(RenderReport, error)`。没有做成「可选接口」是因为
降级信息本来就是渲染结果的一部分；两条调用路径（有报告/无报告）只会让 service 里多一个
类型分支，而 stub / 测试替身本来就要实现同一个契约。

验收（容器内，`ppts/renderer:dev` + 当轮二进制）：

| 情形 | 输出 | 帧比对 |
|---|---|---|
| 不注入 | `degraded=0` | — |
| 注入第 0 页 | `degraded=1` | — |
| 注入第 1 页 | `degraded=1`，两段均 1.500s / 45 帧 | 降级页 f8 vs f42：mean 0.072、>8 像素 0；动画页 f0 vs f42：mean 25.19、96000 像素 |
