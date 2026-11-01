package bind

import (
	"context"
	"errors"
	"time"

	"github.com/F31/ppts/internal/animation"
)

// 按页路由：决定这一页的值不值得问模型。
//
// 三种组合的实测结果（scripts/fidelity/README.md「模型通道对照」）：
//
//	分档                          规则    模型    结论
//	structured（版面+讲稿都有结构） 0.972   0.772   规则赢，且模型多花十几秒
//	script（版面有结构、讲稿没念）  0.667   0.833   模型补位，这是唯一该走模型的档
//	layout/both（版面本身没结构）   0.533   0.353   都救不回来，继续用规则并给低置信提示
//
// 把 layout/both 也路由给模型是这里最容易犯的错：它看起来「更彻底」，实测是拿 0.353
// 换掉规则的 0.533，还多等十几秒。ROUTE_MODEL 只放 script，理由就是这个。

// Route 是这一页实际走的通道。
type Route string

const (
	// RouteRule 覆盖 structured 与 layout/both：前者规则更准，后者模型也救不回来。
	RouteRule Route = "rule"
	// RouteModel 只在 script 档、且模型确实给出了可用结果时出现。
	RouteModel Route = "model"
)

// 模型没参与（或参与后整页退回规则）的原因。空串表示模型正常产出。
const (
	ModelFallbackNoModel     = "no_model"          // 没配模型，或这一页不该问
	ModelFallbackTimeout     = "timeout"           // 超时
	ModelFallbackError       = "error"             // 网关/鉴权/连接错误
	ModelFallbackInvalidJSON = "invalid_json"      // 输出不是「整段一个 JSON 对象」
	ModelFallbackRejected    = "no_valid_bindings" // 能解析，但一条都不该收
	// ModelFallbackQuota 表示额度不足，**压根没有发出请求**。
	// 与 error 分开：一个是「发了但网关坏了」，一个是「没发，因为账上不够」。
	// 前者要查网关，后者要充值或等下个账期——运维动作完全不同。
	ModelFallbackQuota = "quota"
)

// defaultModelTimeout 单页模型调用上限。
//
// 实测 P95 11.7s（单轮）、5 轮并发时 28–43s。45s 给慢网关留了余量，又不至于让一个
// 挂死的请求把 worker 租约拖到过期——超时就退回规则，用户拿到的是 0.667 的结果，
// 而不是一页空白。
const defaultModelTimeout = 45 * time.Second

// RoutedOptions 是模型路由的全部参数。零值即「只跑规则」。
type RoutedOptions struct {
	Rule  Options
	Model ModelCompleter
	Cache CompletionCache
	// ModelID 与 PromptVersion 进缓存键。ModelID 留空时缓存不写也不读——
	// 空模型名会让换模型之后仍命中旧缓存，那种错误在报告上是「换了模型指标没变」。
	ModelID       string
	PromptVersion string
	// Sample 是采样序号。产品只采一次（0），多轮实验靠它区分不同抽样。
	Sample  int
	Timeout time.Duration
	// DisableStructFill 关掉规则收尾（titlePrior + structuralBind）。
	// 留这个开关是为了复现「模型裸成绩」，不是给产品用：模型看不见容器，
	// 关掉之后四步流程页会漏掉一半元素。
	DisableStructFill bool
	// AllowModelIDs 非空时只接受这些元素 id（规则已绑的元素对模型只读）。
	// 产品路由不传（整页换掉规则结果）；留给 M0 的 rule+model 对照复现。
	AllowModelIDs map[string]struct{}
}

// ModelRun 是一次模型参与的记录。无论成败都填，报告与前端都靠它区分
// 「没问模型」和「问了但退回了规则」——两者在产物上看起来一样，意义完全不同。
type ModelRun struct {
	Used      bool  `json:"used"`
	CacheHit  bool  `json:"cacheHit"`
	LatencyMS int64 `json:"latencyMs"`
	// Fallback 只在模型没产出可用结果时有值。
	Fallback string `json:"fallback,omitempty"`
	// Accepted 是被接受的绑定条数；Rejected 按原因计数（八类全部预置为 0）。
	Accepted int            `json:"accepted"`
	Rejected map[string]int `json:"rejected,omitempty"`
	// Prior / Structural 是规则收尾补上的条数，与 Accepted 分开计：
	// 模型做了什么、规则补了什么，必须分得开。
	Prior               int `json:"prior"`
	Structural          int `json:"structural"`
	MonotonicViolations int `json:"monotonicViolations"`
	// PromptChars 是用户提示词的字符数，用于发现「提示词突然变长」这类回归。
	PromptChars int `json:"promptChars"`
}

// RoutedResult 是预填的最终结果。
type RoutedResult struct {
	Result
	Route Route
	Model ModelRun
}

// RouteOf 返回某一档该走的通道。导出是为了报告与测试能直接问同一句话，
// 而不是各自复制一份 ROUTE_MODEL 集合。
func RouteOf(band Band, reason string) Route {
	if band == BandStructured {
		return RouteRule
	}
	if reason == BandReasonScript {
		return RouteModel
	}
	return RouteRule
}

// RunRouted 是带模型通道的预填入口。
//
// 契约：**永不因为模型而失败**。模型没配、超时、返回垃圾、一条都不该收，都退回规则结果，
// 并把原因写进 Model.Fallback。预填是编辑器打开时的增强项，不是必经路径；
// 让一次网关抖动把整页变成错误，比少绑几条严重得多。
func RunRouted(ctx context.Context, slideID string, elements []Element, segments []Segment, opt RoutedOptions) RoutedResult {
	// 阅读序只排一次：规则、提示词、模型输出映射、结构收尾都用这一份。
	// 各用各的顺序时，「模型按阅读序绑的元素」落到规则的下标上会整体错位。
	ordered := readingOrder(elements)
	ruleRes := Run(ordered, segments, opt.Rule)

	out := RoutedResult{Result: ruleRes, Route: RouteRule}
	out.Model = ModelRun{Rejected: map[string]int{}}
	for _, k := range FormatRejects {
		out.Model.Rejected[k] = 0
	}
	for _, k := range SemanticRejects {
		out.Model.Rejected[k] = 0
	}

	if RouteOf(ruleRes.Band, ruleRes.BandReason) != RouteModel {
		// 规则页连缓存都不读：分档说这一页规则更可信，再走一次模型只是多一个出错环节，
		// 延迟还会从毫秒级变成十秒级。
		return out
	}
	if opt.Model == nil || len(segments) == 0 {
		out.Model.Fallback = ModelFallbackNoModel
		return out
	}

	prompt := modelPromptFor(slideID, ordered, segments)
	out.Model.PromptChars = len([]rune(prompt.User))

	version := opt.PromptVersion
	if version == "" {
		version = PromptVersion
	}
	var cacheKey string
	if opt.Cache != nil && opt.ModelID != "" {
		cacheKey = modelCacheKey(opt.ModelID, version, opt.Sample, prompt.System, prompt.User)
		if text, hit, err := opt.Cache.Get(ctx, cacheKey); err == nil && hit {
			out.Model.CacheHit = true
			return finishModelRoute(out, text, ordered, segments, opt)
		}
	}

	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = defaultModelTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	start := time.Now()
	comp, err := opt.Model.Complete(callCtx, prompt)
	// 在 cancel 之前读耗时：cancel 之后 time.Since 仍然正确，但把两者写在一起
	// 容易让人以为耗时被 cancel 影响。
	out.Model.LatencyMS = time.Since(start).Milliseconds()
	cancel()

	if err != nil {
		// DeadlineExceeded 要单独认：网关超时与「模型说错了」在运维上是两件事，
		// 前者查网络，后者查提示词。合成一个 error 就没法分流了。
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			out.Model.Fallback = ModelFallbackTimeout
		} else {
			out.Model.Fallback = ModelFallbackError
		}
		return out
	}

	if cacheKey != "" {
		// 缓存写失败不影响结果：它只是让下一次重跑不稳定，而不是这一次错了。
		_ = opt.Cache.Put(ctx, cacheKey, comp.Text)
	}
	return finishModelRoute(out, comp.Text, ordered, segments, opt)
}

// finishModelRoute 校验模型输出、做结构收尾、拼成最终结果。
// 校验不过时保留规则结果——这是「退回」而不是「清空」。
func finishModelRoute(out RoutedResult, raw string, ordered []Element, segments []Segment, opt RoutedOptions) RoutedResult {
	v := validateModelOutput(raw, ordered, segments, opt.AllowModelIDs)
	out.Model.Rejected = v.Rejected
	out.Model.Accepted = len(v.Pairs)
	out.Model.MonotonicViolations = v.MonotonicViolations

	if !v.ParseOK {
		out.Model.Fallback = ModelFallbackInvalidJSON
		return out
	}
	if len(v.Pairs) == 0 {
		// 「能解析但一条都不该收」和「压根不是 JSON」分开：前者说明模型守住了边界
		// （没有编造 id、没有绑装饰块），只是这一页它也没把握；后者说明输出契约被破坏了。
		out.Model.Fallback = ModelFallbackRejected
		return out
	}

	pairs := v.Pairs
	for i := range pairs {
		pairs[i].source = animation.TriggerSourceModel
	}
	final := pairs
	if !opt.DisableStructFill {
		// 模型只给了 id 映射，它看不见「这个卡片容器里装着一个已绑的文字块」。
		// 四步流程页上它只绑文字块，容器与配图全漏（实测漏 9/18），所以照搬规则侧的
		// 两条收尾：标题先验补标题、结构推断让容器跟着内容走。
		prior := titlePrior(ordered, segments, final)
		structs := structuralBind(ordered, concatPairs(final, prior))
		out.Model.Prior, out.Model.Structural = len(prior), len(structs)
		final = concatPairs(final, prior, structs)
	}
	// 模型先来先得：它若自己绑了容器，结构推断不再重复绑同一个元素。
	final = dedupeByElement(final)
	sortPairsByElem(final)

	out.Route = RouteModel
	out.Model.Used = true
	out.Result.Bindings = toBindings(ordered, segments, final)
	out.Result.BySource = bySourceOf(ordered, final, true)
	return out
}
