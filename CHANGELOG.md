# 变更说明

## v0.5.0

注视引导（B + 薄 C）成为新一键片的默认运动。已保存的入场规格再导出仍走入场；换风格或「应用到全部页面」才切到注视。

### 行为变化

- 字幕显示偏移 `displayOffsetMs` 固化进成品库与分享包，编辑器/成品预览/分享播放器共用同一显示时钟；cue 选择与字级高亮不再各走一套时间轴。`Chars` 越界时回落到比例重映射并埋点，网页高亮缺失 `Chars` 时按行均分，与 ASS/SRT 烧录口径一致。
- 目录等多分区页按页面编号和旁白数字引用建立 section 锚点，`第 2 板块`、`第 4 区域` 等不再因为措辞差异被合并成同一个拍点。
- 表格/图表在表头开始朗读时先做整表/整图总览，再按数据行/成员逐项聚焦；表格、数据行、图表和柱状图不再生成词级底部下划线，普通正文仍保留指针。
- 注视目标改为按组对齐：目录等多项目共用一个面板时，不再把不同条目扩成同一矩形后只保留最后一项；`第N部分/步/条` 会作为不可跨合并的 clause 边界；拍点与形状改单调 DP 对齐并补齐倒序空位；导出导演不能把带序数的段落指到别的条目。已有成品需重新导出才会换成新结果。
- SRT/VTT 改为逐行 cue（12–34 字，复用时间轴字符时间戳），分享包的 inline cues 同步；长段落不再用一条字幕覆盖整页。已有成品的字幕文件需重新配音或导出后才会换成新布局。
- 新默认是整页静图 + 旁白时钟上的 spotlight / 圈选 / 压暗，不再为每个要点发射 fadeIn。教学页走 look 链（每条注视一帧 + ffmpeg hold），不按页时长进 Chrome。
- 「要点依次出现」是显式开关：打开后才挖洞、编入场层；包围盒不可靠时本页回退纯注视，并留下 `entrance_skipped`。一键成片与「应用到全部页面」把该勾选发给 apply-all，由 catalog `EntranceClips` 写入 `gaze+entrance`，不必再改主机 JSON。`ppts apply-animation --source-revision 0` 与缺省查询参数一样落到项目 `current_revision`，不再写 src=0 行。
- 一键成片默认第四步导出 MP4：任务入队后出现在任务中心，完成后进入成品库。只套动效、不导出时取消该勾选。没有配音时间轴时会先拦住，不会假装已经成片。
- 页眉大标题不再进入注视：宽条靠上的 `title` 从拍表和 look 候选里剔除，六宫格卡片标题保留。同一张卡上的标题/正文去重时保留更晚的正文起点，小框落在大框里的 look 丢掉，对焦时卡片内容已经入场完毕。
- `callout=auto` 对正方形走圆角矩形，不再内接成圆。只有预设显式 `circle` 才画圆。
- look 仍帧之间的叠化从 240ms 收到 120ms，避免两个圈选叠在一起时切焦闪一下。纱/圈选仍按 240ms 烧进仍帧。带入场层的 look 仍帧采 hold 的 3/4 处，且不早于该条及更早入场层播完、页间 fade 结束 +40ms。入场页取消到达拆帧和空底板前缀窗；fadeIn/slideIn/pop 起点对齐到该元素第一条 Focus。页切改为硬切（页头 ffmpeg xfade 会先切新页再把上一页冲回来）。look 之间同样硬切（同一套 loop 静图 xfade 会先切新框再闪回旧框）；不再拆到达帧。页眉大标题从原页图再贴在纱之上，始终保持原始清晰，卡片标题仍随纱压暗。角标抢走 `title` kind 时仍按几何识别（靠上、不太高、宽过 20%；副标题带 Y < 20%）。一进页就框第一条；look 旅行 overlay 淡入，不用 xfade。片段键 `sample=ready;page=cut;arrive=0;look=over;title=band`。
- 科技预设 `lookCrop` 只写在带 `Focus.rect` 的页上。没有焦点框的页再带这个标记时，渲染器会 `invalid_plan`，worker 把整份成片降成静帧。
- 科技预设 catalog 仍带 `lookCrop`，compile 词表否决：六宫格 / 列表 / 混合 / 图/表一律整页 + 纱 + 圈选。裁切铺满会把文字放大出画、邻卡消失。切近留给以后「大图」开关。片段键 `punch=off`。
- 词级下划线：旁白走到哪几个字，黄条跟到形状原文的那一截。整页保持 1:1，不切近。卡片纱和圈选还在。稀疏页同一张卡最多再采 2 帧；六宫格这类 ≥6 个互异注视的密页不再为词级指针加仍帧，第一条 Focus 也不再拆到达帧，避免 2G 渲染器 OOM。片段键 `pointer=1`。
- 切开的柱状图（`ChartBars`）在 `gaze+entrance` 下按旁白逐柱生长：柱体从基线长到终态，该页离开 look 链走 Chrome。整图 grow 仍跳过。不改片段键。
- 倾斜 opt-in：IR 允许 `rotate`（度），catalog 新增 `tilt`（默认 0→8°，强度缩放，±20° 封顶）。有 tilt/rotate 的页离开 look 链走 Chrome。默认预设不写 tilt，不猜天平形状。不改片段键。
- 带 `TableCells` 的表格在 `gaze+entrance` 下按旁白逐行淡入：表头留在底板，数据行等高切开 fadeIn。整表 fadeIn 仍跳过。有其它注视时行不当圈选；整页因此不足 2 条注视时，数据行成为圈选，整页留在 look 链。柱子仍不当圈选。同一元素在不同时刻可以再亮。不改片段键。
- 阶段 2 拍表导演：阶段 1 字面匹配不够时，导出路径用现有 LLM 通道要一份约束 BeatSheet JSON（只含时间、shapeId、shot）。失败、超时、输出代码/URL/未知 id 都退回阶段 1 均分。预览仍只跑阶段 1，不把模型塞进打开编辑器的路径。不落库、不送页图。不改片段键。
- 版式词表：条目列表纱加深到最多 0.60；其它版式默认纱。圈选到达 1.12→1 只动黄框，不动页图；导出 look 链不再拆到达帧。
- look 旅行 overlay 淡入，一进页就框第一条。同一张卡上未进拍表的标题/正文跟 Focus.rect 对齐入场。注视页（含 gaze+entrance）用底板+层图在 look 中点画纱/黄框，不进 Chrome。look overlay 头尾拼接以免截断时长。同卡多个拍点保留第一次被点到的时刻。片段键 `still=plate2`。HTML 低电量/掉帧不再拆入场层。
- 台上短标签只出现在预览与 HTML 分享页；导出 MP4 不烧台上 caption（ASS/VTT 仍是全文）。
- 已保存的 fadeIn 规格像素不变。发布后不必设新环境变量。
- 注视圈选扩到最小卡片底（空容器）。同一张卡上的标题/正文按矩形去重为一条 look，条数减少时按阅读序重铺时间。无卡片底的页仍用元素盒 + 8px。
- 注视圈选按源页 `sldSz` 换算。Office 默认 16:9（10"×5.625"）不再按 7.5 英寸页高缩放，圈选贴在画出来的卡片上。旧 extracted.json 没有页尺寸时导出/预览只读源 PPTX 的 `presentation.xml`，不重跑解析。
- 教学注视的 look 时钟跟旁白走：按句读或 VAD 停顿切拍，字面命中的形状才成孔。页脚页码按命名规则剔除。有效拍不足或结构化总起句盖住整页时回退均分（仍不圈页脚）。已保存的入场规格不跑拍表。
- 第一 look 晚于页起点时，前缀仍帧取该窗中点，不再采页边界上上一页的末帧。
- 解析页图 DPI 按 `sldSz` 对齐到成片长边 ≥1920（Office 默认 10" 16:9 为 192 DPI）。已有页图需重新解析才会变清晰。
- 「要点依次出现」（`gaze+entrance`）在教学注视页上仍走 look 链：挖洞后按 look 中点仍帧，240ms xfade 承担显现，不把整页丢进 Chrome。包围盒不可靠仍回退纯注视。已保存的纯 `entrance` 规格仍整页合成。
- 教学注视页每页一次 ffmpeg 图（PNG xfade + concat + ultrafast / CRF 18），不再对每条 look 先 medium 再整页 medium。几何与 240ms easeOutCubic 不变。片段键 `looks=once`。
- 成品库的 mp4 成品可以建在线分享链接：打开即为播放器，`/s/{token}/media` 支持 Range 拖动；创建与每次播放写审计（`artifact_share.create` / `artifact_share.play`），卡片显示留存期内的播放次数，分享面板按链接分别显示。链接沿用 128 位随机 token、有效期、口令与撤销，媒体流每次回查链接，不签发脱离撤销状态的预签名 URL。其它格式仍拒绝分享。
- 分组名在租户内唯一：升级时先把同名分组合并到最早一条（项目随迁），再加唯一索引。并发建同名分组会返回「已存在」，不再产生两条同名分组。

### 质量门禁

- V13（HTML vs MP4）注视 look 链页（含带 Focus.rect 的 `gaze+entrance`）取样每条 Focus 稳态中点，避开 240ms 交叉和页间 fade 窗口，不取 0ms。探针摘掉 `.ppts-caption`。
- V13b 注视页对照末 look（带纱），不对源页 PNG 做全帧 0.98。
- V13c 孔内矩形 vs 源页局部 SSIM ≥ 0.97。
- 观测：`ppts_animation_render_duration_ms_total{path=gaze_looks|chrome|still}`；教学导出 chrome 占 gaze+chrome 持续 > 20% 告警。
- 30 页教学 deck、一键成片、1080p30 的墙钟目标 18 min 以金丝雀为准，本版本不把该 SLO 写成硬门禁。

### 运维

- 生产升级走 `scripts/upgrade-production.sh <new-ppts>`：备份二进制、用本机 postgres peer 迁移、重启、healthz 失败自动回滚。
- 生产 HTTPS 改用 Let's Encrypt 证书（lego TLS-ALPN-01，`/etc/lego/certificates/`），`ppts-tls-renew.timer` 每日检查并自动续期。HTTP 80 仍被云侧 webblock 拦截，不影响 HTTPS。

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
