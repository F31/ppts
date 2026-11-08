# 架构决策记录（ADR）

本目录只放**独立成篇**的决策记录。编号与 V4.0 §18 的摘要表一致，不另起一套。

> V4.0 技术方案本身不随仓库分发（见根目录 `.gitignore`：本地 `docs/` 目录），
> 所以下表里的「V4.0 §18」在干净检出中打不开。这不是漏提交，是既有的文档策略；
> 本目录是唯一的例外，因为代码注释直接引用这些编号。

| 编号 | 标题 | 状态 | 记录形式 |
|---|---|---|---|
| 001–013 | 模块化单体、PG 任务表、Connect RPC、Tauri、go-pptx 适配、动画清单、TTS 主链路、统一时间轴、RLS、AI 来源校验、sidecar IPC、移动 L1/L2、原生播放时钟 | 已采纳 | 仅 V4.0 §18 摘要行（决策 + 重估条件） |
| 014 | `ObjectStore` 端口 + 按租户可配置多后端（含生命周期分层） | 已实现 | 摘要行；代码见 `internal/integrations/objectstore/`（`port.go`、`s3/`、`local/`） |
| 015 | 桌面隐私模式作为独立开关，移动端不提供 | 未实现（G4 顺延，见 [020](./ADR-020-native-client-timing.md)） | 摘要行 |
| 016 | 向量检索按需引入，优先 pgvector | 未实现（按设计无需现在做） | 摘要行 |
| 017 | 图解动画引擎（分镜 + SVG 组件库 + 代码渲染） | 属 go-pptx 仓库 | 本项目只引用，不在此登记 |
| [018](./ADR-018-cross-tenant-scheduler.md) | worker 跨租户调度与租户公平 | 已实现 | 独立记录 |
| [019](./ADR-019-migration-runtime-role-separation.md) | 迁移与运行角色分离（RLS 前提） | 已实现 | 独立记录 |
| [020](./ADR-020-native-client-timing.md) | G4 原生客户端的启动时机与最小范围（触发制 + G4-lite） | 已采纳（G4 推迟） | 独立记录 |
| [027](./ADR-027-number-form-boundary.md) | 数字形态判定留在 `internal/validation`，不迁入 textnorm | 已采纳 | 独立记录（021–026 属动画扩展方案，本地文档占用） |
| [028](./ADR-028-html-bundle-embedded-font.md) | HTML 成品包内嵌字体用「渲染服务二次子集」，按片中字符裁剪 | 已实现 | 独立记录 |
| [029](./ADR-029-product-decisions-animation-html.md) | 动画与 HTML 成品的六项产品决策（全片预览/免费层/竖屏/Lottie/成品形态/文本复制） | 已采纳（②③⑥ 分阶段实现） | 独立记录 |
| [030](./ADR-030-config-reading-policy.md) | 环境读取统一走 `internal/config`，装配层不直读 env、不自建解析器 | 已实现 | 独立记录 |
| [031](./ADR-031-measured-fps-degradation.md) | 成品包实测帧率降级：2s 窗口 × 连续 2 窗，只拆动画层、单向不回退 | 已实现 | 独立记录 |
| [032](./ADR-032-bundle-audio-aac.md) | 成品包混音轨转 AAC 64k 单声道：编码器注入、失败退回 WAV、强制 bitexact | 已实现 | 独立记录 |
| [033](./ADR-033-single-file-resource-dedup.md) | 单文件形态按资源去重内联：短 token + 一份资源表 + 同脚本深遍历还原 | 已实现 | 独立记录 |
| [034](./ADR-034-bundle-page-image-webp.md) | 成品包页图转有损 WebP q75：外部 ffmpeg 编码、逐页择优、失败退回 JPEG | 已实现 | 独立记录（含对比实验） |

## 为什么 001–016 没有独立文件

它们是在 V4.0 成稿时一并定下的，摘要表里的「决策 + 重新评估条件」两列就是当时记录的全部内容。
补写独立记录不是抄一遍摘要：真正有价值的是**当时的备选方案与被否掉的理由**，那部分没有留存，
事后补写只会变成用现在的结论倒推过去的推理。所以这里保持原样，把「只有摘要」这件事写明，
而不是造出一批看起来完整、实际是事后编的记录。

需要独立成篇的门槛：决策会影响多处代码、且重估条件可能在半年内触发（018、019 属于此类）。

## 与代码的对应

代码注释里出现 ADR 编号时，指向的是**决策本身**而不是某个文件：

- `ADR-014` → 对象存储端口与多后端（`internal/integrations/objectstore/port.go`、`s3/s3.go`）
- `ADR-018` → `ppts_scheduler` 角色、`ppts_claim_next_job`、worker 双连接
- `ADR-019` → `ppts_migrator` / `ppts_app` 角色分离、`set_config('app.tenant_id')`

新增独立 ADR 时：编号接着 019 往后排，文件名 `ADR-0NN-短横线小写标题.md`，
并在上表补一行——**不要只加文件不改索引**，索引是唯一能看出「哪些决策只有摘要」的地方。
