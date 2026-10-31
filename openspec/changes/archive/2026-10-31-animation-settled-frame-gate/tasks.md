# 任务：终态帧门禁（V13b）

## 1. 复用层

- [x] 1.1 `scripts/review/cdp.mjs`：浏览器候选表 + 拉起 + CDP 连接，只此一份
- [x] 1.2 `bundle-probe.mjs` 改为 import 它（去掉自带候选表、`openSocket`、端口与 profile 管理）
- [x] 1.3 复跑 `bundle-gate.py --engine native`：21 条判据仍全部 PASS（重构没有改变判据）

## 2. 取帧

- [x] 2.1 `scripts/review/dom-shot.mjs`：按时刻 seek（播放器自己的滑杆）→ 等图解码 → 截图
- [x] 2.2 落点回读（`actualMs`）与舞台矩形（`stage{x,y,w,h}`）随结果返回，供判据使用
- [x] 2.3 `Emulation.setDeviceMetricsOverride` 固定视口：`--window-size` 给的是窗口，实测 1920×1080 会被扣成 1898×982
- [x] 2.4 截图前摘掉封面与字幕（`remove()` 而非 `hidden`）：seek 与迟到的音频元数据都会把它们重新显示出来，实测同一份包两次运行给出 0.9999 / 0.9676 两种结果

## 3. 判据

- [x] 3.1 `scripts/review/settled-gate.py`：逐页取页尾前 250ms，与源页图比 SSIM / 平均差 / 结构差异占比
- [x] 3.2 落点偏差 >50ms 判失败（定位失效时比对无意义）
- [x] 3.3 舞台与源页尺寸不一致按**输入问题**退出（2），不报「不合格」——那是参数错，不是产品错
- [x] 3.4 阈值按正确包实测值留一个数量级余量（0.995 / 0.5 / 0.05%）

## 4. 夹具

- [x] 4.1 `share-baseline -layers N`：页图在同一批框内画出层色，层图即该框的裁片
- [x] 4.2 `share-baseline -pages-out DIR`：落盘本次使用的页图（合成或 `-pages-dir` 读入）
- [x] 4.3 页图/层图共用 `layerBox` / `layerColor`，两处不再各算一遍
- [x] 4.4 单测 `TestSyntheticFixtureComposesToPage`：页图框内颜色 == 层色，且底板同位置**没有**该框

## 5. 两个真问题

- [x] 5.1 重新生成 `internal/share/assets/{app.js,app.css}`（产物落后源码两个提交）
- [x] 5.2 `web/src/sharePlayer/player.css`：`#ppts-share-root { display: contents }`，播放器按原意铺满（上限 1280）
- [x] 5.3 实测：修复前舞台 492×344，修复后 1280×720

## 6. CI

- [x] 6.1 `share-browser` 作业新增「终态帧门禁（V13b）」步骤：`-width 1280 -height 720 -pages-out` 造夹 → 跑门禁
- [x] 6.2 `.gitattributes`：`internal/share/assets/*` 固定 `eol=lf`（重建产物在 Windows 上不再整文件抖动）

## 7. 文档

- [x] 7.1 规格增量：`html-share-quality` 新增「终态帧与源页一致」「播放器产物由源码生成」两条
- [x] 7.2 路线图：覆盖表把 V13b 从「无」改为「CI」；变更表加入本轮
- [x] 7.3 `README.md` 门禁清单加入 V13b
