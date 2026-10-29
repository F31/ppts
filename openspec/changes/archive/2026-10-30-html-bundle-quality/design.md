# 设计：成品包质量门禁（M6-4）

## 1. 分层与职责

```
打包器（internal/share）
  ├── 自包含自检（V14）——不产出坏包
  ├── noscript 内容生成（V17 禁 JS）——模板在 Go 侧拼，JS 被禁时无法由脚本生成
  └── 体积构成（V15）——Measure() 返回分类字节数，供门禁与日志用

播放器（web/src/sharePlayer）
  └── 低内存模式（V17）——deviceMemory < 4 时不建动画层

门禁（scripts/review，需 docker + 渲染镜像）
  ├── bundle-gate.py   —— V14 扫描 + V15 预算 + V16 可播放性（CDP）
  └── bundle-degrade.py—— V17 四条件矩阵（CDP，同一驱动）
```

## 2. 数据契约

### 2.1 体积报告

```json
{
  "totalBytes": 3077771,
  "budgetBytes": 8388608,
  "over": false,
  "byKind": {
    "pages": 812345, "layers": 0, "audio": 1440444,
    "player": 15872, "subtitles": 4096, "manifest": 20480
  }
}
```

分类型而不是只报总数：总数超了无法判断该优化哪一项。`layers` 为 0 表示没有动画页，与「层图丢了」要能区分——所以另给 `animatedSlides` 计数。

### 2.2 可播放性报告（CDP 回传）

```json
{
  "mounted": true,
  "resources": { "checked": 26, "failed": [] },
  "audio": { "src": "audio/mix.wav", "durationSec": 30.0, "readyState": 4 },
  "ended": true,
  "finalTimeSec": 30.0,
  "consoleErrors": [],
  "logErrors": []
}
```

每项都是判据，不是描述：`failed` 非空即阻断，`ended` 为 false 即阻断，`durationSec` 与清单差超过 0.25 秒即阻断。

## 3. 决策

### ADR-1：探针必须是**外部脚本**，不能内联注入

第一版门禁把探针写成 `<script>...</script>` 插进 `index.html` 的 `</body>` 前，结果页面完全没反应。查 stderr 才看到：

```
Executing inline script violates the following Content Security Policy directive
'script-src 'self' file:'. Either the 'unsafe-inline' keyword, a hash, or a nonce
is required to enable inline execution.
```

M6-1 的 CSP 是 `script-src 'self' file:`，**没有** `'unsafe-inline'`——这是对的，不该为了测试放松产物。所以探针改为在**副本目录**里写一个 `probe.js`，再用 `<script src="probe.js">` 引入：相对路径符合 `'self'`，CSP 不拦。

副作用是好的：这条约束顺带证明了产物的 CSP 真的生效。以后任何「注入脚本测一测」的临时手段在这个包上都不成立，必须走外部文件。

### ADR-2：V16 用 CDP，不用 `--dump-dom` + 虚拟时间

`--virtual-time-budget` 能让 `setTimeout` 飞速跑完，适合截图（`dom-ssim.py` 就是这样定位到某一帧的）。但**媒体播放速度是真的 1x**：虚拟时间跑完 8 秒，音频才走了几十毫秒，`ended` 永远不会触发。

试过三种绕法，都不成立：

| 办法 | 结果 |
|---|---|
| `--timeout=3000 --dump-dom` | 参数不被 chrome-headless-shell 接受，dump 仍在 load 后立刻发生 |
| `playbackRate = 16` + 虚拟时间 | 虚拟时间不是被媒体阻塞就是瞬间耗尽，`ended` 时点不可控 |
| 轮询 `currentTime` | 虚拟时间下轮询次数与真实播放进度无关，无法收敛 |

改用 CDP（`--remote-debugging-port` + `ws`）：

- `Page.addScriptToEvaluateOnNewDocument` 在**播放器之前**注入 hook，才能拿到 `<audio>`——播放器把它 `createElement` 出来但没有 `append` 到 DOM，`querySelector('audio')` 永远是 null；
- `Runtime.evaluate` 带 `awaitPromise`，页面返回的 Promise 由 node 侧等它 settle，**等的是真实时间**；
- `Runtime.consoleAPICalled` / `Log.entryAdded` 收 console error 与加载失败，不需要页面自己上报。

镜像里已装 `ws`（remotion 的间接依赖），所以不新增依赖。ESM 的模块解析跟脚本位置走，脚本挂在 `/cdp` 只读目录时 `import 'ws'` 会 `ERR_MODULE_NOT_FOUND`，用 `createRequire('/app/package.json')` 把基准钉到 `/app`。

### ADR-3：自包含性在两个位置各查一次，规则共用

- **打包时**（`assertSelfContained`）：违规直接失败，不产出坏包。这时手上有全部字节，查的是「有没有把外链写进包」。
- **门禁时**（`bundle-gate.py --scan`）：扫的是**产物目录**，能发现打包器不知道的事（比如某个上游把 `index.html` 替换了、或包被人工改过）。

规则必须是同一份。Go 侧抽成 `scanExternalRefs(name string, data []byte) []string`，Python 侧照抄同一套正则，并在两边各写一条「同一份坏样本必须被拒」的用例——两份实现最容易的漂移是「一边放过了 `//cdn` 这种协议相对 URL」。

### ADR-4：noscript 内容在 Go 侧拼

禁 JS 时没有任何脚本能跑，所以「封面 + 逐页静态图 + 字幕文本」只能在打包时写死进 `index.html`。当前模板只有一句「请启用 JavaScript」——按 V17 的「均可读」判据，这是不合格的。

代价是 `index.html` 从 1 KB 涨到「字幕全文 + 页数」量级（5 分钟讲稿约 6–10 KB）。可接受：它换掉了「企业内网禁用 JS 时整个链接等于打不开」。

实现上的两个细节：

- 页图用清单里的实际文件名（`.jpg` 或 `.png`），不能假设扩展名——有动画的页是 `.png` 底板。
- 字幕按 `slideId` 聚合到对应页，不做时间轴推算：`noscript` 是给人读的，页与讲稿的对应关系比时间戳直观。

### ADR-5：低内存只判 `deviceMemory`，不判实测 fps

设计稿的矩阵写的是「`deviceMemory < 4` 或实测 fps < 24」。本轮只做前者：

- `navigator.deviceMemory` 是同步可读的，判据稳定；
- 「实测 fps」需要先跑一段动画再统计，而**统计本身要花掉用户一次播放的开头**，且判据不稳定（首帧解码抖动、后台标签页被节流都会误判）。

所以后者不做，并在 README 里写明「实测 fps 降级未实现」——不写成「已支持」。

低内存模式的行为是**不建动画层**（等同静态页 + 音频 + 字幕），不改变时长与音频。这与 reduced-motion 的区别：reduced-motion 保留 opacity 动画，低内存连 opacity 也不做，省的是合成层与解码，不只是位移。

### ADR-6：体积预算用「基数 + 每分钟」，不用单一阈值

单一阈值（如 8 MB）对 30 秒和 20 分钟一样严，前者永远通过、后者永远失败。方案 §20.6 给的是 5 分钟 1080p 的预算表：

| 组成 | 5 分钟预算 |
|---|---|
| 字体子集 | 40–90 KB |
| 页图 20 页 | ~1.6 MB |
| 音频 5 分钟 | ~2.4 MB |
| 动画数据 | 30–120 KB |
| 播放器 + 清单 | ~50 KB |
| **合计** | **≈4.2 MB** |

换算成可判形式：

```
budget = 3 MiB（播放器 + 20 页图 + 清单）
       + 0.55 MiB / 分钟（音频 AAC 64kbps 单声道 ≈ 0.48 MiB/min，留 15% 余量）
```

5 分钟 → 3 + 2.75 = 5.75 MiB，比方案预算宽 37%——因为**当前没做字体子集与 WebP**，包会比你理想值大。阈值取「当前实现的合理上界」而不是「目标值」，否则门禁第一天就永久红，然后被人关掉。

超预算**告警不阻断**：V15 在方案里是阻断，但它依赖的优化手段（字体子集、WebP、AAC）都还没做，此时阻断等于用门禁替产品做决定。等优化落地后再升级为阻断。

### ADR-7：降级矩阵用同一套 CDP 驱动，判定「可读」而不是「没崩」

四条路径的判据分别是：

| 条件 | 怎么造 | 判据 |
|---|---|---|
| 禁 JS | CDP `Emulation.setScriptExecutionDisabled(true)` | `noscript` 里出现 ≥ 页数的 `<img>`、且每张图的 `naturalWidth > 0`、字幕文本非空 |
| 无 WAAPI | 注入时置 `Element.prototype.animate = undefined` | 所有 `.ppts-layer` 的 `transform` 为 `none` 且 `opacity` 为 `1`（终态可见，不是停在 0） |
| reduced-motion | `Emulation.setEmulatedMedia({features:[{name:'prefers-reduced-motion',value:'reduce'}]})` | 层有 `opacity` 但无位移；`animation.effect.getTiming().duration` 与默认态相同 |
| 低内存 | 注入时把 `navigator.deviceMemory` 改成 2 | `.ppts-layer` 数量为 0，页面仍渲染出当前页 |

`Emulation.setEmulatedMedia` 比 `--force-prefers-reduced-motion` 更好用：后者是进程级开关，一次只能测一种形态，跑矩阵要起四次浏览器。

## 4. 边界与失败

| 情况 | 行为 |
|---|---|
| 包里没有音频（静音片） | V16 跳过 audio 段判据并标注 `audio: skipped`，不判失败 |
| 清单里 `audioMix` 缺失但 `audio[]` 非空 | 走逐段播放路径，`ended` 判据改为「最后一段 ended」 |
| 资源预加载超时（慢盘） | 单资源 8 秒；超时记 `failed: timeout`，与 404 分开报 |
| docker 不可用 | 门禁**跳过**并 exit 2（不是 0）：跳过必须在 CI 里显形，不能装成通过 |
| 渲染镜像缺 `ws` | 同上，明确报「镜像版本过旧」而不是 `ERR_MODULE_NOT_FOUND` 堆栈 |
| 门禁对旧包跑到一半 | 探针只写临时副本，**从不改原包**；原包 mtime 与哈希在门禁前后比对，不一致即失败 |
