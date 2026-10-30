# 5.5 观感盲评 · 评审包

> 状态：材料已就绪，**等待两位评审人独立打分**。任务本身不能由脚本关闭——脚本能证明「像素没丢」，
> 证明不了「看起来对不对」。本包的作用是把能用机器排除的干扰先排掉，让人只对「像不像参考片」负责。

## 一、评的是哪两份片子

同一份底图（`scripts/fidelity/gen_pages.py` 生成的 5 页真实中文）、同一套渲染器，只有入场时长不同：

| | 样片 A（默认） | 样片 B（§19.4 合规） |
|---|---|---|
| 用途 | 与 ADR-18 保真基线、5.10 的记录同一份，**回归用** | **盲评用**——时长落在速度分层内 |
| 入场时长 | 标题 700ms / 其余 520ms | 按元素面积占比交给 `MotionProfile.DurationFor`：小 320 / 中 500 / 大 750 |
| 编译告警 | 5 条 `duration_tier_conflict`（9 条 clip 落在 240–400 与 400–600 之间的空档） | **0 条** |
| planHash | `b4dd44e0a720d65d…` | `7fccdd51f5ad937f…` |

为什么要有 B：默认的 520ms 不在任何一档里（§19.4 是 240–400 / 400–600 / 600–900），
拿 A 去评「时序与节拍」，打出来的低分反映的是测试计划的时长选择，不是渲染器能力。
A 仍然保留——它是 5.10 那张保真表的输入，改了就对不上账。

## 二、身份与溯源

| 项 | 值 |
|---|---|
| 仓库 | `F31/ppts` @ `c5b5082a6e1b879d2956e8563593338018286fc6`（main） |
| 渲染器二进制 | 由上述 HEAD 交叉编译，sha256 `0ba0405bacfb290f…`（`CGO_ENABLED=0 GOOS=linux`） |
| 运行镜像 | `ppts/renderer:dev`，sha256 `bc6e5aa7eabec722…`，Chrome for Testing 156.0.8078.73 |
| 成片 A | `film_30s.mp4`，1920×1080 / 30.000s / 900 帧，sha256 `6f55b67e26dba55d…` |
| 成片 B | `film_B_tier_30s.mp4`，1920×1080 / 30.000s / 900 帧，sha256 `cdddb11e8c6e4551…` |
| 分镜联系表 | `film_30s_storyboard.png` / `film_B_storyboard.png`（每 2s 一帧，5×3） |

文件都在 `tmp/5.5/`（已被 `.gitignore` 忽略），大文件不进仓库，靠 sha256 对账。

**为什么二进制要自己编**：`ppts/renderer:dev` 里的 Go 二进制构建于 16:10，早于 `3.7`（17:39）
与 `4.1–4.5`（16:12）两个提交。观感评审必须评当前代码，所以把 HEAD 编译出的静态二进制挂进镜像替换，
镜像里的 Node/Chrome/字体部分不动。

## 三、机器门禁（过不了就别打分）

### 3.1 保真比对（ADR-18）

`scripts/fidelity/compare.py`，判据：整页平均差 ≤2.0、差 >32 像素 <1%、元素墨量比 ≥0.8。

| 页 | A 平均差 | A >32 | A 墨量比 | B 平均差 | B >32 | B 墨量比 |
|---|---|---|---|---|---|---|
| 0 | 1.23 | 0.138% | 1.00 | 1.23 | 0.138% | 1.00 |
| 1 | 0.67 | 0.117% | 1.00 | 0.67 | 0.117% | 1.00 |
| 2 | 0.59 | 0.344% | 1.00 | 0.59 | 0.344% | 1.00 |
| 3 | 0.56 | 0.089% | 0.98 | 0.56 | 0.089% | 0.97 |
| 4 | 1.23 | 0.000% | 0.99 | 1.23 | 0.000% | 0.99 |

两份都 **PASS**，且数值与 5.10 记录的 0.56–1.23 / <0.35% / ≥0.98 一致——同一份计划重跑是确定性的。
含义：末帧与源页无结构性差异，第 1、3 项（字形完整、元素完整）大概率不会因回归而丢分。

### 3.2 观感度量（§19.6 中可机器判的部分）

`scripts/review/perception-metrics.py tmp/5.5/job-b/plan.json`：

| 页 | 元素数 | clip 数 | clip/秒 | 时长 | clip 区间 | 起播点 |
|---|---|---|---|---|---|---|
| 0 | 4 | 4 | 0.67 | 6.0s | 320–500 | 100 / 120 / 480 / 660 |
| 1 | 6 | 6 | 1.00 | 6.0s | 320–750 | 100 / 300 / 480 / 660 / 840 / 1020 |
| 2 | 2 | 2 | 0.33 | 6.0s | 500–750 | 100 / 300 |
| 3 | 4 | 4 | 0.67 | 6.0s | 320–750 | 100 / 300 / 480 / 660 |
| 4 | 3 | 3 | 0.50 | 6.0s | 320–500 | 100 / 300 / 480 |

§19.6 两条硬阈值：元素数 ≤9（全部满足）、clip 数/秒 ≤3（最高 1.0）。**无越界**。

### 3.3 对比度（§19.6 要求 ≥4.5:1）

按 WCAG 相对亮度算的，配色取自 `gen_pages.py` 的画布常量：

| 组合 | 比值 | 判 |
|---|---|---|
| INK `#1B222C` on 白 | 16.01:1 | PASS |
| MUTED `#5B6677` on 白 / 卡片 / 近白 | 5.81 / 5.41 / 5.62:1 | PASS |
| ACCENT `#1E3A8A` on 浅蓝框 `#DCE6F4` | 8.22:1 | PASS |
| 白字 on ACCENT / ACCENT2 | 10.36 / 5.47:1 | PASS |
| 浅字 `#E8EEF6` on 深色页 `#0E1B2E` | 14.81:1 | PASS |

### 3.4 字号（§19.6 要求正文 ≥32px @1080p）

底图实际用到的字号：26 / 28 / 30 / 32 / 34 / 38 / 40 / 46 / 62 / 66 / 104px。
**26/28/30px 低于 32px 门槛**，全部出现在图表刻度、脚注与卡片副行（不是正文段落）。
请在评第 2 项「文字清晰」时知道这一点：这几处小字是**故意压到临界**的压力输入，
读不清记缺陷，但记的是「小字号可读性」，不要算成渲染器糊了图。

## 四、评分表（两位评审各自独立填，先不商量）

打开评审页（推荐，省得在播放器与表格之间来回切）：

```bash
python3 scripts/review/serve.py        # http://127.0.0.1:8088/
```

页面里成片可拖拽（服务端支持 Range，回看 12.4s 是评分核心动作）、右边是源页对照、
联系表与机器门禁数值都在同一页；评分表选「甲 / 乙」后各自独立存，填完点
「复制为 Markdown」直接得到下面这张表的文本，贴回本节即可。

不启动服务也能评：直接用播放器打开 `tmp/5.5/film_B_tier_30s.mp4`，手填下表。

每项 1–5 分，3 分 = 可用。判据与 5 分/3 分/1 分锚点见 `scripts/review/RUBRIC.md` §二。

成片：`film_B_tier_30s.mp4`（sha256 `cdddb11e8c6e4551…`）

| # | 项目 | 甲 | 乙 | 分差 | 扣分定位（秒数 + 元素） |
|---|---|---|---|---|---|
| 1 | 字形完整 | 4 | | 待乙 | **未填**（见下方「待补」） |
| 2 | 文字清晰 | 4 | | 待乙 | **未填** |
| 3 | 元素完整 | 4 | | 待乙 | **未填** |
| 4 | 残影与转场 | 3 | | 待乙 | **未填** |
| 5 | 时序与节拍 | 3 | | 待乙 | **未填** |
| 6 | 色彩与对比 | 4 | | 待乙 | **未填** |

规则（`RUBRIC.md` §一）：100% 缩放看一遍，再按联系表逐格核对；两人先各自填、再对表；
同一项分差 ≥2 分回看对应秒数定夺；只对「能定位到具体秒数/具体元素」的问题扣分，
说不出位置的「感觉不好」写进备注，不计分。

### 4.1 待补：甲的四分与三分没有定位

甲填的是「扣分定位：（无）」，但 1/2/3/6 给 4 分、4/5 给 3 分——按 §一第 3 条，
**扣分必须能指到秒数与元素**；说不出位置的观感应记备注、该项仍记 5 分。两种收口方式：

- 补上定位（例：「第 3 页 3.42s 第二张卡片右下角文字边缘轻微振铃」）；
- 或把该项改回 5 分，把「说不上来但感觉偏弱」移进备注（备注不计分）。

第 5 项尤其需要定位，因为它可对账：声明起播点为每页 100/120/480/660ms 一档
（`tmp/5.5/job-b/plan.json` 实录，与本包 §3.2 一致）。已知**系统性偏移 +1 帧**：
5.4 在样片 A 上实测 733/933/1167ms 对声明 700/920/1140ms，差 33ms = 1 帧，
来自「t=0 必须是纯白」——首帧不画内容，起播点落在下一帧。按 §二锚点，1 帧内记 5 分、
2–3 帧记 3 分。所以 3 分意味着看到了 **67–100ms** 的滞后；若有，请指出页与秒数，
若只是那 33ms 的已知偏移，该项应为 5。

结论：☐ 可用　☐ 需返工（附最小复现计划）　☑ **未定**（乙未评；甲自填「可用」，
但其 4/5 项的扣分缺定位，见 4.1）

评审人 / 日期：甲 \_\_\_\_\_\_\_\_（2026-10-30，定位待补）　乙 \_\_\_\_\_\_\_\_（未评）

## 五、§19 口径出处（免得下次又找不到）

全部出自 `E:\projects\项目设计文档\ppts\动画视频扩展-设计方案-V1_0.md`（**仓库外**）：

- **§19.1**（第 1350 行起）效果构成拆解：资产 ~35%、版式 ~20%、镜头 ~15%、运动质感 ~15%、
  音频节奏 ~10%、**渲染引擎本身 ~5%**。本表 6 项只覆盖后两者中的渲染部分——资产/版式/镜头
  不像参考片，不该由这一轮评分承担。
- **§19.4**（1425 行起）运动质感参数：stagger 60–90ms、anticipation 15% 反向 4–8px、
  overshoot damping 12–16 幅度 ≤8%、速度分层、hold ≥400ms、beat 每页 3–5 个单拍 900–1400ms。
  代码化的档案在 `internal/design/motion.go`（`SpeedTiers` 600–900 / 400–600 / 240–400）。
- **§19.6**（1454 行起）观感度量：静帧可读性、每页主要元素 7±2、正文 ≥32px、对比度 ≥4.5:1、
  clip/秒 ≤3、beat 3–5、每批抽 3 页盲评。
- **§19.8**（1473 行起）补齐路线与 M2 门禁：「5 页真实内容与参考片并排盲评，
  非专业人员能识别为同一种风格，才允许进入 M3」（另见文档第 1668 行）。

## 六、已知偏差与盲区（不要记成缺陷）

1. **参考片不在仓库里**。§19 对标的是一支 12m12s / 1080p30 的外部片，仓库内只有我们自己的样片
   （`prototype/output/sample.mp4` 就是本包这份 30s 的旧版，不是参考片）。因此「并排盲评」这一半
   只能人工在自己手上有参考片时做；本包的 6 项评分是**不依赖参考片也成立**的那些项。
2. **样片 A 带 5 条速度分层告警**（520ms 落在空档）。评运动质感用 B，不要用 A。
3. **26/28/30px 小字**是压力输入，见 3.4。
4. **部分重叠的兄弟元素会互相带出**（tasks 5.9，ADR-20 已能报诊断）——模型限制，5 页样片未命中。
5. **镜像里的字体是完整 Noto CJK，不是子集**。当前 `ppts/renderer:dev` 缺
   `/usr/share/fonts/opentype/ppts`（这两个 tag 早于字体阶段改动）；本轮用完整 Noto 出片，
   字形与线上同族，但**子集字体相关的空格缺陷（ADR-15）不在本片覆盖范围内**——那条由 5.10 单独验证过。
   子集产物本身本地可复现：`subset-fonts.py` 输出 22215 码位、版本 `2.004-sub03e7e8d7`，与修复后记录一致。
6. **只有 H.264 8bit**：细微色带与暗部细节的判断受编码影响，色彩项请对比源页 PNG
   （`tmp/5.5/fid/pages/`）而不是只凭记忆。

## 七、复跑命令

```bash
# 0) 字体子集（与线上同一份字形）
docker create --name nsrc ppts/renderer:dev && docker cp nsrc:/usr/share/fonts/opentype/noto /tmp/noto
docker rm -f nsrc
python3 deploy/renderer/subset-fonts.py --src /tmp/noto \
  --charset deploy/renderer/charset.txt --out /tmp/ppts-fonts --version-file /tmp/fonts.version

# 1) 底图 + 计划（-tier-compliant 才出合规档）
python3 scripts/fidelity/gen_pages.py --fonts /tmp/ppts-fonts --out tmp/5.5/fid
go run ./scripts/fidelity/plan -in tmp/5.5/fid -out tmp/5.5/job-b -tier-compliant

# 2) 当前代码的渲染器二进制，挂进镜像替换（镜像自带的早于 3.7 / 4.1–4.5）
GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o tmp/5.5/bin/ppts-renderer ./cmd/renderer
docker run -d --name ppts-review --tmpfs /work:mode=1777 --tmpfs /tmp:mode=1777 \
  -v $PWD/tmp/5.5/job-b:/assets:ro \
  -v $PWD/tmp/5.5/bin/ppts-renderer:/usr/local/bin/ppts-renderer:ro \
  -p 127.0.0.1:18090:8090 ppts/renderer:dev \
  -addr :8090 -backend remotion -workdir /work -renderer-script /app/render.mjs -assets-dir /assets

# 3) 提交 → 轮询 → 取产物（/work 是 tmpfs，docker cp 取不到，用 tar 流）
curl --noproxy '*' -X POST --data-binary @tmp/5.5/job-b/plan.json http://127.0.0.1:18090/v1/render
docker exec ppts-review tar -C /work/<jobId> -cf - . | tar -xf - -C tmp/5.5/out-b

# 4) 成片 / 联系表 / 门禁
ffmpeg -f concat -safe 0 -i concat.txt -c copy film_B_tier_30s.mp4
scripts/review/storyboard.sh film_B_tier_30s.mp4 film_B_storyboard.png 2 5
python3 scripts/fidelity/compare.py --pages tmp/5.5/fid/pages \
  --elements tmp/5.5/fid/elements.json --segments tmp/5.5/out-b
python3 scripts/review/perception-metrics.py tmp/5.5/job-b/plan.json
```

两个坑：`docker cp` 取不到 `--tmpfs /work` 里的产物，要 `docker exec ... tar` 流式取；
WSL 里 `http_proxy` 指向代理，访问 `127.0.0.1` 必须加 `--noproxy '*'`，否则会拿到 Cloudflare 的 400 页。
