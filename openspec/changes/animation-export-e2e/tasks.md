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

- [ ] 2.0 **底图投递机制（仍未决，是「生产上真正走动画」的前置）**：渲染服务只从自己的
      `-assets-dir` 读 `baseImage`（ADR-14），而页面 PNG 在对象存储里。两者之间目前**没有通路**：
      渲染容器 `network_mode: none`、compose 里 `/assets` 挂载仍是注释状态。需要二选一并落成
      部署契约：
      （a）共享卷 + 路径映射（单机可行；本项目的对象存储在 S3，通常不成立）；
      （b）渲染服务加 `PUT /v1/assets/{path}`，由导出侧推页图（多一次上传，但后端无关）。
      代码侧已把这条通路收敛成一个接缝 `AnimationRequestBuilder`（2.3），
      适配器未注入 = 不探测、走静态，所以这里只差一个实现与一份部署说明。
      证据：`internal/render/assets.go`、`deploy/renderer/compose.yml` 的 `-assets-dir` 注释。

- [ ] 2.0b **元素几何来源（本轮新发现的前置）**：`AnimationPage.Elements` 需要页内元素的
      EMU 框，而导出主路径只有页图与时间轴。几何**是**可得的——`project.reader.go` 在
      Inspect 时已从 go-pptx 取到 `Bounds`——但没有被持久化成解析产物，导出侧无从读取。
      两条路：解析期把 elements 落成对象（一次成本，导出期零成本），或导出期按源版本重解析
      （零存储，每次导出多一次解析）。选哪条取决于「导出频率 vs 存储成本」，未定。
      在此之前动画段只能编出「整页静态画面」——那与静态版等价，不如不做。
      证据：`internal/project/reader.go` 的 `Bounds`、`internal/app/ingest.go` 的 `PageEntry`。

- [x] 2.1 `ExportSnapshot` 增加动画字段（`omitempty`），并加一条「开关关闭时 snapshotHash
      与今天逐字节一致」的回归用例
- [x] 2.2 `internal/app/animation.go`：编排 `compile → submit → wait → 下载分段`，输出
      分段列表与降级页集合
- [x] 2.3 `renderMP4` 插入动画通道：`tryAnimation` 三态（ADR-21），失败不进入
      `classifyEncodeError`
- [x] 2.4 单页降级：失败页替换为静态段（ADR-23），成片标注页码与降级原因
- [x] 2.5 `animation.enabled` 开关接线；关闭时不探测、不提交、不改变静态路径
- [x] 2.6 表驱动用例：服务不可达 / 单页失败 / 全部失败 / 开关关闭 四种情形，断言
      「都得得到视频」且产物格式正确（另加「时长漂移」一档：它是唯一会被**修复**而非放弃的失败）

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

### 第 2.2 落地（`internal/app/animation.go`）

编排 `compile → submit → wait → 下载分段` 已完成，两条用例覆盖最要紧的两件事：
「第 i 段必须是第 i 页」（用可辨识字节验证，不是只看文件存在）与「缺一页不整片失败」。

过程中发现并补掉一个**契约缺口**：渲染服务此前只有 `GET /v1/render/{id}` 返回
`artifacts` 名字列表，**没有任何下载端点**——分段名字拿到了，字节取不回来。
补了 `GET /v1/render/{id}/artifacts/{name}`（服务端 + 客户端 + 5 条用例），
规格增量写在 `animation-render-mvp/specs/render-plan/spec.md`，任务记 4.8。

另外两处刻意的取舍：

- **4:3 页按 16:9 输出 → 编译期 `ErrAspectMismatch`，不静默出歪动画**。
  静态路径是把整页拉伸（元素跟着页面一起变形才自洽），而 EMU→像素是等比换算，
  两者不能混。宁可降级成静态版，也不产出一版元素框错位的动画。
- **`Missing` 与 `Drifted` 分开**：`Missing`（本层，服务端没给这一段）与
  `media.ConcatSegments` 返回的 `Drifted`（时长漂移超 100ms）是两件事，
  但 2.4 的处理相同——都换成静态段。分开记录是为了日志里能看出是哪一类。

### 第 2.3–2.6 落地（`internal/app/animation_video.go`）

接线形状：`renderMP4` 里只有一次 `if av := h.animatedVideo(...); av != nil { return av.File, nil }`，
其余全部保持原位。这个「一次判断 + 一个返回」是刻意的——动画通道若能顺手改动静态路径的任何一行，
它就不再是增强层。

三道闸门（缺一即静态，且**不探测**）：

| 闸门 | 含义 | 谁提供 |
|---|---|---|
| `renderer != nil` | 功能开关。关闭时不建客户端、不建临时目录 | `PPTS_ANIMATION_ENABLED` + `PPTS_RENDERER_URL` |
| `snapshot.Animation.Specs` 非空 | 用户确实要动画（快照即契约） | 2.1 的字段 |
| `animationPages != nil` | 能把页图与几何交出去 | 2.0 / 2.0b 的适配器 |

`tryAnimation` **不返回 error**。这不是省事，是把 ADR-21 变成类型层面的约束：
一旦签名里有 error，调用链上迟早有人把它当业务失败，于是「渲染服务抖一下」变成
「用户的导出失败」。失败原因只走日志，代价是「静默降级」——所以 2.4 的页码留痕是必须的：

- `AnimatedVideo.Degraded` 记录换过静态段的页，`Drifted` 是其中「片段拿到了但时长不准」的子集。
  两者分开是因为修法不同：少给一段是服务端问题，时长不准多半是计划与素材对不上。
- 成片后打一条 warn：`pages=[..] drifted=[..] total_pages=N plan_hash=...`。
  成品行没有字段承载这个信息（要加列得走迁移，不在本变更范围），日志是当前唯一可查的痕迹。

两处刻意的取舍：

- **漂移只修一次**。把漂移页换成静态段重拼；再漂移就整体退回静态版。
  继续重试只是把一次慢失败变成长失败，而成片里要的是音画同步，不是「尽量多动画」。
- **静态段与动画段必须同参数**（同一编码预设）。这是 `-c:v copy` 能成立的前提；
  参数一不一致，整片就从「秒级拼接」退化成「分钟级重编码」（ADR-22）。

用例（`internal/app/animation_video_test.go`，需 ffmpeg，缺失时 Skip）：

| 用例 | 断言 |
|---|---|
| 单页缺失 | `Degraded == [1]`，且不是整片退回（其余页的动画仍然有效） |
| 时长漂移 | `Drifted` 与 `Degraded` 都是两页——证明「换静态段重拼」真的发生 |
| 服务不可达 / 整片失败 / 开关关闭 | 一律返回 nil，不返回 error |
| 导出四种故障 | `Handle` 全部成功，产物头是 `ftyp`（只看 size>0 会被空壳蒙过去） |
| 开关关闭 | 渲染服务计数为 0——「关闭」必须与「没有这个功能」等价 |

变异验证（都在 WSL 真跑）：

| 变异 | 结果 |
|---|---|
| 缺页改为「跳过该页」而不是换静态段 | 红（整片退回静态，`av == nil`）✅ |
| 关掉漂移修复分支 | 红（`Drifted`/`Degraded` 都空）✅ |

测试自身也踩了一个坑：`WriteTimelineWAV` 会逐段校验音频长度与时间轴是否一致，
第一版给每段塞了整片长度的 WAV，直接报 `duration differs from timeline`。
不是测试挑剔，是它的既有契约。
