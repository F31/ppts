package bind

import "strings"

// 本文件是设计文档 §2 的分档实现：预填之前先判「这一页的版面有没有把讲解顺序编码出来」。
//
// 为什么必须先判：同一套规则在结构化版面 0.972、纯文字页 0.533。硬套会得到一个
// 「大概能填一半」的均值，把「可用」与「不可用」的差别掩盖掉。分档不改绑定结果，
// 只决定这批预填在编辑器里以什么形态呈现（低置信要换样式）。
//
// 四条信号的检测全部是几何 + 文本形态，不调模型、不读字号：
//   - 编号标记：页内存在 ≥3 个 `01`–`99` 形态的短文本块；
//   - 卡片簇：存在互相包含或上下紧邻（≤60px）且横向重合 ≥0.6 的元素对；
//   - 方位覆盖：页内元素覆盖 ≥2 个方向半区；
//   - 讲稿结构词（script-structure / script-flat）：讲稿分段里有没有把版面结构念出来。
//     前三条读版面，这一条读讲稿——它是判断「预填能不能跟上」的另一半。
//
// 判别力是**不均匀**的，这里如实记下，并据此定档：
//
//   - markers、clusters 在真实讲义上区分度很高，任一命中即判结构化；
//   - **方向覆盖单独命中不判结构化**。设计文档把它列为判据，但实测它几乎恒真：
//     任何有元素的页面都会同时占住「左」和「上」两个半区，横竖都过 2 的门槛。
//     一个恒真的判据留在「or」里，等于把分档整个废掉——纯文字页永远拿不到低置信标注。
//     它仍然作为信号输出（Signals + DirectionCoverage），让编辑与报告自己收紧，
//     但不再单独决定档位。
//
// 这个偏离写进了 changes/animation-narration-binding/design.md §2 的「实现修正」。
//
// 第四条信号（script-*）读的是**讲稿**而不是版面，来自结构词消融：
// 「版面有结构」只是结构化的一半前提，另一半是「讲稿把结构讲出来」。
// 同一份有编号/卡片的讲义，讲稿含结构词时内容召回 0.972，删掉结构词后 0.667。
// 只读版面会让「版面有编号、讲稿口语直讲」这么一格拿到结构化待遇——低置信提示
// 恰好在真实的高风险组合上沉默。见 changes/animation-prefill-script-signal/design.md。
//
// classifyBand 返回分档、命中的信号（固定顺序）、方向覆盖数，以及低置信的原因。
// Reason 为空表示判成结构化。
func classifyBand(elements []Element, segments []Segment) bandVerdict {
	signals := make([]string, 0, 5)

	ms := 0
	for _, e := range elements {
		if isMarker(e) && !isAux(e) {
			ms++
		}
	}
	if ms >= 3 {
		signals = append(signals, SignalMarkers)
	}
	clustered := hasCluster(elements)
	if clustered {
		signals = append(signals, SignalClusters)
	}
	cov := directionCoverage(elements)
	if cov >= 2 {
		signals = append(signals, SignalDirections)
	}

	layoutOK := ms >= 3 || clustered
	scriptAbsent := noScriptText(segments)
	scriptOK := hasScriptStructure(segments)
	switch {
	case scriptAbsent:
		// 没有可念的文字：分档的另一半根本不存在，不能判结构化，也不该说
		// 「讲稿没把结构念出来」——用户还没写讲稿，该做的是先写，不是改。
		signals = append(signals, SignalScriptAbsent)
	case scriptOK:
		signals = append(signals, SignalScriptStructure)
	default:
		signals = append(signals, SignalScriptFlat)
	}

	v := bandVerdict{Signals: signals, Directions: cov}
	switch {
	case layoutOK && scriptOK:
		v.Band = BandStructured
	case scriptAbsent:
		v.Band = BandLowConfidence
		v.Reason = BandReasonNoScript
	case layoutOK:
		// 版面没问题、讲稿没把结构念出来：实测 0.667。提示必须指向讲稿，
		// 否则用户会去重排 PPT——而该改的是讲解词。
		v.Band = BandLowConfidence
		v.Reason = BandReasonScript
	case scriptOK:
		v.Band = BandLowConfidence
		v.Reason = BandReasonLayout
	default:
		v.Band = BandLowConfidence
		v.Reason = BandReasonBoth
	}
	return v
}

// noScriptText 报告这一页有没有可念的文字。
//
// 只判「全空」而不判「太少」：一页只有一句「谢谢」也是讲稿，能不能绑是绑定器的事，
// 分档不该替它下结论。这里区分的是「有文字但没念结构」与「压根没有文字」——
// 前者改讲解词，后者先写稿，两个动作指向不同的人。
func noScriptText(segments []Segment) bool {
	for _, s := range segments {
		if strings.TrimSpace(s.Text) != "" {
			return false
		}
	}
	return true
}

// bandVerdict 是一次分档的全部产出。
type bandVerdict struct {
	Band       Band
	Signals    []string
	Directions int
	// Reason 只在低置信时有值：layout / script / both / no-script。
	Reason string
}

// hasCluster 报告页内是否存在卡片簇。
//
// 「上下紧邻」要求 gap ≥ 0（真正上下相邻，允许正好贴合），不接受负值：
// 同一行左右并排的两个文本框 gap 恒为负，若也放行，任何一行并排文字都会被判成卡片簇，
// 这条信号就跟方向覆盖一样恒真了。轻微重叠的同卡片元素由 siblingBind 处理（那里
// 允许负 gap 是对的），两个判据的用途不同，不能共用同一个宽松口径。
func hasCluster(elements []Element) bool {
	for i := range elements {
		a := elements[i]
		if isAux(a) || area(a) == 0 || area(a) >= 0.85*pageW*pageH {
			continue
		}
		for j := i + 1; j < len(elements); j++ {
			b := elements[j]
			if isAux(b) || area(b) == 0 || area(b) >= 0.85*pageW*pageH {
				continue
			}
			if overlapX(a, b) < 0.6 {
				continue
			}
			if contains(a, b) || contains(b, a) {
				return true
			}
			gap := max(b.Box.Y-(a.Box.Y+a.Box.H), a.Box.Y-(b.Box.Y+b.Box.H))
			if gap >= 0 && gap <= 60 {
				return true
			}
		}
	}
	return false
}

// directionCoverage 返回四个方向半区（左/右/上/下）里非空的个数，0..4。
//
// 整页铺底不参与统计：它的中心永远在正中间，算进去等于给每个页面白送左右上下各一个。
func directionCoverage(elements []Element) int {
	var left, right, top, bottom bool
	for _, e := range elements {
		if area(e) == 0 || area(e) >= 0.85*pageW*pageH {
			continue
		}
		cx, cy := center(e)
		if cx < pageW/2 {
			left = true
		} else {
			right = true
		}
		if cy < pageH/2 {
			top = true
		} else {
			bottom = true
		}
	}
	n := 0
	for _, ok := range []bool{left, right, top, bottom} {
		if ok {
			n++
		}
	}
	return n
}
