package bind

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// 模型通道：把一页的元素与讲稿段落交给模型，只换回「哪个元素属于哪一段」的 id 映射。
//
// 端口刻意做得这么窄，理由来自实测（scripts/fidelity/README.md「模型通道对照」）：
//   - 模型看不见版面，容器、配图、卡片底板它一律漏，所以它不能替代规则，
//     只能补规则失灵的那一档（版面有结构、讲稿没把结构念出来：模型 0.833 / 规则 0.667）；
//   - 结构化页上规则是 0.972，模型 0.772，调用它只会拉低结果并多花十几秒；
//   - 模型会无视「规则已绑的元素」这条约束（实测越界 49/30 条），所以逐元素合并不可行，
//     路由到模型时必须整页换掉规则结果，再让规则做结构收尾（见 route.go）。
//
// 因此本文件只负责「问」与「审」：怎么问（提示词）、回来的东西哪些不能收（校验）。
// 什么时候问、问完怎么和规则拼接，在 route.go。

const (
	// PromptVersion 进缓存键。改了提示词却不改版本，旧缓存会继续冒充新提示词的结果——
	// 这类错误在报告上表现为「改了提示词但指标一动不动」，很难自查。
	PromptVersion = "bind-v1"

	// 单元素 / 单段截断。真实讲义里有整段正文塞进一个 shape 的，全量喂会把单页 prompt
	// 顶到几千 token，而超出这一段的文字对「绑到哪一段」几乎不增信息。
	maxElemChars = 120
	maxSegChars  = 400
)

// ModelCompleter 是模型通道唯一的依赖面：给一段提示词，还一段文本。
//
// 不在这里做 HTTP：重试、超时、鉴权属于适配器（internal/integrations/llm），
// 校验与拼接属于这里。两边分开，离线对账才能只用一份假实现跑完所有分支。
type ModelCompleter interface {
	Complete(ctx context.Context, req ModelPrompt) (ModelCompletion, error)
}

// ModelPrompt 是一页的完整输入。System 与 User 分开，是因为缓存键要分别计入：
// 只改系统提示词而不改用户输入，缓存同样必须失效。
type ModelPrompt struct {
	System string
	User   string
}

// ModelCompletion 是适配器回传的原始文本与用量。Text 不做任何修剪——
// 「模型包了 markdown 代码块」这类形态要在校验侧统一处理，而不是每个适配器各剥一次。
type ModelCompletion struct {
	Text              string
	Model             string
	ProviderRequestID string
	PromptTokens      int64
	CompletionTokens  int64
}

// CompletionCache 是模型输出的缓存端口。
//
// 为什么缓存是**正确性**的一部分而不只是省钱：实测同一提示词、temperature=0、
// top_p=1、json_object 连跑三次，模型分别给出 6 / 11 / 7 条绑定。确定性只能靠缓存拿，
// 不能靠采样参数拿。缓存键把模型、提示词版本、采样序号与输入全部算进去，
// 同一个键必须回同一段文本，否则「重跑一次数字就变」会重新出现。
type CompletionCache interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Put(ctx context.Context, key, text string) error
}

// 模型输出被拒的原因。前五类是格式问题（模型没按约定说话），后三类是语义问题
// （话说对了，但这几条不该绑）。两类分开报：格式合法率高而语义越界多，
// 说明提示词有效但页面上有它不该碰的东西；反过来则是输出不稳定。
const (
	RejectNotObject    = "not_object"
	RejectBadElementID = "bad_element_id"
	RejectBadSegmentID = "bad_segment_id"
	RejectDuplicate    = "duplicate"
	RejectBadConf      = "bad_confidence"
	RejectRuleLocked   = "rule_locked"
	RejectAuxElement   = "aux_element"
	RejectNoText       = "no_text"
)

// FormatRejects / SemanticRejects 是上面两类的固定顺序。
var (
	FormatRejects   = []string{RejectNotObject, RejectBadElementID, RejectBadSegmentID, RejectDuplicate, RejectBadConf}
	SemanticRejects = []string{RejectRuleLocked, RejectAuxElement, RejectNoText}
)

// modelSystemPrompt 与 scripts/fidelity/bind_llm.py 的 SYSTEM_PROMPT 逐字一致。
//
// 两边必须同步改：离线实验量的是这条提示词的效果，产品里换一句就等于换了一个被测对象，
// 而报告上的数字还是旧的。对账脚本（scripts/fidelity/llm_check.sh）会因为提示词哈希
// 不匹配而停下来。
const modelSystemPrompt = `你在做一件很窄的事：把幻灯片页面上看得见的元素，绑到讲解稿的段落上。

规则：
1. 只能使用我给你的 elementId 与 segmentId，不许创造新的 id。
2. 每个元素最多绑一个段落；每个段落可以绑多个元素。
3. 不要绑页码、页眉页脚、图注、资料来源、联系方式、版权声明这类元信息。
4. 不要绑没有文字、也没有图形的纯装饰块（铺底背景、边角色块）。
5. 讲解顺序大体就是元素的阅读顺序；但一句话同时讲到几件东西时，允许多个元素绑到同一段。
6. 宁可少绑，也不要猜。拿不准的元素就不要出现在结果里。

只输出一个 JSON 对象，形状严格如下，不要任何解释文字、不要 markdown 代码块：
{"bindings":[{"elementId":"...","segmentId":"...","confidence":0.0}]}
confidence 是你自己的把握（0~1）。它不会被当作准确率展示给用户，只用来排序复核。`

// modelPromptFor 构造一页的输入。
//
// 行式而不是 JSON：省 token，也少一层模型需要遵守的嵌套结构。元素顺序用**阅读序**——
// 与规则侧同一份（Run 入口会重排），否则模型看到的顺序和规则看到的不一样，
// 「讲解顺序大体就是阅读顺序」这条规则在两边指的就不是一件事。
func modelPromptFor(slideID string, elements []Element, segments []Segment) ModelPrompt {
	var b strings.Builder
	b.WriteString("SLIDE ")
	if slideID == "" {
		b.WriteString("?")
	} else {
		b.WriteString(slideID)
	}
	b.WriteByte('\n')
	for _, e := range elements {
		text := clipText(e.Text, maxElemChars)
		if text == "" {
			text = "(no text)"
		}
		b.WriteString("E ")
		b.WriteString(e.ID)
		b.WriteString(" | ")
		if e.Kind == "" {
			b.WriteString("shape")
		} else {
			b.WriteString(e.Kind)
		}
		b.WriteString(" | ")
		b.WriteString(text)
		b.WriteByte('\n')
	}
	b.WriteString("SEG\n")
	for _, s := range segments {
		b.WriteString("S ")
		b.WriteString(s.ID)
		b.WriteString(" | ")
		b.WriteString(clipText(s.Text, maxSegChars))
		b.WriteByte('\n')
	}
	return ModelPrompt{System: modelSystemPrompt, User: strings.TrimRight(b.String(), "\n")}
}

// clipText 把换行压成 ` / `，超长截断加省略号。与 bind_llm.py 的 _clip 同口径：
// 换行压平是为了让一个元素在提示词里始终占一行，否则模型会把多行文本当成多个元素。
func clipText(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " / "))
	if n <= 0 || len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// modelValidation 是一次校验的全部产出。
type modelValidation struct {
	// Pairs 只含被接受的绑定，按元素阅读序升序（同段内的错落顺序靠它）。
	Pairs []pair
	// Rejected 按原因计数，八类全部预置为 0：缺键与「跑了、一条没出」在复核时是两回事。
	Rejected map[string]int
	// Items 是输出里的条目数（非 JSON 时记 1）。它是拒绝率的分母——
	// 整页非 JSON 却按 0 条算，拒绝率会变成 0/0=0，「格式合法」会假通过。
	Items int
	// MonotonicViolations 统计「后一条绑到了更靠前的元素」。只统计不强制：
	// 模型可能故意跨段合并，但大量倒退说明它没按阅读顺序看页面。
	MonotonicViolations int
	// ParseOK 表示输出能被当成 JSON 对象且有 bindings 数组。
	ParseOK bool
}

// validateModelOutput 把模型输出收敛成 pair，并回报每条被拒的原因。
//
// allowIDs 非空时只接受这些 elementId（规则已绑的元素对模型只读）。产品路由是
// 「整页换掉规则结果」，传 nil；这条分支留给 M0 里试过的 rule+model 合并形态，
// 它当时被否掉了（模型越界 49/30 条），留着是为了能复现结论而不是给产品用。
func validateModelOutput(raw string, elements []Element, segments []Segment, allowIDs map[string]struct{}) modelValidation {
	v := modelValidation{
		Rejected: make(map[string]int, len(FormatRejects)+len(SemanticRejects)),
		Items:    1,
	}
	for _, k := range FormatRejects {
		v.Rejected[k] = 0
	}
	for _, k := range SemanticRejects {
		v.Rejected[k] = 0
	}

	obj, ok := extractJSONObject(raw)
	if !ok {
		v.Rejected[RejectNotObject]++
		return v
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(obj, &top); err != nil {
		v.Rejected[RejectNotObject]++
		return v
	}
	rawBindings, ok := top["bindings"]
	if !ok {
		v.Rejected[RejectNotObject]++
		return v
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawBindings, &items); err != nil {
		v.Rejected[RejectNotObject]++
		return v
	}
	v.ParseOK = true
	if len(items) > 1 {
		v.Items = len(items)
	}

	elemIdx := make(map[string]int, len(elements))
	for i, e := range elements {
		if _, dup := elemIdx[e.ID]; !dup {
			elemIdx[e.ID] = i
		}
	}
	segIdx := make(map[string]int, len(segments))
	for j, s := range segments {
		if _, dup := segIdx[s.ID]; !dup {
			segIdx[s.ID] = j
		}
	}

	type rawBinding struct {
		ElementID  string   `json:"elementId"`
		SegmentID  string   `json:"segmentId"`
		Confidence *float64 `json:"confidence"`
	}

	seen := make(map[string]struct{}, len(items))
	last := -1
	for _, rawItem := range items {
		trimmed := strings.TrimSpace(string(rawItem))
		if !strings.HasPrefix(trimmed, "{") {
			v.Rejected[RejectNotObject]++
			continue
		}
		var item rawBinding
		if err := json.Unmarshal(rawItem, &item); err != nil {
			v.Rejected[RejectNotObject]++
			continue
		}
		ei, ok := elemIdx[item.ElementID]
		if !ok {
			v.Rejected[RejectBadElementID]++
			continue
		}
		si, ok := segIdx[item.SegmentID]
		if !ok {
			v.Rejected[RejectBadSegmentID]++
			continue
		}
		if _, dup := seen[item.ElementID]; dup {
			v.Rejected[RejectDuplicate]++
			continue
		}
		if allowIDs != nil {
			if _, ok := allowIDs[item.ElementID]; !ok {
				// 与 duplicate 分开记：「重复输出了一个元素」是格式问题，
				// 「想覆盖规则已经绑好的元素」说明它不守约束——混在一起就看不出来了。
				v.Rejected[RejectRuleLocked]++
				continue
			}
		}
		if isAux(elements[ei]) {
			v.Rejected[RejectAuxElement]++
			continue
		}
		if strings.TrimSpace(elements[ei].Text) == "" {
			v.Rejected[RejectNoText]++
			continue
		}
		conf := 0.0
		if item.Confidence != nil {
			conf = *item.Confidence
			if conf < 0 || conf > 1 {
				v.Rejected[RejectBadConf]++
				continue
			}
		}

		seen[item.ElementID] = struct{}{}
		v.Pairs = append(v.Pairs, pair{elem: ei, seg: si, conf: conf})

		if ei < last {
			v.MonotonicViolations++
		}
		if ei > last {
			last = ei
		}
	}

	sortPairsByElem(v.Pairs)
	return v
}

// extractJSONObject 剥掉一层 markdown 代码块，其余不做模糊修补。
//
// 「尽力从一段废话里抠出 JSON」是另一种危险的绿：抠出来的东西可能只是被截断的前半段，
// 校验会把它当成合法映射。只承认「整段就是 JSON」这一种形态。
func extractJSONObject(raw string) ([]byte, bool) {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if j := strings.Index(rest, "```"); j >= 0 {
			inner := strings.TrimSpace(rest[:j])
			inner = strings.TrimSpace(strings.TrimPrefix(inner, "json"))
			s = inner
		}
	}
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return nil, false
	}
	return []byte(s), true
}

// modelCacheKey 把「模型 + 提示词版本 + 采样序号 + 提示词全文」压成一个键。
//
// 采样序号必须进键：temperature=0 在实测里并不确定（同一提示词三次 6/11/7 条），
// 不同采样序号代表「另一次抽样」，混用会让多轮实验全落到第一次的结果上。
func modelCacheKey(model, promptVersion string, sample int, system, user string) string {
	h := sha256.New()
	for _, part := range []string{model, promptVersion, strconv.Itoa(sample), system, user} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	// 32 位十六进制与 bind_llm.py 的 cache_key 同宽，便于两边对同一份输入核对键。
	return hex.EncodeToString(h.Sum(nil))[:32]
}
