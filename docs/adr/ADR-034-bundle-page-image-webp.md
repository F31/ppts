# ADR-034：成品包页图改用有损 WebP（q75），编码走外部 ffmpeg，失败退回 JPEG

- 状态：**提议**（2026-11-08，实验已完成，待批准实施）
- 关联：`internal/share/images.go`（EncodePage）、`internal/share/measure.go`（体积类别与预算注释）、
  ADR-032（AAC 转码——本决策复用其「外部编码器注入 + 失败降级」架构）、ADR-029 ⑤（成品形态）

## 背景

V15 的三件套（design ADR-6 的 4.2 MB 预算依据）里，字体子集（ADR-028）与音频 AAC（ADR-032）
已落地，**WebP 是唯一缺席项**。`measure.go` 的注释明写：页面底图是 JPEG q82，真实 PPT
截图按 150–400 KB/页算，20 页就是 3–8 MB。当前 5 分钟包 3.72 MiB 中页图类占 1.32 MiB，
音频收口后它成了剩余体积的大头。

不引依赖的取向（`images.go`：「不引入 x/image 依赖」）意味着 WebP 不能拍脑袋上，
先做实验拿数据。

## 实验

### 素材

**真实渲染页图**，不是程序合成图（合成图大面积纯色，会高估压缩收益——Q7 的教训）：

- 源：`internal/animation/animdefault/testdata/sample-customer-deck.pptx`（14 页真实商务模板，
  白底 + 中文标题/正文 + 图标卡片，无照片）
- 链路：WSL LibreOffice → PDF → `pdftoppm -r 200` → PNG（2667×1500）
- 预处理：长边缩到 1600×900（对齐 `EncodePage.MaxPageLongEdge`），所有方案吃**同一份**缩放源
- 实验脚本：`tmp/webp-exp/run.py`（不入库；复现步骤如上）

### 结果（14 页合计 / 每页最差）

| 方案 | 总字节 | vs 现状 | SSIM 均值 | SSIM 最差 | 高差像素占比¹ 最差 |
|---|---:|---:|---:|---:|---:|
| **JPEG q82（现状）** | 1,239,791 | 100% | 0.99238 | 0.98408 | 4.00% |
| JPEG q75 | 1,097,228 | 88.5% | 0.99042 | 0.98136 | 4.83% |
| JPEG q70 | 1,034,137 | 83.4% | 0.98969 | 0.98031 | 5.29% |
| **WebP q80** | 581,714 | **46.9%** | **0.99600** | **0.98993** | **3.07%** |
| **WebP q75** | 512,698 | **41.4%** | **0.99518** | **0.98872** | **3.59%** |
| WebP q70 | 494,544 | 39.9% | 0.99484 | 0.98800 | 3.78% |

¹ |diff| > 10/255 的像素占比，对文字边缘的局部损伤比全局 SSIM 敏感。

**核心结论：WebP q75 体积降到 41.4%，且两个质量指标反而全面优于现状 JPEG q82**——
同一字节数下 WebP 对文字边缘的保留明显更好。这不是「用质量换体积」，是纯粹的赢。
而「不引依赖、只降 JPEG 质量」的路线（q70/q75）只省 11–17% 且质量同步下降，不可行。

### 编码器选型（Go 侧）

标准库不能编 WebP，候选逐一实测或排除：

| 候选 | 结论 |
|---|---|
| `HugoSmits86/nativewebp` v1.3.0（纯 Go） | **排除**。只有 VP8L lossless，无有损编码。实测同源 14 页 **114.2%**（1,323,810 vs 1,159,573 字节）——缩放后的抗锯齿文字边缘产生大量「几乎但不完全相同」的颜色，lossless 全面吃亏 |
| `kolesa-team/go-webp`（cgo 绑 libwebp） | **排除**。cgo 破坏 CI 的 4×crossbuild 与渲染镜像外的所有交叉编译场景 |
| **外部进程 `ffmpeg -c:v libwebp`** | **推荐**。与 ADR-032 的 AAC 转码完全同构：编码器注入接口 + 启动时探测 + 失败优雅降级。体积与官方 `cwebp`（libwebp 1.3.2）和 PIL（同 libwebp）一致到 ±2% 以内，0.26s/页（默认 effort，30 页约 8 秒） |

ffmpeg 侧验证过一组坑：**对照必须用同一份缩放源**——第一轮拿 2667×1500 原图给
ffmpeg/cwebp 编码得到 73 KB，误判「ffmpeg 比 PIL 大 76%」；缩到 1600×900 后三方一致
（ffmpeg 42.6 KB / cwebp 41.7 KB / PIL 41.7 KB，page-03）。ffmpeg 仅比官方工具大 ~2%，
来自默认 compression_level 4（可调 6，耗时基本不变）。

## 拟实施方案

1. `internal/share` 新增 `PageImageEncoder` 接口（与 `M4Encoder` 同构）：
   `Encode(img image.Image, longEdge int) (data []byte, ext string, err error)`。
   默认实现 `jpegEncoder` 保持现状（JPEG q82）；`ffmpegWebpEncoder` 调
   `ffmpeg -c:v libwebp -quality 75 -compression_level 6`，输入缩放后的 RGBA 管道输出。
2. 探测与降级照抄 ADR-032：`BundleBuilder.SetPageImageEncoder` 注入；`internal/app/export.go`
   与 sidecar 各自装配（sidecar 走 `sync.Once` 探测）；ffmpeg 缺失 / 编码失败 / 空输出 /
   **产物反而更大**（≤100 KB 的小图 WebP 可能不占优）任一命中 → 退回 JPEG q82。
   逐页独立降级：一页失败只影响该页，不让整包失败（与「转码失败不阻断导出」一致）。
3. `measure.go` 体积类别按**实际**扩展名分桶（`.webp` 进页图类），V15 报告如实反映；
   预算基数**暂不收紧**——先让 WebP 落地拿真实分布，再议 4.2 MB 目标（与字体子集/AAC
   的顺序相同：先落地、再收口）。
4. `EncodePage` 需支持 WebP 输入透传：renderer 侧将来可能直接产出 WebP，
   `image.Decode` 标准库不认 webp，需检测 RIFF 头直接放行（扩展名 `.webp`），
   避免被当「解码失败回退 PNG」误标扩展名。
5. 清单/播放器/门禁无需改动：`single.go` 的 data URI MIME 表已含 `image/webp`；
   播放器按清单文件名加载；浏览器门禁的资源判据是「能取到」，与格式无关。
   兼容性：WebP 有损解码覆盖所有现代浏览器（Safari 14+，2020）。

## 收益估算

- 5 分钟 / 30 页目录包：页图 1.32 MiB → ~0.55 MiB，总包 3.72 → **~2.95 MiB**（-21%）；
- 单文件（ADR-033 后剩余成本主要就是页图 ×2：清单 + noscript）：WAV 21.9 → ~18.7 MB，
  AAC 6.68 → **~5.9 MB**（转发场景的直接收益）；
- 20 页真实素材的包（150–400 KB/页 JPEG）：WebP 后页图部分降到 40–45%。

## 风险与重估条件

- ffmpeg 在部署环境的可得性与 AAC 相同（同一二进制、同一探测逻辑），风险已由 ADR-032
  的降级路径覆盖；本机 Windows 无 ffmpeg 时门禁样本仍是 JPEG——门禁的体积判据不能
  假定格式，须按 measure 实际输出断言。
- 编码耗时 +0.3s/页（30 页约 +8s），对导出任务（分钟级）不显著；若将来页数上限提高，
  可并行化（与渲染页级并行同构）。
- 重估条件：出现纯 Go 有损 WebP 编码器（届时可去掉外部依赖）；或页图改为 SVG/矢量
  交付（届时报废本决策）。
