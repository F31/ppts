# 任务：讲稿绑定预填

> 状态：**第 1–5 组已全部落地**（来源标注 + 规则绑定器 Go 移植 + 金标复现 + 预填入口 +
> 编辑器复核 + 带配音样片验收）。规格里的三条 MUST（来源/置信度、分档呈现、按来源撤销）
> 都已兑现，5.1 的像素证据也已补上，本变更随本轮归档。
> 6.x 两项是**数据缺口**（真人讲稿重测、模型通道对照），非代码缺口，不阻塞规格生效，
> 已转入路线图作为后续跟进。

## 1. 来源标注

- [x] 1.1 `animation.Clip` 增 `TriggerSource`（`triggerSource,omitempty`），白名单 8 值；未知值返回 400 并列出允许取值
      —— `internal/animation/model.go`（7 条规则名 + `manual`，`triggerSources` 有序声明）、
      `internal/animation/validate.go:validateClipTriggerSource`；
      测试 `trigger_source_test.go:TestUnknownTriggerSourceRejected`（断 400、字段名、Hint 含全部 8 值）
- [x] 1.2 单测固定字节不变：无标注的既有规格序列化与 `planHash` 均与引入前一致
      —— `trigger_source_test.go:TestClipJSONUnchangedWithoutTriggerSource`（钉死整串字节）、
      `internal/render/trigger_source_test.go:TestTriggerSourceDoesNotAffectPlan`
      （同一份规格带/不带标注，`CanonicalJSON` 与 `Hash` 逐字节相等）
- [x] 1.3 用户修改预填绑定的 `trigger` 后来源置为 `manual`，MUST NOT 保留原规则名
      —— `internal/animation/source.go:ReconcileTriggerSources`（PUT 前与上一版逐条比对，
      匹配键 = 元素 id + clip id，无 id 时退回位置），接入 `internal/api/animation.go:put`；
      测试 `source_test.go` 7 条。已知边界：前端若给改过的 clip 换新 id，服务端匹配不上，
      来源保持原值（按位置猜会在增删 clip 时把结论安错元素）——该边界归 tasks 4.3 的编辑路径处理

## 2. 规则绑定器 Go 移植

- [x] 2.1 落点：`app.PageElements` 之上（输入 `*project.Page` + segments，输出 elementID → segmentID）
      —— `internal/app/bind_elements.go:PageBindElements`（走 `project.Shape` 自己的投影，
      不经过 `render.Element`：文本与无文本容器都要，而 `PageElements` 两者都丢）；
      测试 `bind_elements_test.go:TestPageBindElementsMatchesExperiment`
      （解析真实 pptx，与实验脚本的 71 个元素分类、包围盒**逐元素一致**）
- [x] 2.2 相似度三路：bigram Dice 0.5 / LCS 覆盖率 0.3 / token 命中 0.2；标题 ×1.15；中文数字归一（只动两位以上带单位的串）
      —— `internal/animation/bind/text.go`
- [x] 2.3 单调 DP 对齐（`bind` 分支不推进 j，一段可带出多个元素）
      —— `internal/animation/bind/align.go`
- [x] 2.4 五条结构规则（辅助白名单 / 数量词→编号组 / 第N条→第N块 / 方位指代 / 同块带出）+ 两条兜底（标题先验、结构推断收紧版）
      —— `internal/animation/bind/rules.go`，每条规则一个单测（`rules_test.go`）
- [x] 2.5 版面分档：编号标记 / 卡片簇 / 方位覆盖 → 无信号页标记低置信
      —— `internal/animation/bind/band.go`。**实现修正**：方向覆盖单独命中不判结构化
      （实测恒真，留在 `or` 里等于废掉分档），详见 design.md §2「实现修正」
- [x] 2.6 金标复现：`sample-customer-deck.pptx` 1/2/4/8/10 页 + `q8-script.json`，内容召回 ≥ 0.972、精确 ≥ 0.962
      —— `bind_golden_test.go:TestGoldenQ8`：exact 51 / wrong_seg 1 / missed 0 / extra 1 /
      both_skip 18，内容召回 **0.972**、精确 **0.962**，来源分布 18/10/11/1/9/0/4 与 Python 报告逐项相同
- [x] 2.7 确定性：同一输入连续调用两次，结果逐条相等；`-count=5` 不抖动
      —— `rules_test.go:TestDeterministic`（5 页 × 6 次 `reflect.DeepEqual`）；
      整套 bind 测试 `-count=5` 通过

## 3. 预填入口

- [x] 3.1 预填不覆盖人工绑定；用户清空后不回填
      —— 由读路径的结构天然保证：`GET .../animation` 一旦在存储里找到规格就原样返回，
      `?default=true` 只在 `ErrNotFound` 时才走到生成（`internal/api/animation.go:get`），
      所以预填**没有**可以覆盖的对象。测试 `animation_prefill_test.go:TestPrefillNeverOverridesSavedSpec`
      （PUT 人工绑定 → 带 `prefill=true` 取默认 → 返回 revision 1 与 `withSegment:seg-2`；
      再 PUT 空轨道 → 取回仍是 0 条，没有被回填）
- [x] 3.2 只写 `withSegment:<segmentId>`，MUST NOT 写 `atTime:`
      —— `internal/animation/animdefault/defaults.go:buildBoundTracks`（触发器由
      `TriggerSegmentPrefix + b.SegmentID` 拼出，没有任何绝对时间分支）；
      测试 `bound_test.go:TestBoundTracksUseSegmentTriggers`（逐条断前缀）、
      `internal/app/prefill_e2e_test.go:TestPrefillPipelineOnRealDeck`（真实 5 页 45 条 clip 全是相对锚点）
- [x] 3.3 分段缺失走既有 `DiagSegmentUnresolved`，不另造降级逻辑
      —— 分两种情形分开处理：**分段清单为空**时跳过预填并说明
      （`prefill.applied=false, reason="no_segments"`，轨道退回既有默认规格，不产生任何
      `withSegment`——没有分段就没有锚点可编）；**绑定指向不存在的分段**只可能来自调用方
      拼错输入，直接报错而不是降级（`bound_test.go:TestBindingToUnknownSegmentFails`）。
      运行期讲稿改稿导致锚点失效的路径没有变：编译期 `resolveTrigger` 出
      `DiagSegmentUnresolved`、保存期 V7 出 409

- [x] 3.4 每条预填 clip 带连续置信度（规格「可解释」一条的后半句）
      —— `animation.Clip` 增 `confidence`（omitempty，0 = 无置信度：非字面规则或非预填产物），
      `validateClipConfidence` 拒收 0..1 之外的值；`buildBoundTracks` 写入 `bind.Binding.Score`。
      测试 `trigger_source_test.go:TestClipJSONOmitsZeroConfidence` / `TestConfidenceOutOfRangeRejected`、
      `bound_test.go:TestBoundTracksCarrySourceAndStagger`

> **入口形态（本轮定的）**：`GET .../animation?default=true&prefill=true`。显式开关，
> 不加参数时产物与引入前逐字节相同（`TestDefaultSpecWithoutPrefillFlagIsUnchanged`
> 断响应里没有 `prefill` 字段）。`DefaultOptions.Bindings` 为 nil 走既有顺序分配，
> 非 nil（哪怕零条）走绑定，二者不混用。
>
> **实测（真实样本 1/2/4/8/10 页）**：绑定 53 条 → 轨道 45 条。差 8 条是绑在
> 无文字容器上的（`structural`），business-calm 不给这类元素挂动效——绑定成立、模板不存在。
> 数字钉在 `TestPrefillPipelineOnRealDeck` 里，改绑定器或预设就要重定基线。

## 4. 编辑器（已落地）

- [x] 4.1 按来源筛选、按来源批量撤销
      —— 纯状态层 `web/src/animationPrefill.ts`（`prefillBindings` / `sourceGroups` /
      `filterBySource` / `reviewOrder` / `revokeSource` / `revokeBinding`），
      界面在 `web/src/AnimationEditor.tsx` 的「预填复核」区块：来源 chip 分组 + 行内「改为页首」+
      选中某来源后的批量撤销。
      入口按钮「按讲稿自动绑定」只在 `saved === null` 时可用——预填产出的是「这一页的起点」，
      对已有规格跑一遍会整体换掉人工确认的绑定（规格「人工权威」），所以禁用并说明原因。
      测试 `web/animation-check.mjs`：预填表只列带来源的入场 clip / 来源分组顺序固定 /
      撤销一个来源 / 撤销单条不动强调 clip / 撤销不存在的来源是空操作 / 未登记来源不硬套文案
- [x] 4.2 置信度升序呈现；低置信预填以不同样式标识
      —— **实现修正**：纯置信度排序不成立。`bind.Binding.Score` 的定义是字面相似度，
      非字面规则（计数/序数/方位/同块带出/先验/结构）恒为 0，且这是刻意的
      （`bind.go:43` 注释：硬塞一个数会让人误以为可以排序）。六条规则全压在 0 上，升序等于没排。
      改为「复核优先级」两级排序：`sibling` 最先（唯一会一次带出一整片的规则，
      `animdefault/defaults.go:192` 原话「先看同块带出的那 9 条」）→ 其余无字面依据的规则按固定
      顺序 → `text` 按相似度升序 → `manual` 最后；同层按元素 id 兜底保证顺序稳定。见 design.md §10.1
      呈现：无置信度的行显示 `—` 而非 `0`（`hasConfidence` 区分「无依据」与「0 分」）；
      低置信页整块换警示底色，并写出实测数字（0.533 对 0.972）
      已知边界：`band` 不是规格字段，重开面板后只剩来源与置信度，低置信标识不再出现（§10.5）
- [x] 4.3 批量撤销后清除来源标注
      —— `revoke*` 同时 `delete triggerSource/confidence`、trigger 改回 `onEnter`、`startMs` 归零
      （留着 withSegment 时代的 stagger 会让时间轴色块停在与实际不符的位置）。
      **clip 本体保留**：撤销的是绑定不是动效；删掉 clip 等于砍掉入场，而界面里没有"加一条 clip"
      的入口，撤销就成了一次不可逆的破坏。
      落库结果由服务端 `ReconcileTriggerSources` 定为 `manual`（上一版来源是规则名 + trigger 变了），
      钉在 `internal/animation/source_test.go:TestReconcileRevokedPrefillBecomesManual`
      人手改段号走同一判据（`withTrackTrigger` 清标注），复核表当场更新，不必等保存后再刷新

> **顺手修掉的两个既有缺陷**（都属于"门禁看起来在守、实际没守住"）：
>
> 1. `isDirty(draft, null)` 返回 `false`：**新建页面根本存不下去**——选了预设之后保存按钮
>    一直是灰的；预览也静默什么都不做（`runPreview` 里 `current = saved` 为 null 直接 return，
>    连错误提示都没有）。现在没有已保存版本即算脏，`web/animation-check.mjs` 钉住。
> 2. `savePayloadOf` 不回传 `triggerSource`/`confidence`：用户改一下强度再保存，来源标注被
>    静默抹掉（服务端只在 trigger 变化时补 `manual`，这条路径 trigger 没变）。现在原样带回。

## 5. 验收

- [x] 5.1 带配音 30s 样片，逐段核对「讲到第 N 段时第 N 条才出现」（开口前 400ms 内区域变化为 0）
      —— 一条命令跑完：`bash scripts/fidelity/prefill_sample.sh`（配音复用 `tmp/q7` 的
      edge-tts 产物，5 页 35.5s；从讲解词重打见 `narrate.py`）。
      链路：`plan -prefill`（**产品代码**：`animation/bind` + `animdefault.SpecFromElements`）→
      `cmd/renderer`（Remotion，服务端生成底板）→ 拼接混音 → `check_speech_gate.py`。
      **实测 18 条绑定：preMax 全为 0.0（开口前 400ms 内区域变化为 0），
      delay 13–467ms 全在 900ms 预算内，18 通过 / 0 不过**。逐条数字见
      `tmp/prefill-sample/speech-gate.json`（产物不入库，脚本可重跑）。
      负对照（把开口后移 400ms 再判）**16/18 判成提前，比例 0.89**——尺子量得出提前，
      不是「宽到什么都过」。两条没被抓的是 500ms 级别的慢动画（图表容器淡入、柱子生长），
      在假开口那一刻只走了 60–80%，灰度差还没越过 2.0；把阈值收紧到能抓它们，
      正常判据就会开始误报，所以负对照的合格线定 80% 并列出漏网项。
      不进 CI：要 TTS 网络、Chrome、**完整版 ffmpeg**（Remotion 自带的那份没有 rawvideo 输出）。
      > **踩坑留痕**：直接调 `renderer/render.mjs` 渲这份计划，18 条里 11 条测不到出现——
      > 底板（plate）是 Go 侧 staging 生成的，绕过它，淡入的层只是在整页底图上又叠了一遍
      > 自己，像素差恒为 0。必须走 `cmd/renderer`。已写进 fidelity README。
- [x] 5.2 `go build/vet/test` + `npm run build` + `build:share` 无漂移
      —— `go build ./...`、`go vet ./...`、`go test -p 1 ./...` 全绿；
      `npm run build`（contrast / api:check / route:check / core / polling / animation 51 用例 /
      tsc / vite）与 `npm run build:share` 均通过；`npm run i18n:check` 1364/1364 对等、无新增孤儿

## 6. 阻塞（数据缺口，不是代码缺口）

- [ ] 6.1 真人讲稿重测：回答「0.972 是否自证」
      **可算的一半已经做完**（`scripts/fidelity/ablate_structure.py`，表与结论见 fidelity
      README 的「结构词消融」一节）。把讲稿里的 13 处结构词（序数 8 / 数量 3 / 空间 2）
      删掉、golden 一字不动，内容召回 **0.972 → 0.667**、精确 0.962 → 0.902；同一份扰动
      讲稿喂给 Go 实现（`bind.Run`）逐项相同（37/3/12/1）。所以 0.972 里有 **0.305 是
      「讲稿替你把版面念出来」**，真人场景的预期该报 **0.67–0.75**，「几个部分」这类模糊
      数量与整句不说等价（规则只认精确数字）。
      仍然缺的是真人样本本身：真人会换顺序、省整块、插题外话，这三种失效不在删词覆盖内，
      真实值可能比 0.667 更低。
      顺带确认的**产品缺陷已修**（变更 `2026-11-01-animation-prefill-script-signal`）：
      分档以前只读元素几何、不读讲稿，于是「版面有编号/卡片但讲稿没有结构词」这一格被判成
      `structured` 且不给低置信提示，而实测召回是 0.667。现在 `layoutOK && scriptOK` 才判
      结构化，低置信文案按 `layout` / `script` / `both` 分叉，script 档点名「要改讲解词」。
- [x] 6.2 模型通道对照：结论是「模型不替代规则；按页路由成立，但收益来自规则的确定性，
      不是模型更强」。**首轮的 0.972 已被证伪**——那是 5 次抽样里最好的一次。
      实测（agnes-3.0-flash / agnes 网关，真实 5 页 71 元素，`scripts/fidelity/bind_llm.py --repeat 5`）：

      | 变体 | 召回各轮 | 均值 | 最差 | 极差 | 规则召回/精确 |
      |---|---|---|---|---|---|
      | q8 原稿 | 0.972/0.472/0.583/0.944/0.722 | 0.739 | 0.472 | **0.500** | 0.972/0.962 |
      | q8 去全部结构词 | 0.806/0.972/0.694/0.833/0.861 | 0.833 | 0.694 | 0.278 | 0.667/0.902 |
      | q8 混合（`--mix-pages 1,4`） | 0.833/0.861/0.472/0.944/0.750 | 0.772 | 0.472 | 0.472 | 0.694/0.927 |
      | 混合 + 按页路由 | 0.833/0.972/0.722/0.972/0.861 | **0.872** | **0.722** | **0.250** | 同上 |

      1. **温度 0 不保证确定性**：同一 prompt 连调三次，同一页输出 6 / 11 / 7 条绑定，
         该页召回在 0.091 与 1.000 之间跳。任何单次测量都不是结论，这是首轮结论翻车的根因。
      2. **「补 0.305」更正为「均值补 0.166、最差轮补 0.027」**：去结构词档规则 0.667，
         模型均值 0.833、最差 0.694。收益真实但缩水一半，下界贴着规则，过不了 0.75 门槛。
      3. **不能替代规则（更稳）**：原稿 5 轮全部低于规则 0.972。目录页「一段话概括 11 项」
         规则全绑、模型最好也只绑 1 条——确定性结构推理，模型学不会。
      4. **按页路由成立**：用 `scripts/fidelity/bandcheck`（调产品代码，不在 Python 里复刻分档）
         拿真实分档，`structured`→规则 3 页、`script`→模型 2 页。与全模型共用缓存键做**配对比较**：
         逐轮差值 0.000/+0.111/+0.250/+0.028/+0.111，五轮一次没输给全模型，且每轮都优于全规则。
         判据写进 `route_gate()`，取最差一轮守门。
      5. **收益来源要说清**：不是模型更强，是四步流程页规则 18/18、模型恒为 10/18（漏 8 条容器），
         固定给规则每轮白捡 8 条，方差随之从 0.472 降到 0.250。**代价是精确 0.985 → 0.956**：
         封面页 `clusters` 误判成结构化，规则在那页召回 0.5 / 精确 0.333。
      6. **逐元素合并（rule+model）不可行**：模型无视 `BOUND->` 标记重绑整页（越界 49/30 条，全被拒），
         退化成纯规则；P95 从 11.6s 涨到 57.7s。
      7. **版面本身没结构时模型救不回来**：q7 上 0.353，精确 0.400，比规则还差。

      延迟仍不达标：5 轮并发 P95 28–43s（含 429 重试），结论是**必须异步 job**，
      不能挂在同步的 `?prefill=true` 上。仍未闭环：样本只有 5 页（极差 0.5 需要更多页消化，
      不是加采样轮次）；混合只造了一种消融组合；封面页 `clusters` 误判未修；
      换网关/换模型（2.5-flash 格式错 57.9%、2.5-pro 403）必须重跑。

      | 变体 | 模型召回 | 模型精确 | 规则召回 | 规则精确 |
      |---|---|---|---|---|
      | q8 原稿 | 0.694 | 1.000 | **0.972** | 0.962 |
      | q8 去全部结构词 | **0.972** | **0.975** | 0.667 | 0.902 |
      | q7 口语样本 | 0.353 | 0.400 | 0.353 | 0.750 |

      1. **不能替代**：原稿上模型 0.694 对规则 0.972。目录页一句话概括 11 个元素，
         规则的数量词规则全绑上，模型只绑 1 条——「一段话概括 N 项」规则更强。
      2. **补的正是规则失灵那档**：去全部结构词后规则 0.667、模型 0.972，精确还高 0.073。
         消融测出的「0.305 靠讲稿念结构」，模型正好补回来。
      3. **逐元素合并（rule+model）不可行**：模型无视 `BOUND->` 标记，把整页重绑
         （越界 49/30 条，全被拒），结果退化成纯规则；输入变长后 P95 从 11.6s 涨到 57.7s。
      4. **版面本身没结构时模型也救不回来**：q7 上 0.353，精确 0.400，比规则还差。

      所以产品形态是**按页路由**（`BandReason`）：`structured` 走规则、
      `script` 走模型 + 结构收尾、`layout`/`both` 走规则并给低置信提示。
      延迟不达标（P95 11.7s > 8s），结论是**必须异步 job**，不能挂在同步的 `?prefill=true` 上。
      仍未闭环的部分：没有「两种页混在一份讲稿」的中间样本，按页路由的实际收益未测；
      换网关/换模型（2.5-flash 格式错 57.9%、2.5-pro 403）要重跑，结论不可迁移。