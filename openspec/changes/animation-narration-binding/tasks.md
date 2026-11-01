# 任务：讲稿绑定预填

> 状态：**第 1–4 组已落地**（来源标注 + 规则绑定器 Go 移植 + 金标复现 + 预填入口 + 编辑器复核）。
> 规格里的三条 MUST（来源/置信度、分档呈现、按来源撤销）都已兑现。
> 仍不归档：`design.md §7` 把「带配音 30s 样片」列为验收口径，5.1 未做（需要真实配音），
> 6.x 两项是数据缺口。**归档前必须先把 5.1 的证据补上**——规格生效而验收没做，
> 等于把"实现了"写成"验证过了"。

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

## 5. 验收（5.1 未完成）

- [ ] 5.1 带配音 30s 样片，逐段核对「讲到第 N 段时第 N 条才出现」（开口前 400ms 内区域变化为 0）
- [x] 5.2 `go build/vet/test` + `npm run build` + `build:share` 无漂移
      —— `go build ./...`、`go vet ./...`、`go test -p 1 ./...` 全绿；
      `npm run build`（contrast / api:check / route:check / core / polling / animation 51 用例 /
      tsc / vite）与 `npm run build:share` 均通过；`npm run i18n:check` 1364/1364 对等、无新增孤儿

## 6. 阻塞（数据缺口，不是代码缺口）

- [ ] 6.1 真人讲稿重测：回答「0.972 是否自证」
- [ ] 6.2 模型通道对照：门槛是纯文字页明显超过 0.533
