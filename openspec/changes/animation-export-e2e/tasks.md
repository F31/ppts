# 任务：导出接线与逐页动画编辑（M3）

> 顺序即合并顺序（§10.4 PR4 → PR5）。第 1 组可在没有渲染服务、没有 export 改动的情况下
> 独立合入并验证；第 2 组才碰 `export.go` 主路径。

## 1. 媒体层：分段拼接与静态段（PR4-a）

- [ ] 1.1 `media.VideoSegment` + `ConcatOptions`：分段拼成整片，支持可选音频、可选字幕烧录
- [ ] 1.2 `compileConcatArgs` 纯函数：三条路径（copy / 字幕重编码 / copy 失败回退重编码）
      的参数构造，**不 exec**，保证在没有 ffmpeg 的机器上也能单测
- [ ] 1.3 `ConcatSegments`：写 concat 清单 → 执行 → ffprobe 校验（时长 / 分辨率 / 是否有音轨）；
      `-map 0:v:0` 只取视频流，避免把降级段的静音轨拼进去
- [ ] 1.4 `EncodeStillSegment`：把单页静态图编码成与动画段同参数的 mp4（复用既有编码预设），
      供单页降级替换
- [ ] 1.5 逐段时长校验：拼接前 ffprobe 每段，累计偏差 > 100ms 的页标记为待降级
- [ ] 1.6 单测：纯函数路径断言（不依赖 ffmpeg）+ 真拼接用例（ffmpeg 缺失时 Skip，
      与 `mp4_test.go` 同一惯例）

## 2. 导出接线与开关（PR4-b）

- [ ] 2.1 `ExportSnapshot` 增加动画字段（`omitempty`），并加一条「开关关闭时 snapshotHash
      与今天逐字节一致」的回归用例
- [ ] 2.2 `internal/app/animation.go`：编排 `compile → submit → wait → 下载分段`，输出
      分段列表与降级页集合
- [ ] 2.3 `renderMP4` 插入动画通道：`tryAnimation` 三态（ADR-21），失败不进入
      `classifyEncodeError`
- [ ] 2.4 单页降级：失败页替换为静态段（ADR-23），成片标注页码与降级原因
- [ ] 2.5 `animation.enabled` 开关接线；关闭时不探测、不提交、不改变静态路径
- [ ] 2.6 表驱动用例：服务不可达 / 单页失败 / 全部失败 / 开关关闭 四种情形，断言
      「都得得到视频」且产物格式正确

## 3. 逐页编辑与预览（PR5）

- [ ] 3.1 `web/src/animationState.ts`：草案 / 保存 / 冲突（复用 `scriptState` 模式）
- [ ] 3.2 `web/src/AnimationEditor.tsx`：三级入口第一级（预设 / 强度 / 逐条要点改锚点）
- [ ] 3.3 单页预览：按 Q1 只提交该页 IR，≤ 3s 返回；超时给明确提示而非空转
- [ ] 3.4 `ExportDialog` 增加「动画视频」选项 + 能力探测（服务不可用时置灰并说明）

## 4. 验收（按 §15.2 演示脚本）

- [ ] 4.1 导入真实 PPT → 选「科技动感」强度 1.1 → 第 2 页第 3 条要点改 `withSegment` 到第 4 段
- [ ] 4.2 预览该页，确认要点在第 4 段开始时出现
- [ ] 4.3 导出 1080p，校验时长与静态版一致（≤ 100ms）、字幕位置不变、音画同步、无空洞/残影
- [ ] 4.4 关闭 `animation.enabled` 再导出，确认回到静态版且无报错
- [ ] 4.5 故障注入：停掉渲染服务导出，确认拿到静态版且提示明确

## 5. 文档

- [ ] 5.1 `deploy/renderer/README.md`：降级矩阵与「什么情况下用户拿到静态版」
- [ ] 5.2 归档时把能力并入 `openspec/specs/animation-spec/`（沿用已有能力路径，不新建）
