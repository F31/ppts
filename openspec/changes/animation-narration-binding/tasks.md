# 任务：讲稿绑定预填

> 状态：**第 1、2 组已落地**（来源标注 + 规则绑定器 Go 移植 + 金标复现），3–4 组未开始。
> 本变更暂不归档——归档意味着规格生效，而规格里的「预填入口」与「编辑器按来源撤销」
> 两条 MUST 还没有实现。

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

## 3. 预填入口（未开始——需要先定「在哪儿触发」）

- [ ] 3.1 预填不覆盖人工绑定；用户清空后不回填
- [ ] 3.2 只写 `withSegment:<segmentId>`，MUST NOT 写 `atTime:`
- [ ] 3.3 分段缺失走既有 `DiagSegmentUnresolved`，不另造降级逻辑

> 阻塞点不是代码：`animdefault.buildTracks` 现在按「第 N 条要点 → 第 N 段」分配
> （`preset.SegmentAligned`），这与规则预填是**两套语义**。要么预填取代它（未绑上的元素
> 退回静态），要么二者并存由预设开关切换——这是产品决策，定下来才能写 3.x。

## 4. 编辑器（未开始）

- [ ] 4.1 按来源筛选、按来源批量撤销
- [ ] 4.2 置信度升序呈现；低置信预填以不同样式标识
- [ ] 4.3 批量撤销后清除来源标注

## 5. 验收（未开始）

- [ ] 5.1 带配音 30s 样片，逐段核对「讲到第 N 段时第 N 条才出现」（开口前 400ms 内区域变化为 0）
- [ ] 5.2 `go build/vet/test` + `npm run build` + `build:share` 无漂移
      —— Go 侧本轮已全绿（animation/bind/render/app/api 串行），前端未动故未跑

## 6. 阻塞（数据缺口，不是代码缺口）

- [ ] 6.1 真人讲稿重测：回答「0.972 是否自证」
- [ ] 6.2 模型通道对照：门槛是纯文字页明显超过 0.533
