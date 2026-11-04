// Package chartgeo 从**渲染后的页面图**里量出内嵌图表每根柱子的包围盒。
//
// ## 为什么要有它
//
// PPTX 里图表是一个形状，柱子由图表引擎在渲染时排布——文档模型里没有单根柱的
// 几何。而「这一刻讲的是哪一根」这句话要落到画面上，必须有一块矩形（矩形遮罩
// 压暗，见 render.FocusSpan.Rect）。所以只能从页面图上量。
//
// ## 与 scripts/fidelity/split_chart_bars.py 的关系
//
// 那份 Python 是保真链的实验实现，这份是产品用的。**两份实现会漂**，所以：
//   - 算法口径完全照搬（同一批阈值、同一条「宁可未测也不指错」），并在注释里
//     逐条写明「为什么是这条」，不在这里另立一套判断；
//   - `parity_test.go` 用同一张合成页图跑两边，要求框逐根一致。跑不了 python
//     时那条用例 skip，而不是假装通过。
//
// ## 边界（与 Python 版一致，且都不是「暂时没做」而是「做了会指错」）
//
//   - 垂直柱（column*）与水平条（bar*）。水平条只做单系列：多系列的组内次序
//     随坐标系翻转，没有样本可验就不猜。
//   - 负值不切：柱会翻到基线另一侧，长度不再反映大小。
//   - 折线/饼/面积不切：它们在画面上不是「一根柱占一个带」。
//   - 多系列要求「每系列一色」，且组内颜色次序每组一致——这同时证明系列是按
//     声明顺序画的，于是不必猜哪个颜色对应哪个系列。
package chartgeo

import (
	"fmt"
	"image"
	"math"
	"sort"
)

// 算法常数。每一项都对应 Python 版里的同名常量与实测依据。
const (
	// colorTol 是「算同一种柱色」的 RGB 欧氏距离。同一块纯色经缩放/压缩后与主色
	// 差不超过 10，40 留足余量；再大就会把浅灰网格线（与蓝柱差 ~90）吃进来。
	colorTol = 40.0
	// colMinRatio：一列里至少这么多比例是柱色，才算「这一列有柱子」。柱宽 116px、
	// 高 135–246px 时占比 20–35%；细网格线横跨整幅图，每列只有 1–2px，占比 <1%。
	colMinRatio = 0.03
	// satMin 是饱和度门槛 max-min：白(0)、灰(0)、黑(0) 都过不了；蓝柱是 110。
	satMin = 24
	// minCandShare：柱子至少占图表面积的 2%（三根柱合计 ~7%，留一半余量）。
	minCandShare = 0.02
	// widthSpreadMax 是同一张图里柱厚的允许倍数差（抗锯齿会让边缘差 1–2px）。
	widthSpreadMax = 1.15
	// baseTolPx 是底边（垂直）/ 左端（水平）对齐容忍，页面图坐标系。
	baseTolPx = 3
	// clusterShareMin：小于这个占比的色簇是抗锯齿残留、图例描边，撑不起一根柱子。
	clusterShareMin = 0.05
	// clusterRestMax：选够 n_series 个色簇之后，剩下的大色簇最多能占多少。
	// 超过说明这张图不止这么多种颜色（渐变、多主题强调色，或把别的东西当成了柱子）。
	clusterRestMax = 0.15
	// thicknessKeep：切段里厚度不足最厚者这个比例的，是图例方块而不是柱子。
	thicknessKeep = 0.55
	// labelLookback / labelLookahead 是找数据标签的搜索范围（像素）。
	labelLookback  = 70
	labelLookahead = 140
	// labelDark 只认黑字：网格线是 135 的灰，基线更浅，都不会被算进来。
	labelDark = 110
	// labelMargin 是标签带外侧多留的一点，免得字形抗锯齿的最后一列被压暗。
	labelMargin = 3
)

// Rect 是页面图上的轴对齐矩形（像素）。
type Rect struct{ X, Y, W, H int }

// Bar 是一根柱。Cat/Ser 是在「类别主序、系列次序」里的下标，由调用方翻成
// 类别名与系列名——这里不认识业务语义。
type Bar struct {
	Rect Rect
	Cat  int
	Ser  int
	// LabelDir 是数据标签相对柱体的方向："up"（垂直柱）或 "right"（水平条）。
	// 空表示没量到——那时焦点矩形只能框住柱体，这根自己的数字会被压在纱下面。
	LabelDir string
	LabelPad int
}

// Split 在一页图的 chart 区域里切出每根柱。返回的 Bar 已按「类别主序、系列次序」
// 排好；为空时 reason 说明为什么没切成，调用方应把它原样写进报告。
func Split(src image.Image, box Rect, nCats, nSeries int, horizontal bool) ([]Bar, string) {
	if nCats <= 0 || nSeries <= 0 {
		return nil, "没有类别或系列，未切"
	}
	if horizontal && nSeries > 1 {
		return nil, "水平条形只做单系列（多系列的组内次序随坐标翻转，没有样本可验）"
	}
	p, ok := crop(src, box)
	if !ok {
		return nil, "包围盒在页面图上不足 8px，未切"
	}
	colors, reason := colorClusters(p, nSeries)
	if len(colors) == 0 {
		return nil, reason
	}

	// 每个色簇各自切段：段数都必须等于类别数。
	segsPerColor := make([][][2]int, nSeries)
	masks := make([][]bool, nSeries)
	for si := 0; si < nSeries; si++ {
		m := p.mask(colors[si])
		masks[si] = m
		segs := segments(p, m, horizontal)
		// 图例里的色块与柱子同色，会在同一色簇里多切出一段。它比柱子薄得多
		// （12px 方块 vs 130px 柱宽），按厚度滤掉——柱厚在同一张图里本来就一致。
		if len(segs) > 0 {
			top := 0
			for _, s := range segs {
				top = max(top, s[1]-s[0]+1)
			}
			kept := make([][2]int, 0, len(segs))
			for _, s := range segs {
				if float64(s[1]-s[0]+1) >= thicknessKeep*float64(top) {
					kept = append(kept, s)
				}
			}
			segs = kept
		}
		if len(segs) != nCats {
			return nil, fmt.Sprintf("第 %d 个色簇量到 %d 段，应为类别数 %d",
				si, len(segs), nCats)
		}
		segsPerColor[si] = segs
	}

	type raw struct {
		start   int // 主轴起点
		ser     int
		rect    Rect
		thick   int
		baseEnd int
	}
	bars := make([]raw, 0, nCats*nSeries)
	for si, segs := range segsPerColor {
		for _, s := range segs {
			r, ok := p.extent(masks[si], s, horizontal)
			if !ok {
				return nil, fmt.Sprintf("色段 %d–%d 里没有柱色像素", s[0], s[1])
			}
			// 「厚」是垂直于主轴的那一维；「基线端」是柱体落在轴上的那一侧：
			// 垂直柱看底边（y+h），水平条看左端（x）。
			thick, baseEnd := r.W, r.Y+r.H
			if horizontal {
				thick, baseEnd = r.H, r.X
			}
			bars = append(bars, raw{start: s[0], ser: si, rect: r, thick: thick, baseEnd: baseEnd})
		}
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].start < bars[j].start })
	if len(bars) != nCats*nSeries {
		return nil, fmt.Sprintf("量到 %d 根柱，类别×系列应为 %d 根（%d×%d）",
			len(bars), nCats*nSeries, nCats, nSeries)
	}

	// 组内颜色次序必须每组一致：这是「系列按声明顺序画」的唯一可验形式。
	// 不一致说明量到的不是「一类别一组」，硬编下去会把系列张冠李戴。
	var order []int
	for g := 0; g < nCats; g++ {
		got := make([]int, 0, nSeries)
		seen := make(map[int]struct{}, nSeries)
		for _, b := range bars[g*nSeries : (g+1)*nSeries] {
			got = append(got, b.ser)
			seen[b.ser] = struct{}{}
		}
		if len(seen) != nSeries {
			return nil, fmt.Sprintf("第 %d 组里有重复配色，不像「一类别一组」", g)
		}
		if order == nil {
			order = got
			continue
		}
		for i := range got {
			if got[i] != order[i] {
				return nil, "各组内配色次序不一致，系列归属无法确定"
			}
		}
	}

	minThick, maxThick := bars[0].thick, bars[0].thick
	minBase, maxBase := bars[0].baseEnd, bars[0].baseEnd
	for _, b := range bars {
		minThick = min(minThick, b.thick)
		maxThick = max(maxThick, b.thick)
		minBase = min(minBase, b.baseEnd)
		maxBase = max(maxBase, b.baseEnd)
	}
	if float64(maxThick)/float64(minThick) > widthSpreadMax {
		return nil, fmt.Sprintf("柱厚/宽差 %.2f 倍（%d–%dpx），不像同一张图里的柱子",
			float64(maxThick)/float64(minThick), minThick, maxThick)
	}
	if maxBase-minBase > baseTolPx {
		return nil, fmt.Sprintf("柱起点差 %dpx，不在同一条基线上，可能量到了图例或背景",
			maxBase-minBase)
	}

	out := make([]Bar, 0, len(bars))
	for g := 0; g < nCats; g++ {
		// 水平条形的类别轴自下而上：组按 y 升序排开，但第 0 组在最底下。
		cat := g
		if horizontal {
			cat = nCats - 1 - g
		}
		for _, b := range bars[g*nSeries : (g+1)*nSeries] {
			ser := 0
			for i, s := range order {
				if s == b.ser {
					ser = i
					break
				}
			}
			bar := Bar{Rect: b.rect, Cat: cat, Ser: ser}
			if pad := p.labelPad(b.rect, horizontal); pad > 0 {
				bar.LabelPad = pad
				bar.LabelDir = "up"
				if horizontal {
					bar.LabelDir = "right"
				}
			}
			out = append(out, bar)
		}
	}
	// 产物一律按「类别主序、系列次序」排：两种朝向的编号才对得上，下游拿 b0
	// 当「第一个类别」时不用先问这张图是横的还是竖的。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Cat != out[j].Cat {
			return out[i].Cat < out[j].Cat
		}
		return out[i].Ser < out[j].Ser
	})
	if len(out) < 2 {
		return nil, "切出的柱子不足 2 根，未采用"
	}
	return out, ""
}

// ---------------------------------------------------------------- 内部

// plane 是裁剪出来的一块图，拆成三个平面。分开存是为了让「求掩码」这一趟
// 连续读同一通道，而不是每个像素跳三次。
type plane struct {
	w, h int
	r    []float32
	g    []float32
	b    []float32
}

func crop(src image.Image, box Rect) (plane, bool) {
	bd := src.Bounds()
	x0 := max(bd.Min.X, box.X)
	y0 := max(bd.Min.Y, box.Y)
	x1 := min(bd.Max.X, box.X+box.W)
	y1 := min(bd.Max.Y, box.Y+box.H)
	if x1-x0 < 8 || y1-y0 < 8 {
		return plane{}, false
	}
	w, h := x1-x0, y1-y0
	p := plane{w: w, h: h,
		r: make([]float32, w*h), g: make([]float32, w*h), b: make([]float32, w*h)}
	i := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			r, g, b, _ := src.At(x, y).RGBA() // 16 位分量
			p.r[i] = float32(r >> 8)
			p.g[i] = float32(g >> 8)
			p.b[i] = float32(b >> 8)
			i++
		}
	}
	return p, true
}

func (p plane) mask(c [3]float64) []bool {
	m := make([]bool, p.w*p.h)
	cr, cg, cb := float32(c[0]), float32(c[1]), float32(c[2])
	for i := range m {
		dr, dg, db := p.r[i]-cr, p.g[i]-cg, p.b[i]-cb
		m[i] = math.Sqrt(float64(dr*dr+dg*dg+db*db)) < colorTol
	}
	return m
}

func colorClusters(p plane, nSeries int) ([][3]float64, string) {
	n := p.w * p.h
	type cell struct{ r, g, b uint8 }
	counts := make(map[cell]int)
	total := 0
	for i := 0; i < n; i++ {
		mx := max(p.r[i], max(p.g[i], p.b[i]))
		mn := min(p.r[i], min(p.g[i], p.b[i]))
		if mx-mn < satMin || mx <= 40 {
			continue
		}
		total++
		// 量化到 8 级再统计：抗锯齿边缘的过渡色不会各成一簇。
		counts[cell{uint8(int(p.r[i]) &^ 7), uint8(int(p.g[i]) &^ 7), uint8(int(p.b[i]) &^ 7)}]++
	}
	if total < max(64, int(minCandShare*float64(n))) {
		return nil, "图表区域里没有成规模的饱和色（不是柱状图）"
	}
	type kv struct {
		c cell
		n int
	}
	list := make([]kv, 0, len(counts))
	for c, k := range counts {
		list = append(list, kv{c, k})
	}
	// 计数降序；同数时按颜色排序，保证同一张图每次跑出同样的选择（可复现）。
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		a, b := list[i].c, list[j].c
		if a.r != b.r {
			return a.r < b.r
		}
		if a.g != b.g {
			return a.g < b.g
		}
		return a.b < b.b
	})

	chosen := make([][3]float64, 0, nSeries)
	rest := 0
	for _, e := range list {
		if float64(e.n) < clusterShareMin*float64(total) {
			continue
		}
		c := [3]float64{float64(e.c.r), float64(e.c.g), float64(e.c.b)}
		near := false
		for _, ch := range chosen {
			dr, dg, db := c[0]-ch[0], c[1]-ch[1], c[2]-ch[2]
			if math.Sqrt(dr*dr+dg*dg+db*db) < colorTol {
				near = true
				break
			}
		}
		if near {
			continue
		}
		if len(chosen) < nSeries {
			chosen = append(chosen, c)
			continue
		}
		rest += e.n
	}
	if len(chosen) < nSeries {
		return nil, fmt.Sprintf(
			"图表区域里找不到 %d 个成规模且互不相同的色簇（多系列配色、渐变，或非柱状图）",
			nSeries)
	}
	if float64(rest) > clusterRestMax*float64(total) {
		return nil, "选够系列数之后仍有成规模的另一种颜色，这张图比「类别×系列」复杂"
	}
	return chosen, ""
}

// segments 沿主轴投影切段。垂直柱按列投影（段是 x 区间），水平条按行投影（y 区间）。
func segments(p plane, m []bool, horizontal bool) [][2]int {
	// 投影的长度是**主轴**上的格数（垂直柱数有多少列）；门槛却是拿垂直于主轴的
	// 那一维算的（一列里有多少比例是柱色）。两者写反会让阈值差一个数量级，
	// 表现是「一根柱子都没找到」——而不是报错。
	n := p.w
	span := p.h
	if horizontal {
		n, span = p.h, p.w
	}
	thick := max(2, int(colMinRatio*float64(span)))
	proj := make([]int, n)
	if horizontal {
		for y := 0; y < p.h; y++ {
			n := 0
			for x := 0; x < p.w; x++ {
				if m[y*p.w+x] {
					n++
				}
			}
			proj[y] = n
		}
	} else {
		for x := 0; x < p.w; x++ {
			n := 0
			for y := 0; y < p.h; y++ {
				if m[y*p.w+x] {
					n++
				}
			}
			proj[x] = n
		}
	}
	var segs [][2]int
	start := -1
	for i, v := range proj {
		on := v >= thick
		switch {
		case on && start < 0:
			start = i
		case !on && start >= 0:
			if i-start >= 4 {
				segs = append(segs, [2]int{start, i - 1})
			}
			start = -1
		}
	}
	if start >= 0 && len(proj)-start >= 4 {
		segs = append(segs, [2]int{start, len(proj) - 1})
	}
	return segs
}

// extent 求某一段色簇在垂直于主轴方向上的范围，拼成完整矩形。
func (p plane) extent(m []bool, seg [2]int, horizontal bool) (Rect, bool) {
	if horizontal {
		x0, x1 := p.w, -1
		for y := seg[0]; y <= seg[1]; y++ {
			for x := 0; x < p.w; x++ {
				if m[y*p.w+x] {
					x0 = min(x0, x)
					x1 = max(x1, x)
				}
			}
		}
		if x1 < 0 {
			return Rect{}, false
		}
		return Rect{X: x0, Y: seg[0], W: x1 - x0 + 1, H: seg[1] - seg[0] + 1}, true
	}
	y0, y1 := p.h, -1
	for x := seg[0]; x <= seg[1]; x++ {
		for y := 0; y < p.h; y++ {
			if m[y*p.w+x] {
				y0 = min(y0, y)
				y1 = max(y1, y)
			}
		}
	}
	if y1 < 0 {
		return Rect{}, false
	}
	return Rect{X: seg[0], Y: y0, W: seg[1] - seg[0] + 1, H: y1 - y0 + 1}, true
}

// labelPad 量数据标签离柱体有多远；量不到返回 0。
//
// 标签画在柱体包围盒**之外**：垂直柱在柱顶上方，水平条在条的右端。焦点矩形只框
// 柱体时，这根自己的数字会被压在纱下面——「指谁谁灰」，图上最该清楚的数反而看不见。
// 量而不是用常数：水平条的标签宽度随位数变（"174" 与 "96" 差一倍），一个常数盖不住。
func (p plane) labelPad(r Rect, horizontal bool) int {
	dark := func(i int) bool {
		return max(p.r[i], max(p.g[i], p.b[i])) < labelDark
	}
	if horizontal {
		x0 := r.X + r.W
		x1 := min(p.w, x0+labelLookahead)
		last := -1
		for x := x0; x < x1; x++ {
			for y := r.Y; y < r.Y+r.H; y++ {
				if dark(y*p.w + x) {
					last = x
					break
				}
			}
		}
		if last < 0 {
			return 0
		}
		return last - x0 + 1 + labelMargin
	}
	y0 := max(0, r.Y-labelLookback)
	first := -1
	for y := y0; y < r.Y; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			if dark(y*p.w + x) {
				first = y
				break
			}
		}
	}
	if first < 0 {
		return 0
	}
	return r.Y - first + labelMargin
}
