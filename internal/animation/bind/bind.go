package bind

import (
	"sort"

	"github.com/F31/ppts/internal/animation"
)

// Box 是 1080p 画布坐标下的轴对齐矩形（px）。实验脚本与渲染侧共用同一套坐标系，
// 方位判定（0.5 切线）与面积占比才有可比的量纲。
type Box struct {
	X, Y, W, H float64
}

// Element 是参与绑定的一页元素。
//
// Kind 用的是**版面分类**（title/bullet/marker/eyebrow/note/page_number/image/table/
// chart/shape），不是 animation.Track.Kind 的那一套（title/bullet/image/shape/icon/chart/
// table/page）。两者差在 marker 与三类「元信息」：绑定器需要知道「01」是编号、「02」在
// 右下角是页码，而这两件事对动效渲染没有意义，所以渲染分类里没有它们。
//
// 这个类型存在的唯一理由：绑定器要**在编译之前**拿到文本。render.Element 只有
// {ID, Kind, Bounds}，文本在 project.Shape 上，换算成 render.Element 时就丢了。
type Element struct {
	ID   string
	Kind string
	Box  Box
	Text string
}

// Segment 是一个讲解段落。ID 必须是讲稿里的稳定 id（预填只写 withSegment:<id>）。
type Segment struct {
	ID   string
	Text string
}

// Binding 是一条预填结果。
type Binding struct {
	ElementID string
	SegmentID string
	// Source 是 animation.TriggerSource* 之一，标明这条绑定出自哪条规则。
	Source string
	// Score 是字面相似度（0..1）。非字面规则恒为 0——它们本来就没有字面依据，
	// 硬塞一个数进去只会让人误以为可以拿来排序。
	Score float64
	// Confidence 是模型自报的把握（0..1），只用于复核排序，不进任何门槛。
	// 规则绑定恒为 0：规则没有「自报把握」这回事，字面相似度已经在 Score 里，
	// 硬把两者塞进同一个字段，报告上就分不清「模型不确定」和「字面不像」。
	Confidence float64
	// StaggerMS 是同段内的错落偏移，按元素阅读顺序 × StaggerMS。
	StaggerMS int64
}

// Band 是版面分档：结构化页面值得预填，无信号页面只给低置信预填。
//
// 为什么不直接平均：同一套规则在结构化版面 0.972、纯文字页 0.533。硬套会得到一个
// 「大概能填一半」的均值，把「可用」和「不可用」的差别掩盖掉。分档让编辑器知道
// 哪一批该多看一眼。
type Band string

const (
	// BandStructured 表示版面把讲解顺序编码出来了（编号/卡片/分区），预填可信。
	BandStructured Band = "structured"
	// BandLowConfidence 表示没检测到结构信号，预填照出但要标低置信。
	BandLowConfidence Band = "low-confidence"
)

// 结构信号名。报告与编辑器都按这些字面量筛选。
//
// 前三条读版面，后三条读讲稿（三条互斥，永远只出一条）：版面有没有把顺序编码出来，
// 与讲稿有没有把那个顺序念出来，是两件事，缺哪一件的补救动作也不同。
const (
	SignalMarkers         = "markers"          // 页内有 ≥3 个编号标记（01/02/03）
	SignalClusters        = "clusters"         // 存在卡片簇（包含或上下紧邻 + 横向重合）
	SignalDirections      = "directions"       // 页内元素覆盖 ≥2 个方向半区
	SignalScriptStructure = "script-structure" // 讲稿含序数/精确数量/空间指代
	SignalScriptFlat      = "script-flat"      // 讲稿没有把结构讲出来
	// SignalScriptAbsent 表示这一页根本没有可念的文字。
	// 它和 script-flat 是两件事：一个要写讲稿，一个要改讲稿。
	// 真实客户 deck 里这个分支是常态——33 页 Token 方案有 27 页没有备注，
	// 混成 script-flat 会让编辑器对着空讲稿说「改讲解词」。
	SignalScriptAbsent = "script-absent"
)

// 低置信的原因。四者预期准确率与补救动作都不同，不能合成一句提示。
const (
	BandReasonLayout   = "layout"    // 版面无结构信号（0.533）
	BandReasonScript   = "script"    // 版面有结构、讲稿没讲出来（0.667）
	BandReasonBoth     = "both"      // 两侧都缺（0.533）
	BandReasonNoScript = "no-script" // 这一页还没有讲稿，无从判断（不预填，先写稿）
)

// Result 是一次预填的全部产出。
type Result struct {
	Bindings []Binding
	Band     Band
	// Signals 是命中的结构信号，按固定顺序（markers/clusters/directions/script-*）排列。
	Signals []string
	// BandReason 只在低置信时有值（layout / script / both / no-script）：
	// 说明缺的是版面、讲稿，还是这一页压根还没有讲稿。
	// 结构化结果为空。
	BandReason string
	// DirectionCoverage 是四个方向半区里非空的个数（0..4）。分档判据用 ≥2，
	// 同时把它透出来：真实讲义上这条信号判别力弱，编辑与报告可以自己收紧。
	DirectionCoverage int
	// BySource 按来源分组列出元素 id，顺序与 Bindings 一致。用于「先看同块带出的 9 条」
	// 这类复核——没有它，用户面对几十条预填只能肉眼逐条核。
	BySource map[string][]string
}

// Options 控制预填行为。零值即实验口径（v2 规则 + 同块带出 + 阈值 0.12）。
type Options struct {
	// Threshold 是允许绑定的最低相似度；<=0 时取 DefaultThreshold。
	Threshold float64
	// DisableSiblings 关闭「同块带出」。留这个开关是为了能复现 base 对照，
	// 不是为了给产品用——关掉它四步流程里只会亮一个标题。
	DisableSiblings bool
}

func (o Options) threshold() float64 {
	if o.Threshold <= 0 {
		return DefaultThreshold
	}
	return o.Threshold
}

// pair 是内部的对齐结果。source 在规则接龙时逐段填上。
type pair struct {
	elem   int
	seg    int
	score  float64
	source string
	// conf 是模型自报把握，规则侧恒为 0（见 Binding.Confidence）。
	conf float64
}

// Run 是唯一入口：纯函数，同一输入必然同一输出。
//
// elements 的顺序不被信任：调用方给的是形状树顺序（z-order），而 align 的单调
// 假设是讲解顺序。入口先按阅读序重排（readingOrder），下游所有规则都吃这一份。
func Run(elements []Element, segments []Segment, opt Options) Result {
	elements = readingOrder(elements)
	pairs, _ := align(elements, segments, opt.threshold())

	// 辅助元素白名单：字面匹配上的也退掉。图注/页码被绑属于**错绑**——精确率会掉，
	// 而且用户看不出来（它动的时候很像「这里本来该动」）。
	textPairs := make([]pair, 0, len(pairs))
	for _, p := range pairs {
		if isAux(elements[p.elem]) {
			continue
		}
		textPairs = append(textPairs, p)
	}
	textPairs = withSource(textPairs, animation.TriggerSourceText)

	bound := boundOf(textPairs)
	countPairs := countBind(elements, segments, bound)
	bound.update(countPairs)
	ordinalPairs := ordinalBind(elements, segments, bound)
	bound.update(ordinalPairs)
	spatialPairs := spatialBind(elements, segments, bound)
	bound.update(spatialPairs)

	var siblingPairs []pair
	if !opt.DisableSiblings {
		siblingPairs = siblingBind(elements, concatPairs(textPairs, countPairs, ordinalPairs, spatialPairs))
		bound.update(siblingPairs)
	}
	// 先标题先验、再结构推断：容器的落点依赖「它装着的元素」，而那个元素可能正是靠
	// 标题先验绑上的。反过来跑，卡片就无主可依（口语版讲稿里实测漏掉）。
	priorPairs := titlePrior(elements, segments,
		concatPairs(textPairs, countPairs, ordinalPairs, spatialPairs, siblingPairs))
	structPairs := structuralBind(elements,
		concatPairs(textPairs, countPairs, ordinalPairs, spatialPairs, siblingPairs, priorPairs))

	merged := concatPairs(textPairs, countPairs, ordinalPairs, spatialPairs,
		siblingPairs, priorPairs, structPairs)

	res := Result{
		Bindings: toBindings(elements, segments, dedupeByElement(merged)),
		Signals:  []string{},
		// 空切片而非 nil：报告按来源逐项列出（含 0 条的「标题先验」），
		// 少一个键和一个 0 在复核时是两回事——前者像「这类没跑」，后者是「跑了，一条没出」。
		BySource: emptyBySource(),
	}
	// 按**去重前**统计来源：七条规则各判断「有没有主」，理论上不会重复，但真重复了
	// 也要看见——去重后的计数会把「同块带出多抢了一件」这类问题抹平。
	for _, p := range merged {
		res.BySource[p.source] = append(res.BySource[p.source], elements[p.elem].ID)
	}
	verdict := classifyBand(elements, segments)
	res.Band = verdict.Band
	res.Signals = verdict.Signals
	res.BandReason = verdict.Reason
	res.DirectionCoverage = verdict.Directions
	return res
}

func withSource(pairs []pair, source string) []pair {
	for i := range pairs {
		pairs[i].source = source
	}
	return pairs
}

func concatPairs(groups ...[]pair) []pair {
	n := 0
	for _, g := range groups {
		n += len(g)
	}
	out := make([]pair, 0, n)
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func dedupeByElement(pairs []pair) []pair {
	seen := make(map[int]struct{}, len(pairs))
	out := make([]pair, 0, len(pairs))
	for _, p := range pairs {
		if _, ok := seen[p.elem]; ok {
			continue
		}
		seen[p.elem] = struct{}{}
		out = append(out, p)
	}
	return out
}

// emptyBySource 给出一份按来源预置空列表的映射。
//
// 规则页与模型页共用它：模型路由下多一个 model 键——「这一页有没有问过模型」
// 必须能从结果本身看出来，而不是靠调用方记得自己走过哪条分支。
func emptyBySource() map[string][]string {
	m := map[string][]string{
		animation.TriggerSourceText:       {},
		animation.TriggerSourceCount:      {},
		animation.TriggerSourceOrdinal:    {},
		animation.TriggerSourceSpatial:    {},
		animation.TriggerSourceSibling:    {},
		animation.TriggerSourcePrior:      {},
		animation.TriggerSourceStructural: {},
	}
	return m
}

// bySourceOf 按绑定来源分组列出元素 id（入参需已按元素排序，列表顺序即阅读序）。
func bySourceOf(elements []Element, pairs []pair, withModel bool) map[string][]string {
	out := emptyBySource()
	if withModel {
		out[animation.TriggerSourceModel] = []string{}
	}
	for _, p := range pairs {
		out[p.source] = append(out[p.source], elements[p.elem].ID)
	}
	return out
}

// sortPairsByElem 按元素阅读序升序排列；同元素时按段号。
//
// 段内错落（StaggerMS）是按遍历顺序赋的，只有先按元素排序，同一段里先出现的元素
// 才拿到更小的偏移。模型输出的顺序不可信，这一步不能省。
func sortPairsByElem(pairs []pair) {
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].elem != pairs[j].elem {
			return pairs[i].elem < pairs[j].elem
		}
		return pairs[i].seg < pairs[j].seg
	})
}

// toBindings 转成输出：段内按阅读顺序错落 StaggerMS。
//
// 错落是**段内**序号，不是全局序号：同一段带出四件时是 0/140/280/420，下一段重新从 0
// 开始。全局累加会让越靠后的段落起点越晚，末尾几页的第一次出现能推迟到一两秒，
// 看起来像「讲完了画面才动」。
func toBindings(elements []Element, segments []Segment, pairs []pair) []Binding {
	sorted := make([]pair, len(pairs))
	copy(sorted, pairs)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].elem != sorted[j].elem {
			return sorted[i].elem < sorted[j].elem
		}
		return sorted[i].seg < sorted[j].seg
	})
	seenInSeg := make(map[int]int, len(segments))
	out := make([]Binding, 0, len(sorted))
	for _, p := range sorted {
		out = append(out, Binding{
			ElementID:  elements[p.elem].ID,
			SegmentID:  segments[p.seg].ID,
			Source:     p.source,
			Score:      p.score,
			Confidence: p.conf,
			StaggerMS:  int64(seenInSeg[p.seg]) * StaggerMS,
		})
		seenInSeg[p.seg]++
	}
	return out
}
