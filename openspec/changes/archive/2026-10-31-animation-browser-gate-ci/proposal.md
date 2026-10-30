# 提案：把成品包的浏览器门禁（V16/V17）搬进 CI

## 为什么做

`html-share-quality` 写得很清楚：V16「可播放性由真实浏览器验证」、V17「降级矩阵逐条可执行」。四条判据也确实存在，也确实抓到过真实缺陷（混音轨与逐段音频双份，音频占 80%）。

但它们**只在本地跑得起来**，原因不是判据不对，是跑法绑死了环境：

- `bundle-probe.mjs` 把 Chrome 钉在 `/opt/chrome/chrome-headless-shell-linux64/...`；
- 它 `createRequire('/app/package.json')` 去要镜像里的 `ws`；
- `bundle-gate.py` 只会 `docker run`，没有镜像就没有第二条路；
- `file://` 地址用 `path.posix.join` 拼，换个平台连 URL 都不合法。

于是「包在真实浏览器里能不能播」这件事，在合并前**没有任何人验过**。而这恰好是唯一一类读代码看不出来的问题：删掉一张清单引用的层图，页面照样"看起来正常"，只是那一层没了；reduced-motion 分支写错，只用默认设置看片的人永远发现不了。

## 目标

1. **探针与环境解耦**：浏览器按 `PPT_CHROME` → 平台常见路径 → 镜像路径的顺序自行解析；候选表只写在探针一处，门禁通过 `--print-chrome` 问它，不各写一份。
2. **不再依赖 `ws`**：Node 22 自带 WebSocket（undici）足够跑 CDP；镜像里的旧 Node 仍退回 `ws`，两条路合成同一组接口。
3. **门禁可换引擎**：`--engine auto|native|docker`，`auto` 先试本机 Chrome，再退镜像。
4. **样例包真有图层**：`share-baseline -layers N` 产出带 `clips` 的分层素材——否则 V17 的判据没有可测对象（实测：无图层的包跑门禁会红四项）。
5. **进 CI**：新增 `share-browser` 作业，每个 push / PR 跑一遍 V14–V17。

## 非目标

- 不动 V16/V17 的判据本身。本轮只改「在哪跑、拿什么跑」，判据一条没松。
- 不把 `dom-ssim.py` / `still-check.py` 搬进 CI：它们要真实成片（MP4 + 逐页截图），仍依赖渲染镜像，属另一件事。
- 不改探针的采样口径（页首 +400ms）。合成动画时长从 520ms 调到 900ms 是为了让取样落在动画中段，不是改判据。
- 不做浏览器版本固定：CI 用 runner 自带的 Chrome/Edge；像素级一致性归渲染镜像（`renderer-image` 的 Chrome 版本进 planHash），两件事不混。

## 与既有变更的关系

- 依赖 `html-bundle-quality`（已归档）的 V14–V17 判据与 `cmd/share-verify`；
- 与 `animation-render-service-image`（已归档）共用「镜像内也能跑」这条要求：改完两边都能跑，镜像路径不是唯一路径。
