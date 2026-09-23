# ADR-020：字级对齐——静音锚点估算（已落地）与强制对齐选型（待确认）

- 状态：阶段一已实现；阶段二（强制对齐）**待确认**（需先定部署形态与阈值）
- 关联：`docs/语音对齐方法AlignMethod与优先级.md`；`internal/integrations/tts/vad_align.go`；`internal/app/narration.go:validateSynthesisResult`
- 编号说明：ADR-018/019 之后续编为 020。

## 背景

`buildEstimatedAlignment`（`internal/integrations/tts/siliconflow.go`）在净时长区间内做
**匀速**字符分布，与真实语音的停顿、语速变化不符；播放器用真实音频的 `audio.currentTime`
去查这份估算时间轴，高亮会逐渐偏离真实朗读位置。播放器时钟逻辑与音频时长错位已排除。

对齐时间轴是网页高亮与 MP4 字幕烧录的**唯一共源**（`media.Timeline.Subtitles[].Chars`），
因此修复只需发生在"生成对齐"层。

## 决策

### 阶段一（已实现，可立即上线）

在不引入任何新模型/服务依赖的前提下，对真实合成音频做能量 VAD，把检测到的**真实停顿**
锚定到文本标点，将整段切成若干小区间，仅在各小区间内做匀速分配；VAD 无效时回退整体匀速。

- 新 `AlignMethod=estimated_vad` 与既有 `estimated` 区分，便于阶段二对比与排查。
- 兜底严格：无有效静音 / 无标点 / 锚点区间非法 → 回退 `estimated`，不报错、不空转。
- 量化：合成受控语料上 MAE 184→84ms（−54%）、MaxAE 372→183ms（−51%）。

### 阶段二（待确认）

目标是接入**强制对齐**（已知文本 + 已知音频 → 真实字级时间戳），作为长期主路径，效果不
依赖 TTS 供应商是否提供原生时间戳。按现有 `AlignMethod`/时间戳类型抽象接入，新增
`AlignMethod=forced`，失败/低置信度自动降级到 `estimated_vad`，用户侧无感。

**该阶段需要新增部署组件或外部依赖，故先给出选型评估，确认后再实现。**

## 部署条件（实测，用于选型）

| 项目 | 现状 |
|---|---|
| GPU | 有：NVIDIA RTX 5090 Laptop（WSL2） |
| CPU / 内存 | 24 核 / 15 GiB |
| Python | 3.12.3 |
| onnxruntime | 1.27.0（**仅 CPUExecutionProvider / AzureExecutionProvider，无 CUDA provider**） |
| torch / funasr / modelscope | **未安装** |
| 现有服务形态 | 纯 Go（api/worker 两个 systemd 服务）；无 Python sidecar；ffmpeg/ffprobe 静态随包 |
| 磁盘 | / 约 626 GiB 可用 |

## 备选方案与代价

| 方案 | 形态 | 优点 | 代价 / 风险 |
|---|---|---|---|
| A. 本地 FunASR/Paraformer CTC 对齐（Python sidecar） | 新增 Python 服务（FastAPI 包 FunASR） | 中文原生、CTC 字级对齐 + 置信度、可 GPU | 新增 sidecar 运维（部署/健康检查/升级）；需 torch+funasr（GB 级）、模型下载；与现有纯 Go 栈异构 |
| B. 本地 ONNX Paraformer CTC（onnxruntime，无 torch） | 轻量 sidecar 或短暂子进程 | 复用已装 onnxruntime，规避 torch；CPU 离线够用；保留 GPU 后续可选 | 需获取/校验 ONNX 模型与字典；仍需一个 sidecar/进程；模型来源与合规需确认 |
| C. 外部强制对齐/ASR API（阿里/讯飞/腾讯或第三方） | 纯 HTTP 调用 | 零本地运维、接入快 | 按量计费；**讲解音频出境**（合规）；网络时延与限流；供应商锁定 |
| D. Go 进程内 ONNX（onnxruntime-go 绑定） | 无新服务 | 无 sidecar | CGO/原生库；模型与线程管理复杂；worker 进程内推理影响调度稳定性；集成量大 |
| E. 改用带原生时间戳的 TTS（AlignProvider，如 Azure） | 纯配置 | 最省事，架构已支持 | 不满足"供应商无关"；仅在有该供应商时生效；可能替换现有 TTS |

## 推荐

1. **阶段 2a（低成本、可尽快）**：先接通 `AlignProvider`（`provider_timestamps`）路径——
   已建模、改动小；在切换到/新增带时间戳的供应商时立即受益。不满足"供应商无关"主路径，
   仅作补充。
2. **阶段 2b（战略主路径）**：采用 **方案 B：本地 ONNX Paraformer CTC 对齐**，以一个小
   sidecar 或受控子进程暴露内部 HTTP 接口：
   - 复用已装的 onnxruntime（CPU）；GPU 可作为后续可选项（装 onnxruntime-gpu + CUDA）。
   - 避免 torch 巨型依赖，贴合现有部署（磁盘/内存充足）。
   - 若团队更看重模型生态与维护便利、且能接受 sidecar + torch，则选 **方案 A**。
3. **架构**（无论 A/B）：新增 `AlignmentProvider` 抽象（在 `internal/integrations/tts`
   内，与 `TTSProvider` 并列），worker 在合成落盘后**异步**触发对齐；成功且置信度达标则
   更新该段 alignment 并重发时间轴，前端平滑替换；失败/低置信度记录埋点并保留
   `estimated_vad`。置信度阈值建议 **0.6**（待确认）。
4. 不建议 C（合规 + 供应商锁定）与 D（进程内推理侵入 worker）作为主路径。

## 需要确认的输入

- 阶段二是走 **sidecar（A/B）** 还是允许 **外部 API（C）**？（涉及是否接受讲解音频出境与按量成本）
- 若走本地：接受 **onnxruntime+ONNX 模型（B）** 还是 **FunASR+torch（A）**？（运维与依赖体量权衡）
- 置信度阈值建议 **0.6**；阶段二验收阈值建议 mean ≤150ms、max ≤400ms（与需求一致）——
  确认是否按此执行。
- 阶段二是否要求 GPU（当前 onnxruntime 无 CUDA provider，GPU 路径需额外安装 CUDA/onnxruntime-gpu）。

## 影响

- 阶段一：无新增依赖、无新服务，`estimated_vad` 已替代 SiliconFlow 的原生估算路径并保留回退。
- 阶段二落地前，`forced_alignment` 常量与优先级链已在文档中预留；实现时按上述架构接入，
  不新起并行逻辑。
