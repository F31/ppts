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

#### 已知限制（登记为 tasks 3.14）

- **重启即丢索引**：`jobs` 只在内存，进程重启后 `/work` 下的目录成为孤儿，没有任何记录能回收它们。
  彻底解决要持久化索引（或启动时扫描按 mtime 清理），在「服务无状态」的定位下需要单独评审。
- **`queuedLocked` 是 O(存活 job 数) 扫描**：TTL 内的 job 数量有限（默认 30 分钟窗口），实测可忽略；
  若未来 TTL 拉长到数小时，应改为维护计数器。

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
| 编排忠实度 | 修复前**不通过**：三条 bullet 声明 700/920/1140ms，实测全部在 ~400ms 随卡片出现（ADR-9 嵌套裁切 + ADR-10 `startMs` 丢失）。修复后通过：起播点 ≈800/1000/1150ms，见 ADR-9 末表 |
| 文字保真 | 未覆盖。样例底图是纯色块占位，没有文字；文字由上游「页面栅格化」决定，不在本后端职责内 |

两条结论要连着读：ADR-10 修好「偏移没有被丢掉」，ADR-9 修好「子元素不会被父层带出来」——
必须两个都在，错落编排才会真的错落。任缺其一，画面都会「看起来正常但不忠实」。

**这两条未完成，且不是本机可解决的：**

1. **镜像从未构建**。本机无 Docker；Chrome for Testing 的 zip 在本机代理下下载失败（curl 56），
   因此 `chrome.sha256` 没有生成，`Dockerfile` 的摘要校验分支没有被执行过。
2. **干净环境 `GOWORK=off` 构建会在 `textnorm@v0.2.2` 处失败**：`go mod download` 报
   `checksum mismatch`（下载到 h1:IZDO…，`go.sum` 记的是 h1:d3rO…）。本机构建成功是因为模块已在
   本地缓存。这条不修，镜像与 CI 的 `-race` 都过不去；修它要动 `go.sum`，属于模块所有者的决定。

**并发验证的边界（本轮，必须说清楚）：**

`go test -race` 在本机跑不了：Windows 上 race detector 依赖 cgo，而本机 `CGO_ENABLED=0` 且没有任何
C 工具链（`gcc` / `cc` / `zig` / `clang` 均不存在）。CI 侧的 `-race` 也仍被上面第 2 条（`textnorm` 的
`go.sum` 校验和）挡着。因此本轮**没有** race detector 的结论，只有两样替代物：

1. **人工审计**：逐行检查 `service.go` 的全部共享状态（`jobs` / `byHash` / `attempts` / `job.*` 字段
   一律在 `s.mu` 下读写）、`RemotionBackend.chromeOnce`（`sync.Once`）、`limitedBuffer`（由 `cmd.Wait()`
   建立 happens-before）。审计发现并修掉了 ADR-11 的三个缺陷。
2. **压力用例** `TestConcurrentSubmitCancelConsistency`：24 个不同计划并发提交（`MaxConcurrent=4`）、
   并发取消一半、再并发重复提交同一计划 8 次；断言全部落终态、成功任务的产物真的在盘上、
   取消任务的目录真的消失、信号量归零。它**不能**替代 race detector——数据竞争不会让这个用例失败，
   它只能抓到丢任务、死锁、终态与磁盘不一致、槽位泄漏这类可观测后果。

补跑方式：在装有 gcc 的 Linux/macOS 或 CI 上执行
`go test -race ./internal/render/... ./cmd/renderer/... ./internal/animation/...`。
