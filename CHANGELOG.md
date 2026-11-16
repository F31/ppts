# 变更说明

## Unreleased

注视引导（B + 薄 C）成为新一键片的默认运动。已保存的入场规格再导出仍走入场；换风格或「应用到全部页面」才切到注视。

### 行为变化

- 新默认是整页静图 + 旁白时钟上的 spotlight / 圈选 / 压暗，不再为每个要点发射 fadeIn。教学页走 look 链（每条注视一帧 + ffmpeg hold），不按页时长进 Chrome。
- 「要点依次出现」是显式开关：打开后才挖洞、编入场层；包围盒不可靠时本页回退纯注视，并留下 `entrance_skipped`。一键成片与「应用到全部页面」把该勾选发给 apply-all，由 catalog `EntranceClips` 写入 `gaze+entrance`，不必再改主机 JSON。`ppts apply-animation --source-revision 0` 与缺省查询参数一样落到项目 `current_revision`，不再写 src=0 行。
- 科技预设 `tech-dynamic` 对当前条做 LookCrop punch-in（裁切铺满，不是 Ken Burns）。教学页忽略该标记。
- 台上短标签只出现在预览与 HTML 分享页；导出 MP4 不烧台上 caption（ASS/VTT 仍是全文）。
- 已保存的 fadeIn 规格像素不变。发布后不必设新环境变量。
- 注视圈选扩到最小卡片底（空容器）。同一张卡上的标题/正文按矩形去重为一条 look，条数减少时按阅读序重铺时间。无卡片底的页仍用元素盒 + 8px。
- 注视圈选按源页 `sldSz` 换算。Office 默认 16:9（10"×5.625"）不再按 7.5 英寸页高缩放，圈选贴在画出来的卡片上。旧 extracted.json 没有页尺寸时导出/预览只读源 PPTX 的 `presentation.xml`，不重跑解析。
- 教学注视的 look 时钟跟旁白走：按句读或 VAD 停顿切拍，字面命中的形状才成孔。页脚页码按命名规则剔除。有效拍不足或结构化总起句盖住整页时回退均分（仍不圈页脚）。已保存的入场规格不跑拍表。
- 第一 look 晚于页起点时，前缀仍帧取该窗中点，不再采页边界上上一页的末帧。
- 解析页图 DPI 按 `sldSz` 对齐到成片长边 ≥1920（Office 默认 10" 16:9 为 192 DPI）。已有页图需重新解析才会变清晰。
- 「要点依次出现」（`gaze+entrance`）在教学注视页上仍走 look 链：挖洞后按 look 中点仍帧，240ms xfade 承担显现，不把整页丢进 Chrome。包围盒不可靠仍回退纯注视。已保存的纯 `entrance` 规格仍整页合成。
- 教学注视页每页一次 ffmpeg 图（PNG xfade + concat + ultrafast / CRF 18），不再对每条 look 先 medium 再整页 medium。几何与 240ms easeOutCubic 不变。片段键 `looks=once`。

### 质量门禁

- V13（HTML vs MP4）注视 look 链页（含带 Focus.rect 的 `gaze+entrance`）取样每条 Focus 稳态中点，避开 240ms 交叉和页间 fade 窗口，不取 0ms。探针摘掉 `.ppts-caption`。
- V13b 注视页对照末 look（带纱），不对源页 PNG 做全帧 0.98。
- V13c 孔内矩形 vs 源页局部 SSIM ≥ 0.97。
- 观测：`ppts_animation_render_duration_ms_total{path=gaze_looks|chrome|still}`；教学导出 chrome 占 gaze+chrome 持续 > 20% 告警。
- 30 页教学 deck、一键成片、1080p30 的墙钟目标 18 min 以金丝雀为准，本版本不把该 SLO 写成硬门禁。

### 回滚

还原 catalog 默认即可回到入场生成。已保存的注视规格再导出仍是注视。

## v1.0.0

首个稳定版本。从更早的构建升级时，先跑 `ppts migrate`，再核对下面四条。

### 行为变化

- 上传与解析共用 100 MiB（`PPTS_MAX_UPLOAD_BYTES=104857600`，页数 `PPTS_MAX_PARSE_PAGES=100`）。此前 API 放到 512 MiB，解析阶段才失败。网页一直是 100 MiB，只改环境变量不会放宽浏览器检查。
- 布尔环境变量认 `1/true/yes/on` 与 `0/false/no/off`。已经写成 `PPTS_AUTH_DEV_HEADERS=1` 的环境，升级后会接受未签名身份头。生产保持不设置或 `false`。
- 终态任务默认保留 30 天（`PPTS_JOB_RETENTION_TTL=720h`）。到期删除会放开 `UNIQUE(tenant_id, idempotency_key, kind)`，同一个键可以再执行一次。本版本的契约是：幂等期等于保留期。同键要永久拒绝，就不能靠删任务行实现，本版本不另建永久幂等表。写成 `0` 或负数不会关闭保留，会退回 30 天。
- 导入报告与解析结果里的解析器版本是 `go-pptx-v2.0.3`。它跟随 `go.mod` 里 `github.com/F31/go-pptx/v2` 的 require，不是环境变量。

### 配置

全表在 `packaging/config.md`，由 `go generate ./internal/config/` 从登记表生成。`packaging/config.env.example` 是 PostgreSQL 单机安装模板（密钥 `CHANGE_ME`，TTS 为 `fake`）。什么都不设时进程走 SQLite，开发头关闭，没配指标 token 则拒绝抓取，没配配音供应商且没有模型网关时 worker 拒绝启动。

`server`、`worker`、`migrate` 遇到未登记的 `PPTS_*` 会打 WARN，日志只有键名。

### 已知限制

- 分组重名靠应用层预检查。库里还没有唯一索引，并发创建仍可能出现同名分组。
- 没装 ffmpeg 时，超长 WAV 混音轨会超过校验的 64 MiB 单条上限。装了 ffmpeg 后成片走 AAC。
- 发布构建必须 `GOWORK=off`。开发机的 `go.work` 挂着 `../go-pptx` 与 `./textnorm`，和发行包解析到的模块版本不是同一条路径。

### 打包

```bash
git tag v1.0.0
scripts/package.sh
```

未打标签时 `scripts/package.sh` 用提交哈希当版本号，只适合试包。
