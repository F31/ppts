# 任务：成品包质量门禁（M6-4）

> 判据统一写在每条任务里；没有实测值的勾不算数。

## 1. 降级实现（V17 的前置）

- [x] 1.1 `index.html` 的 `noscript` 升级为「标题 + 逐页静态图 + 对应讲稿」：
      页图用清单里的真实文件名（`.jpg` / `.png` 不假设），字幕按 `slideId` 聚合。
      实测：5 页包生成 5 个 `<img>` + 5 段讲稿
- [x] 1.2 低内存模式：`navigator.deviceMemory < 4` 时不建动画层（等同静态页 + 音频 + 字幕）。
      实测：注入 `deviceMemory=2` 后 5 个取样点层数均为 0，stage 仍存在
- [x] 1.3 `app.css` 补 `.ppts-noscript` 版式。挂在 `position: fixed; inset: 0` 上——
      禁 JS 时 body 拿不到 `.ppts-share-page` 类，默认白底与 8px margin 会从四周露出来
- [x] 1.4 用例 4 条：noscript 含 2 页图 + 合并讲稿、不含 `loading="lazy"`、
      无讲稿页有占位、标题在 noscript 内同样被转义

## 2. 自包含性（V14）

- [x] 2.1 `internal/share/selfcontain.go`：`ScanExternalRefs(name, data)` 扫
      `src=` / `href=` / `url()` / `@import`，拒 `http://`、`https://`、`//`、`file:`、以 `/` 开头。
      返回**片段**而不只是计数——只报数量时排查要从头 grep
- [x] 2.2 打包时集成：挂在 `emit` 唯一出口上自检，违规即失败并报条目 + 片段。
      挂出口而不是逐条目检查：以后加 `.woff2` 或新 css 不需要记得再注册一次扫描
- [x] 2.3 用例 16 条：13 条分类（含协议相对 `//host`、`file:`、根绝对、`data:`、JS 字符串字面量）、
      二进制不扫、去重、打包拒绝（错误信息含违规片段）
- [x] 2.4 判据统一：`CountExternalRefs` 改为汇总 `ScanExternalRefs` 的结果。
      原来的正则只认 `http(s)://` 与 `//`，`/static/x.png` 能溜过去（file:// 下必然 404）

## 3. 体积构成（V15）

- [x] 3.1 `MeasureFiles(files, manifest)`：页图 / 层图 / 音频 / 播放器 / 字幕 / 清单 / 其它，
      另返回 `pages` / `layers` / `animatedSlides`（层图丢了与"这页本来没动画"在体积上无法区分）
- [x] 3.2 预算公式 `3 MiB + 0.55 MiB/分钟`；`share-verify -budget` 超预算 exit 3
- [x] 3.3 报告里 `字体子集：not_applicable`（当前不内嵌字体），不记 pass
- [x] 3.4 实测（5 页 / 30 秒 / 19 层）：2.05 MiB，预算 3.28 MiB。
      构成：音频 1.37 MiB（67%）、层图 0.37（18%）、页图 0.26（13%）、其余 <2%
- [x] 3.5 **顺带修掉的缺陷**：混音轨 + 逐段音频双份 → 只写混音轨。
      3.42 MiB → 2.05 MiB（-40%）。拼不成 mix 时仍退回逐段

## 4. 可播放性（V16，CDP）

- [x] 4.1 `scripts/review/bundle-probe.mjs`：自起 chrome、`Target.attachToTarget`（flatten）、
      `Page.addScriptToEvaluateOnNewDocument` 注入 media hook、`Runtime.evaluate` + `awaitPromise`
- [x] 4.2 判据：挂载、25 个资源全可加载、`duration` 30s 与清单一致、
      播到结尾（`ended` 或 `reached-end`）、console error 为空、log error 为空、页面 error 为空
- [x] 4.3 `scripts/review/bundle-gate.py`：V14/V15 走 `share-verify`，V16/V17 走探针；
      结构不自洽时给出可读失败而不是 traceback
- [x] 4.4 实测：`mounted=true`、`resources.checked=25 failed=[]`、
      `audio=mix.wav duration=30s`、`ended`、`consoleErrors=[]`
- [x] 4.5 反例：删掉 `slides/003-layer-00.png` →
      `slide 2 layer 0 image "slides/003-layer-00.png" is missing from bundle`

## 5. 降级矩阵门禁（V17）

- [x] 5.1 `bundle-gate.py` 一轮跑完五种形态（play / nojs / default / reduced / nowaapi / lowmem）
- [x] 5.2 禁 JS 用 `Emulation.setScriptExecutionDisabled`：5 张图 `naturalWidth=1920`、
      5 段讲稿、stage 未创建
- [x] 5.3 无 WAAPI：19 层 `transform:none`、`opacity:1`
- [x] 5.4 reduced-motion 用 `Emulation.setEmulatedMedia`：全部 identity、
      opacity 仍有动画（0.618/0.668/0.968）、duration 两态同为 6000ms
- [x] 5.5 低内存：5 个取样点层数均为 0 且 stage 存在
- [x] 5.6 **对照基准**：采样点必须按「每页 startUs+400ms」。按全片百分比取点时
      所有层都在终态（本项目的入场动画都在页首 100–800ms 内完成），
      reduced-motion 的对照会得出"默认态也没有位移"的假象。改后默认态每页 1 层有位移
      （translateY 0.096–0.114px）

## 6. 收口

- [x] 6.1 变更自带 README：命令、四项判据与实测值、预算公式的由来、三个坑、
      未做项（V18 埋点 / 字体子集 / WebP / AAC / 实测 fps / 移动端真机）、回滚
- [x] 6.2 路线图进度表与计划表更新 M6-4 状态
- [x] 6.3 `openspec validate --all` 通过后归档，delta 吸收进 `openspec/specs/html-share-quality`
