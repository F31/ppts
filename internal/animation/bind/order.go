package bind

import (
	"math"
	"sort"
)

// readingOrder 把元素按「讲者会怎么念」排：先按行（顶边接近的算一行），行内从左到右。
//
// 为什么绑之前必须先排：align 的单调 DP 假设「元素顺序就是讲解顺序」，
// 而调用方（internal/app.PageBindElements）给的是形状树顺序，也就是 z-order——
// 作者先放背景框、后放标题时，这个顺序和阅读顺序是反的。
//
// 造出来的 5 页样本上两者恰好一致，所以一直没暴露；真实讲义上不一致。
// 同一批页按 z-order 喂进去，元素绑回自己的比例（自引用召回，scripts/fidelity/selfrecall.py）：
//
//	33 页 Token 方案  62.8% → 按阅读序 92.6%
//	14 页客户 deck    76.9% → 按阅读序 100.0%
//	5 页造样本        80.6% → 按阅读序 100.0%
//
// 差额不是讲稿质量，也不是页面太脏：把顺序换掉，同一份讲稿、同一批元素就全对上了。
//
// 排序在绑定器内部做，返回新切片，不改调用方的元素顺序——Bindings 里带的是 ElementID，
// 调用方按 ID 回查，不依赖下标。
func readingOrder(els []Element) []Element {
	if len(els) < 2 {
		return els
	}

	// 行高容差取中位高度的 0.6 倍，下限 12px。同一个数字两处用（这里和
	// scripts/fidelity/selfrecall.py），改一处必须改另一处，否则离线实验和
	// 产品行为会对不上——两边量的就不是同一件事了。
	tol := 12.0
	if hs := heights(els); len(hs) > 0 {
		sort.Float64s(hs)
		tol = math.Max(12, hs[len(hs)/2]*0.6)
	}

	ordered := make([]Element, len(els))
	copy(ordered, els)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Box.Y != ordered[j].Box.Y {
			return ordered[i].Box.Y < ordered[j].Box.Y
		}
		return ordered[i].Box.X < ordered[j].Box.X
	})

	out := make([]Element, 0, len(ordered))
	rowStart := 0
	for i := 1; i <= len(ordered); i++ {
		if i < len(ordered) && ordered[i].Box.Y-ordered[rowStart].Box.Y <= tol {
			continue
		}
		row := ordered[rowStart:i]
		sort.SliceStable(row, func(a, b int) bool { return row[a].Box.X < row[b].Box.X })
		out = append(out, row...)
		rowStart = i
	}
	return out
}

func heights(els []Element) []float64 {
	hs := make([]float64, 0, len(els))
	for _, e := range els {
		if e.Box.H > 0 {
			hs = append(hs, e.Box.H)
		}
	}
	return hs
}
