package bind

// 本文件是设计文档 §2 的分档实现：预填之前先判「这一页的版面有没有把讲解顺序编码出来」。
//
// 为什么必须先判：同一套规则在结构化版面 0.972、纯文字页 0.533。硬套会得到一个
// 「大概能填一半」的均值，把「可用」与「不可用」的差别掩盖掉。分档不改绑定结果，
// 只决定这批预填在编辑器里以什么形态呈现（低置信要换样式）。
//
// 三条信号的检测全部是几何 + 文本形态，不调模型、不读字号：
//   - 编号标记：页内存在 ≥3 个 `01`–`99` 形态的短文本块；
//   - 卡片簇：存在互相包含或上下紧邻（≤60px）且横向重合 ≥0.6 的元素对；
//   - 方位覆盖：页内元素覆盖 ≥2 个方向半区。
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
// classifyBand 返回分档、命中的信号（固定顺序）与方向覆盖数。
func classifyBand(elements []Element) (Band, []string, int) {
	signals := make([]string, 0, 3)

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

	if ms >= 3 || clustered {
		return BandStructured, signals, cov
	}
	return BandLowConfidence, signals, cov
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
