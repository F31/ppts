# 成品包质量门禁（M6-4）

M6-1 让成品包能播，M6-2 让 DOM 与 MP4 同帧一致，M6-3 让成品能分享。三者各有验证，
但「包本身有没有坏」此前只靠人打开看。本变更把这条空档补成四条可执行判据。

## 一条命令

```bash
python3 scripts/review/bundle-gate.py --pkg tmp/m64/pkg
```

输出一张判据表，退出码：`0` 全过、`1` 有阻断失败、`2` 环境缺失（docker/镜像/ws）。

没有 docker 时可以只跑 V14/V15：

```bash
python3 scripts/review/bundle-gate.py --pkg tmp/m64/pkg --skip-browser
```

单看体积与外链，也有更轻的入口：

```bash
go run ./cmd/share-verify tmp/m64/pkg          # 人类可读，含类别构成表
go run ./cmd/share-verify -json --budget tmp/m64/pkg
```

## 四项判据

| 编号 | 判据 | 阻断 | 实测（5 页 / 30 秒 / 19 层） |
|---|---|---|---|
| V14 | 包内 `src`/`href`/`url()`/`@import` 的外部引用数为 0 | 是 | 0 处 |
| V15 | 体积 ≤ `3 MiB + 0.55 MiB/分钟`；给出类别构成 | 否（告警） | 2.05 MiB / 预算 3.28 MiB |
| V16 | 挂载成功、清单资源全可加载、音频时长一致、播到结尾、无 console error | 是 | 25 个资源全加载，30s = 30s |
| V17 | 禁 JS / 无 WAAPI / reduced-motion / 低内存四条降级路径 | 是 | 21 条判据全 PASS |

V13（同帧 SSIM）在 M6-2 已完成，用 `scripts/review/dom-ssim.py` 单独跑，逐页最差 0.99357。

## V15 的预算怎么来的

方案 §20.6 给的是 5 分钟 1080p 的构成表，合计约 4.2 MB。换算成可判形式：

```
budget = 3 MiB（播放器 + 20 页图 + 清单） + 0.55 MiB/分钟（AAC 64k 单声道 ≈0.48，留 15% 余量）
```

5 分钟 → 5.75 MiB，比目标宽 37%。**这是故意的**：字体子集、WebP、AAC 都还没做，
按目标值设阈值会让门禁第一天就永久红，然后被人关掉。等这些优化落地后，这个常量要跟着降。

超预算只**告警**不阻断，理由同上——此刻阻断等于用门禁替产品做决定。

## 门禁实现里的三个坑

写脚本时撞到的，每个都会让门禁悄悄失效：

**探针不能用内联脚本。** 产物 CSP 是 `script-src 'self' file:`，没有 `'unsafe-inline'`。
第一版往 `index.html` 里插 `<script>` 完全没有反应（控制台报 CSP 违规）。
改用 CDP 的 `Page.addScriptToEvaluateOnNewDocument`——它走 DevTools 特权上下文，
不受页面 CSP 约束，而且**原包一个字节都不用改**。

**虚拟时间与 1x 媒体播放不可调和。** `--virtual-time-budget` 能让 `setTimeout` 飞速跑完
（`dom-ssim.py` 靠它定位某一帧），但音频播放速度是真的 1x。试过三条绕路都不成立，
最终用 CDP 的 `Runtime.evaluate` + `awaitPromise`，它等的是真实时间。

**层样式必须按「每页开场 400ms」采样。** 按全片 10/30/50/70/90% 取点时，
默认态所有层都是 identity + opacity 1——本项目的入场动画都在页首 100–800ms 内完成，
页中往后早已停在终态。在那种时点做 reduced-motion 对照，会得出「默认态也没有位移」
的假象，门禁等于没验。

## 降级矩阵的四条判据

| 条件 | 怎么造 | 实测 |
|---|---|---|
| 禁 JS | `Emulation.setScriptExecutionDisabled` | 5 张 noscript 图全加载（naturalWidth 1920）、5 段讲稿、stage 未创建 |
| 无 WAAPI | 注入时置 `Element.prototype.animate = undefined` | 19 层 `transform:none`、`opacity:1`（终态可见） |
| reduced-motion | `Emulation.setEmulatedMedia` | 全部 identity；opacity 仍有动画（0.618 / 0.668 / 0.968）；duration 两态同为 6000ms |
| 低内存 | 注入 `navigator.deviceMemory = 2` | 5 个取样点层数均为 0，stage 仍存在 |

「禁 JS 可读」是本轮补的实现：原来 `<noscript>` 只有一句「请启用 JavaScript」，
企业内网禁用脚本时整个链接等于打不开。现在打包时把「标题 + 逐页静态图 + 讲稿」
写死进 `index.html`——禁 JS 时没有任何脚本能补内容，只能这么做。

## 顺带修掉的体积缺陷

门禁第一次跑就超预算（3.42 / 3.28 MiB），构成表指出**音频占 80%**。
原因是包里同时存了逐段音频和混音轨，而播放器在 mix 模式下根本不读 `audio[].src`
（时钟直接来自 mix 的 `currentTime`）——逐段文件是纯冗余。

改为拼成 mix 时不再写逐段文件，`audio[].src` 统一指向 mix；拼不成时仍退回逐段。
**3.42 → 2.05 MiB（-40%）**，音频 2.75 → 1.37 MiB。

## 没做的部分

| 项 | 为什么 |
|---|---|
| V18 埋点（首帧曝光、25/50/75/100% 进度、来源上报） | 需先定「哪些算一次观看」「隐私模式下关到什么程度」，属产品口径 |
| 字体子集化 / WebP / AAC | 字体要有 OFL 资产与许可声明，WebP 要确认浏览器矩阵，AAC 要引编码器。当前包不内嵌字体（系统字体栈），所以「字体子集覆盖率」判据在本实现下**不成立**——报告里标 `not_applicable`，不记 pass |
| 实测 fps < 24 的降级 | 要先跑一段动画再统计，统计本身花掉用户一次播放的开头，且后台标签页被节流会误判。只做 `deviceMemory < 4` |
| 移动端真机矩阵 | 无头 Chrome 覆盖不到 iOS Safari 的自动播放策略 |
| 门禁进 CI 必跑集 | 依赖 docker + 渲染镜像，与 `dom-ssim.py` 一致，发布前手跑 |

## 验收包怎么造

`tmp/m64gen/main.go`（临时，不入库）：读 M6-2 验收包的页图与层数据，配 5 段合成音频，
用 `share.BuildLocalToDir` 生成一个「5 页 × 6 秒、19 层、带混音轨」的包。
换一台机器重跑门禁时按同样思路生成即可——门禁本身不依赖这个包，只要求包里有
音频、有层、有转场。

## 回滚

三条提交各自独立：

| commit | 内容 | 回滚影响 |
|---|---|---|
| `03dc6e4` | 外链自检、体积构成、音频去重 | 包体积回到 3.42 MiB；外链只能在事后 CLI 查到 |
| `93b56eb` | 禁 JS 可读、低内存降级 | 禁 JS 退回一句话提示 |
| `e59529f` | 门禁脚本 | 只删工具，不影响产物 |
