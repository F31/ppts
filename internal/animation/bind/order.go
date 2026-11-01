package bind

import (
	"math"
	"sort"
	"strings"
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
	//
	// 中位高度只统计**有文字**的元素：行带要建模的是「一行字」，而整块卡片的
	// 底板、蒙层、装饰色块不是一行字。把这些 200–300px 的高盒子算进来，中位高度
	// 会被抬高，容差随之变大，于是**相邻两条不同的文字行被并成一行**，
	// 行内再按 x 排序，跨栏的标签就被拉到正文后面去了。
	// 33 页 Token 方案 p16：55 个元素里 17 个无字底板（最高 302px），
	// 中位高度 54→容差 32.4，把 y=546 的「Token增值」和 y=570 的「智能体」并成一行；
	// 因为 x 更大（1577 vs 1183），「Token增值」被排到「智能体」之后，
	// 而讲稿里它是前一句，单调对齐于是只能把它退回后一段（1.00 的精确匹配换成 0.52）。
	// 只算文本元素时中位高度 37→容差 22.2，两行正确断开。
	tol := 12.0
	if hs := heights(els); len(hs) > 0 {
		sort.Float64s(hs)
		tol = math.Max(12, hs[len(hs)/2]*0.6)
	}

	// 分行只拿文字元素做，空形状（底板、分隔条、图标底）随后挂到它覆盖的那一行。
	//
	// 为什么不能让空形状参与分行：一行字从上往下扫，第一条落进这一行的元素就是基准线。
	// 底板常比它装的文字高出一截、顶边也靠上几像素（p7 的 s9 在 y=326，字在 331），
	// 基准线因此被抬高，下一行字（y=382，只差 51）就掉出了这一行，顺序反过来——
	// 于是对齐只能把「核心逻辑闭环」整段让给后面的段落，它自己反而变成同块带出。
	// 空形状从不参与字面匹配（相似度恒为 0），把它放进哪一行对结果没有影响，
	// 唯一的影响就是这条基准线，所以干脆不让它当基准。
	text, rest := splitText(els)
	ordered := make([]Element, len(text))
	copy(ordered, text)
	if len(ordered) == 0 {
		// 全页没有文字：没有可断的行，按 y 再 x 排一遍即可。
		ordered = append(ordered, rest...)
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].Box.Y != ordered[j].Box.Y {
				return ordered[i].Box.Y < ordered[j].Box.Y
			}
			return ordered[i].Box.X < ordered[j].Box.X
		})
		return ordered
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Box.Y != ordered[j].Box.Y {
			return ordered[i].Box.Y < ordered[j].Box.Y
		}
		return ordered[i].Box.X < ordered[j].Box.X
	})

	var rows [][]Element
	rowStart := 0
	for i := 1; i <= len(ordered); i++ {
		if i < len(ordered) && ordered[i].Box.Y-ordered[rowStart].Box.Y <= tol {
			continue
		}
		// 必须复制：这些切片共享 ordered 的底层数组，后面往某一行 append 空形状时
		// 会写穿到下一行的长度之外，把下一行的元素覆盖掉（表现为同一元素出现两次）。
		rows = append(rows, append([]Element(nil), ordered[rowStart:i]...))
		rowStart = i
	}
	// 空形状挂到纵向中心所在（或最近）的那一行，再按 x 归位。
	for _, e := range rest {
		k := nearestRow(rows, e)
		rows[k] = append(rows[k], e)
	}
	out := make([]Element, 0, len(els))
	for _, row := range rows {
		sort.SliceStable(row, func(a, b int) bool { return row[a].Box.X < row[b].Box.X })
		out = append(out, row...)
	}
	return out
}

func splitText(els []Element) (text, rest []Element) {
	for _, e := range els {
		if strings.TrimSpace(e.Text) != "" {
			text = append(text, e)
			continue
		}
		rest = append(rest, e)
	}
	return text, rest
}

// nearestRow 返回纵向中心落在哪一行；落在行间空隙时取最近的一行（并列取靠上的）。
func nearestRow(rows [][]Element, e Element) int {
	cy := e.Box.Y + e.Box.H/2
	best, bestDist := 0, math.Inf(1)
	for i, row := range rows {
		top, bot := math.Inf(1), math.Inf(-1)
		for _, x := range row {
			top = math.Min(top, x.Box.Y)
			bot = math.Max(bot, x.Box.Y+x.Box.H)
		}
		d := 0.0
		if cy < top {
			d = top - cy
		} else if cy > bot {
			d = cy - bot
		}
		if d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

// heights 取「一行字」的高度样本：只收有文字的元素。
//
// 全页一个文字元素都没有时退回全体（这时没有行可断，容差取什么都一样，
// 但不能让调用方拿到 0 容差而逐元素成行）。
func heights(els []Element) []float64 {
	hs := make([]float64, 0, len(els))
	for _, e := range els {
		if e.Box.H > 0 && strings.TrimSpace(e.Text) != "" {
			hs = append(hs, e.Box.H)
		}
	}
	if len(hs) > 0 {
		return hs
	}
	for _, e := range els {
		if e.Box.H > 0 {
			hs = append(hs, e.Box.H)
		}
	}
	return hs
}
