# 任务：导出接线与逐页动画编辑（M3）

> 顺序即合并顺序（§10.4 PR4 → PR5）。第 1 组可在没有渲染服务、没有 export 改动的情况下
> 独立合入并验证；第 2 组才碰 `export.go` 主路径。

## 1. 媒体层：分段拼接与静态段（PR4-a）

- [x] 1.1 `media.VideoSegment` + `ConcatOptions`：分段拼成整片，支持可选音频、可选字幕烧录
- [x] 1.2 `compileConcatArgs` 纯函数：三条路径（copy / 字幕重编码 / copy 失败回退重编码）
      的参数构造，**不 exec**，保证在没有 ffmpeg 的机器上也能单测
- [x] 1.3 `ConcatSegments`：写 concat 清单 → 执行 → ffprobe 校验（时长 / 分辨率 / 是否有音轨）；
      `-map 0:v:0` 只取视频流，避免把降级段的静音轨拼进去
- [x] 1.4 `EncodeStillSegment`：把单页静态图编码成与动画段同参数的 mp4（复用既有编码预设），
      供单页降级替换
- [x] 1.5 逐段时长校验：拼接前 ffprobe 每段，累计偏差 > 100ms 的页标记为待降级
- [x] 1.6 单测：纯函数路径断言（不依赖 ffmpeg）+ 真拼接用例（ffmpeg 缺失时 Skip，
      与 `mp4_test.go` 同一惯例）

## 2. 导出接线与开关（PR4-b）

- [x] 2.1 `ExportSnapshot` 增加动画字段（`omitempty`），并加一条「开关关闭时 snapshotHash
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
- [ ] 5.2 归档时把能力并入 `openspec/specs/animation-export/`（本变更新建的能力路径；
      `animation-spec` 管规格域，导出通道是另一个关注点，不塞进同一个 spec）

## 落地状态（2026-10-29）

第 1 组已落地（`internal/media/segments.go`，提交 87b4f11 / 修复 a488376）：

- 纯函数用例本机跑（无 ffmpeg，只验参数构造）；真拼接在 CI 与 WSL 两处跑过。
  WSL（ffmpeg 6.1.1）整包 2.4s 全绿；CI run 40798181655 的 13 个 job 全绿。
- 一次真实红灯值得记下来：`TestEscapeConcatPath` 断言了 `filepath.ToSlash` 会把
  `C:\tmp\a.mp4` 转成 `C:/tmp/a.mp4`，而它在 Linux 上是恒等函数。**错的是用例**——
  跨平台断言只能写「引号闭合」这类与路径风格无关的性质（a488376）。
- 下一步是第 2 组：`export.go` 接线。在那之前 `renderMP4` 完全不受影响，
  所以本组的合并对静态导出零风险。

第 2.1 组已落地（`ExportSnapshot.Animation` + 快照键回归用例）：

- 字段：`Animation *AnimationOptions`，内含 `Specs []AnimationSpecRef`
   （`slideId / language / sourceRevisionNo / revision`）。没有 preset / intensity——
  它们已经在规格自身的字段里，重复记录会让同一份输入有两种写法、两个键。
- 字节：抽出 `app.SnapshotBytes` 作为唯一来源，Connect 入口 / REST 入口 / 导出步骤共用；
  空 `AnimationOptions` 归一到 nil（都不渲染动画、产物同源）。
- 回归用例 `internal/app/export_snapshot_test.go` 断言的是**字节**而不是哈希：
  三条字面量取自加字段之前的真实序列化（探针打印），失败信息能直接看出哪个字段插进来或跑掉了。
- 用例有效性做了变异验证：
  | 变异 | 结果 |
  |---|---|
  | 去掉 `json:"animation,omitempty"` | 红（多出 `"animation":null`）✅ |
  | 把 nil 指针字段插到 `fps` 与 `burnSubtitles` 之间 | **绿**——被省略的字段不进字节，位置无关 |
  | 去掉空动画归一 | 红（同键被拆成两个）✅ |
  第二条变异推翻了我原本写进注释的「新字段只能追加到末尾」：真正的约束是「零值必须被省略」。
