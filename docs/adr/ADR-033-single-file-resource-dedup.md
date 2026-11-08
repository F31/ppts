# ADR-033 单文件内联按资源去重（token 表 + 同脚本还原）

- 状态：已实现
- 日期：2026-11-08
- 关联：ADR-029 ⑤（单文件形态）、ADR-032（AAC 混音轨）、V14（自包含）、V16/V17（可播放/降级矩阵）

## 背景

`BuildSingle` 从 M6 起的实现只有一句话：把包内**路径**在文本条目里换成 data URI。
路径在清单里从来不是唯一出现的：

- 混音轨模式下 `audio[].src` 每一段都指向同一条 mix（30 段 = 30 次引用），`audioMix` 再指一次；
- `slides[i].image` 与 `slides[i].shots[0].image` 是同一个文件（首镜复用页级图）；
- 层图在 `layers[].image` 里可能被多页复用。

于是同一份数据被写 N 遍。实测 5 分钟 / 30 页（WAV 混音轨，`cmd/share-baseline` 默认样本）：

| 版本 | 单文件 | zip | 单文件 / 解压后 |
|---|---:|---:|---:|
| 逐处替换（改前，WAV） | 618 MB | 1.1 MB | 40.9× |
| 资源去重（改后，WAV） | 21.9 MB | 1.1 MB | 1.45× |
| 资源去重（改后，AAC 64k） | 6.68 MB | 2.5 MB | 1.80× |

理论下限是 15.12 MiB × 1.33 = 20.1 MB（base64）+ `<noscript>` 页图那一份，
所以改后基本贴着下限。AAC 那条按 3.72 MiB 解压后算同类下限约 5.0 MB，
实测 6.68 MB——多出来的仍是 `<noscript>` 页图那一份。
这个坑在单元测试里长期是绿的，因为夹具只有 2 段：
判据必须按「同一资源的引用次数」写，而不是按「有没有内联」写。

## 决策

1. **清单脚本用短 token 占位**（`@@ppts-res-N@@`），资源表只写一份，紧跟在
   `window.__PPT_MANIFEST__ = {...}` 之后、**同一个 `<script>`** 内做还原。
   为什么必须同脚本：`app.js` 是后加载的独立脚本，只认全局清单；还原晚一步，
   第一帧就会拿 token 当资源路径去加载（现场是黑屏 + 几个 404，而 `file://` 下
   连 404 都看不到）。
2. **还原用深遍历**，不按字段名逐个处理：清单里出现资源路径的字段已有五处
   （`slides[].image`、`shots[].image`、`layers[].image`、`audio[].src`、`audioMix`），
   以后加字段不该要求同步改这里——漏改的现场是"那一层不显示"。
3. **资源表是 IIFE 局部变量**，不挂 `window`：19 MB 数据若再被一个全局名引用住，
   等于白省一份内存（用完即弃，GC 能收）。
4. **`<noscript>` 保持完整 data URI**：禁 JS 时没有任何脚本能还原 token。
   那里每页一份，是**不同用途**的第二份，不是重复内联。
5. **单文件形态的禁 JS 提示改口**（"音频与字幕已内嵌在本文件里"）。单文件里不存在
   "同目录文件"，照旧写 `subtitles.vtt` / `audio/mix.wav` 会让用户去找一个不存在的东西。
   为此 `BundleInput` 加了一个**不导出**的 `singleFile` 位：它不是调用方的选择，
   而是"这一次组装会被内联"这一事实，影响面只有那一句提示——条目集合、清单、
   降级语义一律不变，两种形态仍然同源。

## 取舍

- **为什么不把 `<noscript>` 也 token 化**：没有脚本就没有还原者。要么删掉禁 JS 的逐页画面
  （违背 ADR-4「至少能读」），要么认下每页两份。认下。
- **为什么不改清单结构**（例如让所有 `audio[].src` 直接指向 `audioMix` 就完事）：
  清单是交换格式，逐段 `src` 的语义是"这段讲稿在混音轨上的区间"，语义靠 `startUs/endUs`
  表达；为省字节动清单结构，会让 zip 与单文件的清单不再逐字节可比（ADR-029 ⑤ 的等价关系）。
- **文本条目名仍留在清单里**（`subtitles.src` = `subtitles.vtt` 等）。它们不是坏链：
  播放器读的是 `subtitles.inline`。所以残留检查只管二进制条目——把它们也抹掉反而
  破坏"清单逐字节一致"。

## 验证

- `TestBuildSingleInlinesSharedResourceOnce`：多段共用一条混音轨时，全文只允许 **1** 份
  音频数据；页图恰好 **2 × 页数**（清单 + noscript）。
- `TestBuildSingleManifestUsesTokenTableInSameScript`：引用点 → 资源表 → 还原的顺序，
  且三者不跨 `<script>`。
- `TestBuildSingleNoscriptStaysReadable`：noscript 页图齐全、提示不再指向不存在的文件；
  目录包仍给出真实文件名（两种形态的差异只有这一句）。
- 真实浏览器（Chrome CDP，`bundle-probe.mjs` 直接指向 22 MB 单文件）：
  - `--mode play`：挂载成功、去重后的 4 个资源全部加载、音频 300s 与清单一致、播到结尾，
    0 page error / 0 console error；
  - `--mode nojs`：30 张页图全部 `naturalWidth > 0`，提示文案为"已内嵌在本文件里"。
  - AAC 路径（WSL 里带真实 ffmpeg 生成，6.68 MB）同样全部通过：
    音频 `data:audio/mp4` 时长 300s、`ended` 由媒体事件触发。
- 顺带修了 probe 三处单文件适配（data URI 的音频判定、失败项与图片名截断）：
  否则 19 MB 的 data URI 原样回传会把 CDP 消息撑爆，现场表现是 probe **卡死**而不是报错
  （本次就是这么卡了 4 分钟才定位到的）。

## 遗留

- 页图在单文件里恒为两份（清单 + noscript）。真实素材（150–400 KB/页）下这部分会到
  10–20 MB，是单文件体积的主要成本。

## 后续（2026-11-08 补）

单文件已进入 V14/V16/V17 的自动化门禁：

```
python3 scripts/review/bundle-gate.py --single tmp/m64/index.html
```

- `cmd/share-verify -single` 提供 V14（外链扫描）与体积；它**不做**清单结构校验——
  单文件里的清单是 token 化的，token 的还原只发生在浏览器里，在 Go 侧再写一个反解器
  只是多一处会与打包器一起漂移的实现。
- 页数/时长/音频段数由探针从**还原后的** `window.__PPT_MANIFEST__` 取（`out.manifest`），
  所以门禁读到的是用户真正拿到的那份清单，而不是打包器的记账。
- V15 的体积预算不套用于单文件（它天然是解压后 × 1.33 + noscript 页图），只报体积；
  字体覆盖率同样标 `not_applicable`——字体已 base64 内联进 `<style>`，没有独立条目可解析。
- 实测（3 页 / 12s / 2 层，1.09 MiB）：23 项判据全 PASS，含单文件专属的两处：
  音频以 `data:audio/wav` 参与「时长与清单一致」、V17 六条降级判据。
- 反例可红：给单文件塞一条 `<link href="https://cdn...">`，`--skip-browser` 即退出 1。
