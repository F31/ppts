# 出片保真检查（ADR-18）

回答一个问题：**渲染出来的视频，内容比源页少了什么没有？**

仓库里原先的样片是用色块占位的，画面里一个字形都没有。它适合验时间轴、转场、残影，
但对「文字丢没丢」完全不敏感 —— ADR-15 的空格缺陷和 ADR-17 的图层顺序缺陷都是
换成带真实文字的样片之后才暴露的。这套脚本就是那份样片的来源。

## 四步跑完

```bash
# 1) 造底图：字体从镜像里取，保证与线上同一份字形
docker create --name fsrc ppts/renderer:dev
docker cp fsrc:/usr/share/fonts/opentype/ppts /tmp/ppts-fonts && docker rm -f fsrc
python3 scripts/fidelity/gen_pages.py --fonts /tmp/ppts-fonts --out /tmp/fidelity

# 2) 编译成计划（底图会被拷进 job 目录，见 ADR-14）
go run ./scripts/fidelity/plan -in /tmp/fidelity -out /tmp/job

# 3) 提交给渲染服务（--assets-dir 指向 /tmp/job 的只读挂载）
curl -s -X POST --data-binary @/tmp/job/plan.json http://127.0.0.1:8090/v1/render
#    然后轮询 GET /v1/render/{jobId} 直到 succeeded，取回 segment_*.mp4

# 4) 比对末帧与源页
python3 scripts/fidelity/compare.py \
    --pages /tmp/fidelity/pages \
    --elements /tmp/fidelity/elements.json \
    --segments /tmp/job-out
```

## 判据

| 指标 | 阈值 | 说明 |
|---|---|---|
| 整页平均绝对差 | ≤ 2.0 | H.264 编码噪声在 0.5–1.3；到 2 以上多半是内容变了 |
| 差 >32 的像素占比 | < 1% | 结构性丢失会到几个百分点（ADR-17 是 5.8%） |
| 元素区域墨量比 | ≥ 0.8 | 文字被盖掉/淡掉时直接趋近 0 |

任何一条不过，脚本以非零退出。这三个阈值来自实测：5 页带文字样片在正确实现下是
平均差 0.56–1.23、差 >32 的像素 <0.35%、墨量比 ≥0.95。

## 两个容易踩的口径坑

- **取末帧**，不要取页中任意一帧：入场动画在页末才回到恒等，取中间帧会把动画状态算成差异。
- **墨量的基准色只从源图取**：两边各自取中位色时，渐变区域会得到不同基准，
  算出来的是基准漂移而不是像素差异（第一版因此虚报了 3.7 个百分点）。

## 想复现 ADR-17 那个缺陷

把 `gen_pages.py` 里 `page1` 的图层顺序改成「先 items 后 card」再跑一遍：
三行要点的墨量会从约 6% 掉到 0.00%，而任务状态仍是 `succeeded`、没有任何 warning。

## 带配音的样片（Q7：动效是否跟着讲解走）

上面那条链路评的是「渲染器画得对不对」，它**测不了**「该动的时候才动」——
成片没有音轨，起播点是 `120 + i*180` 按元素序号排的，trigger 全是 `onEnter`。
这一节是补那条链路：把讲解词合成为音频，把元素的起播点绑到段落上。

### 五步

```bash
# 0) 底图沿用 5.5 那套（同样五页、同一份字形）
cp -r tmp/5.5/fid tmp/q7/fid

# 1) 讲解词 → 逐段配音 + 页内时间轴（edge-tts + ffmpeg，在 WSL 里跑）
python3 scripts/fidelity/narrate.py
#    → tmp/q7/segments.json（每段起止）+ tmp/q7/audio/timeline.wav（整轨）

# 2) 编译计划：clip.trigger 写成 withSegment:<段 id>，页长取配音实测值
go run ./scripts/fidelity/plan -in tmp/q7/fid -out tmp/q7/job \
  -tier-compliant -script tmp/q7/segments.json -hard-cut
#    → plan.json（零 warning；未绑定的元素走页末 atTime 淡入）

# 3) 渲染 + 拼接 + 混入音轨（起容器、提交、取产物、合成一次跑完）
bash tmp/q7/run_all.sh
#    → tmp/q7/film_C_narrated.mp4

# 4) 量「元素到底在什么时候出现」
python3 scripts/fidelity/check_alignment.py tmp/q7/film_C_narrated.mp4 \
  tmp/q7/segments.json tmp/q7/fid/elements.json 2.0
#    → 每个元素的 pre（开口前 400ms 内的最大变化）、onset、delay

# 5) 人工评审
python3 scripts/review/serve.py --root tmp/q7 --page scripts/review/q7.html \
  --film film_C_narrated.mp4 --port 8089
```

### 四个必须这么做的选择

| 选择 | 不这么做会怎样 |
|---|---|
| **逐段合成**，不是整页合成后再切 | 整页音频只能靠 VAD 或强制对齐切段，误差几十到几百毫秒；逐段合成的段落边界就是拼接点 |
| **裁掉首尾静音**（`silenceremove` 双向） | edge-tts 的 mp3 头部自带约 100ms 静音，元素按 startMs 出现、声音却晚 100ms——3 帧，正好踩在「不滞后」的判据线上，测出来的是 TTS 的静音头，不是渲染器 |
| **trigger 用 `withSegment`**，不是 `atTime` | 两者都能出片，但用 atTime 时改一句讲解词，音频时长变了、画面还按旧毫秒走，且不报错 |
| **关掉页转场**（`-hard-cut`） | 转场 320ms 逐像素混合，页首 220ms 开口的段落与它重叠，包围盒灰度差量出来的是混合度，不是元素不透明度 |

### 判据与实测

`check_alignment.py` 的算法：取段落开口前 400ms 到开口后 1100ms 的连续帧，裁出元素
包围盒，算它与「开口前那一帧」的灰度平均绝对差（MAD），第一次越过 2.0 的帧就是实际起播点。
只裁包围盒是必须的——整页指标会被大面积纯底色压住，2% 面积的变化看不出来。

2026-10-30 这版（30.77s / 13 段 / 14 个绑定元素）：

- **开口前 400ms 内，14 个元素的区域变化全部为 0.0** —— 没有「提前出现」。
- **扣除设计偏移后，最大起播延迟 47ms（1.4 帧）**；`p1-card`（设计 +140ms）实测 147ms、
  `p3-flow`（设计 +700ms）实测 713ms、`p3-hero`（设计 +320ms）实测 300ms，说明段内错落
  也是准的，不是「所有元素一起塌到段首」。
- 联系表上可以肉眼复核：讲「五段流水线」时流程图才出现，讲之前那一页只有标题。

### 这版样片的边界

- 配音来自 **edge-tts**（免密钥的在线合成），不是产品链路的 SiliconFlow TTS。段落时长是真实的，
  音色与线上不同——判对齐够用，判音质不够。
- 元素 → 段落的绑定是**手写**的 `q7-script.json`，不是模型或规则自动生成的。这证明的是
  「`withSegment` 这条路通、渲染端按时出画」，不证明「绑定能自动生成」。后者是 Q7 第 1 件事。
- 4 个元素刻意不绑定（`p0-eyebrow`、`p0-meta`、`p3-caption`、`p4-note`），放在页末淡入，
  作为「不空转」的对照组。

## 元素→段落的绑定实验（Q7 第 1 件事）

上一节证明了「绑上去之后，元素确实跟着讲解出现」。这一节问的是**绑定本身从哪来**。
结论写在 `openspec/changes/animation-render-service-image/q7-bind-experiment.md`，
这里只记怎么复跑。

```bash
# 0) 元素文本：render.Element 只有 ID/Kind/Bounds，文本在 PageElements 换算时被丢掉。
#    从 gen_pages.py 的源头补回（底图一个字节不动）
python3 scripts/fidelity/enrich_elements.py tmp/q7/fid/elements.json

# 1) 复述式讲稿（照元素文字写，字面重合被抬高）
python3 scripts/fidelity/bind_elements.py --show-matrix --out tmp/q7/bind-report.json

# 2) 口语式讲稿（正常讲法：不复述标题、用指代、口语数量）—— 消融对照
python3 scripts/fidelity/bind_elements.py \
  --segments scripts/fidelity/q7-script-colloquial.json \
  --out tmp/q7/bind-report-colloquial.json

# 3) 只看字面匹配（第一轮的算法），用来对照 v2 加了多少
python3 scripts/fidelity/bind_elements.py --rules base --out tmp/q7/bind-report-base.json

# 4) 第二个样本：真实讲义 5 页（带表格/卡片/页码/装饰形状），元素从 pptx 抽
python3 scripts/fidelity/extract_pptx_elements.py \
  --slides 1,2,4,8,10 --out tmp/q8/fid/elements.json
python3 scripts/fidelity/bind_elements.py --rules base \
  --segments scripts/fidelity/q8-script.json \
  --elements tmp/q8/fid/elements.json --golden scripts/fidelity/q8-script.json \
  --out tmp/q8/bind-report-base.json
python3 scripts/fidelity/bind_elements.py --rules v2 \
  --segments scripts/fidelity/q8-script.json \
  --elements tmp/q8/fid/elements.json --golden scripts/fidelity/q8-script.json \
  --out tmp/q8/bind-report-v2.json
```

`--show-matrix` 打印每页「元素 × 段落」的相似度，调阈值、看失败原因都从这张表开始，
不要凭结果反推参数。`--rules base|v2` 是同一个脚本的两档：`base` 只有字面匹配，
`v2` 再加辅助白名单、数量词、序数、方位、同块带出五条规则（`--no-siblings` 可单独关掉
同块带出）。**报数必须说明用的是哪一档**，否则两组数字不可比。

### 三条不要改坏的性质

- **单调对齐**（DP）而不是逐个取最大：后者会把四行条目全绑到第一段，且允许「倒退」，
  而讲解是线性的。
- **绑定来源全部分开记账**：字面 / 计数 / 序数 / 方位 / 同块 / 标题先验 / 结构推断。
  混在一起会把「容器按位置挂上去」算成「文本匹配成功」，召回率会虚高 5–10 个点。
- **golden 可以标 `role: "decor"`**：图标、圆形底座这类伴随装饰绑不上时，不该算成
  内容召回失败。报告里内容召回与总召回分列。

### 实测（2026-10-30）

| 样本 | 讲稿 | 规则 | 召回（全部） | 召回（内容） | 精确 |
|---|---|---|---|---|---|
| 合成 5 页 / 19 元素 | 复述式 | base | 0.800 | 0.800 | 0.923 |
| 合成 5 页 | 口语式 | base | 0.400 | 0.400 | 1.000 |
| 合成 5 页 | 复述式 | v2 | 0.933 | 0.933 | 1.000 |
| 合成 5 页 | 口语式 | v2 | 0.533 | 0.533 | 1.000 |
| 真实 5 页 / 71 元素 | 自然讲法 | base | 0.404 | 0.500 | 0.955 |
| 真实 5 页 | 自然讲法 | v2 | **0.981** | **0.972** | 0.962 |

两个数字最值得记住：**真实讲义上 0.97，合成样本的口语版上 0.53**。同一套规则、
同一档参数，差在版面有没有把讲解顺序编码出来（编号、卡片、方位）。引用「0.97」
必须带上这半句，否则会变成「规则够用了」的错误结论。

## 出现时刻与音画同步：两个「看片之前先排除」的检查

5.6 收到一份「四个判据全 1 分」的评分，而同片的逐段对齐测量说「全部准时」。
查下来是**测量本身有两个洞**，与片子无关——这两个检查现在是评第 7 项前的必跑项。

```bash
# 1) 整页范围的出现时刻：元素到底什么时候真的出现在画面上
python3 scripts/fidelity/check_appearance.py

# 2) 音画是否同步：成片音轨 vs 旁白原件（timeline.wav）
python3 scripts/fidelity/check_av_sync.py
```

**为什么需要 1）**：`check_alignment.py` 的取窗是 `t0 = max(page_start, at-400)`，
只回看 400ms。元素若在更早出现（页首一起铺、或跟着上一段出来），窗口里它「已经在」，
相对窗口首帧的差恒为 0 → `preMax = 0`、`delay = 0`，工具报「不提前、准时」。
`check_appearance.py` 反过来以**该页最后一帧**为终端状态，逐帧量到「最后一次变化之后」，
整页范围内不设窗口。

**为什么需要 2）**：所有对齐测量的参照点都是 `segments.json` 的 `startMs`，那是**文本
时间轴**，不是观众听到的时间。封装时音频整体偏移（AAC priming、容器起点）会让画面
相对人声提前/滞后，而视频侧的两个检查一个都发现不了。

### 实测（film_C_narrated.mp4，2026-10-30）

| 检查 | 结果 |
|---|---|
| 出现时刻（19 个元素） | **没有任何元素早于其所属段落**；15 个绑定元素的 `lead` 在 −42 ~ −446ms 之间（负=到位晚于开口，与淡入时长一致） |
| 未绑定元素 | `p0-eyebrow`/`p0-meta` 5.08s、`p3-caption` 27.84s、`p4-note` 30.27s —— 都是页末 `unboundLeadMS=900` 的淡入，不是页首铺满 |
| 音画同步 | **lag 0.000s**，±100ms 处相关值掉到 0.44（峰尖锐，读数可信） |
| 音轨内容 | mean −26.9 dB / max −3.9 dB（有真实语音，不是静音轨） |

一条局限：`p1-card` 这种**容器**的读数会落到「最后一个子元素到位」（报 12.5s，实际
5.9s 就在了）。大容器只看「有没有提前」，不要拿绝对值对段落。
