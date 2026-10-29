package render

// orderLayersParentsFirst 把「被别的层包住的层」排到包含它的层之后。
//
// 为什么必须有这一步：BuildLayerAssets 为了 ADR-9（嵌套元素不能跟着父层的不透明度
// 出现）会把子元素从父层位图里抹掉，再单独出一张子层图。合成器按数组顺序绘制，
// 所以父层一旦排在子层之后，就会把子元素整块盖回底色——画面上的要点文字凭空消失，
// 而且没有任何报错、没有 warning、哈希也照常。
//
// 这个顺序原先只是一句注释里的事实（「当前编译产物按 track 顺序出层，正是这个顺序」），
// 而 track 顺序来自编排结果，IR 里没有任何东西保证它。把约束写成函数，才是约束。
//
// 只动「确实存在包含关系」的层，其余保持原序：两个并列且部分重叠的元素互换会有
// 真实的 z-order 变化，那不是本函数该管的事。
func orderLayersParentsFirst(layers []Layer) []Layer {
	if len(layers) < 2 {
		return layers
	}
	out := append([]Layer(nil), layers...)
	// 冒泡：一轮里只要交换过就再来一轮。层数是个位数量级，O(n²) 无所谓，
	// 换来的是「稳定」——并列层之间的相对顺序不会因为实现细节被重排。
	for changed := true; changed; {
		changed = false
		for i := 0; i+1 < len(out); i++ {
			if layerContains(out[i+1], out[i]) && !sameBox(out[i+1].Box, out[i].Box) {
				out[i], out[i+1] = out[i+1], out[i]
				changed = true
			}
		}
	}
	return out
}

// layerContains 判断 outer 是否完整包住 inner。
// 整页层（ElementID 为空）按画布大小参与判断；几何无效的层不参与，
// 否则 0×0 的层会被当成「被一切包住」而乱序。
func layerContains(outer, inner Layer) bool {
	if inner.Box.W <= 0 || inner.Box.H <= 0 || outer.Box.W <= 0 || outer.Box.H <= 0 {
		return false
	}
	return outer.Box.X <= inner.Box.X &&
		outer.Box.Y <= inner.Box.Y &&
		outer.Box.X+outer.Box.W >= inner.Box.X+inner.Box.W &&
		outer.Box.Y+outer.Box.H >= inner.Box.Y+inner.Box.H
}

func sameBox(a, b Box) bool { return a == b }

// partialOverlapMinRatio 是判定「部分重叠值得告警」的最小重叠面积占比（相对较小的一块）。
//
// 为什么要门槛：文本框的包围盒普遍带内边距，标题盒与副标题盒常有一两像素的交叠，
// 交叠区里根本没有墨——按纯几何判会天天误报，告警也就没人看了。5% 之下按不重叠处理。
const partialOverlapMinRatio = 0.05

// partialOverlaps 返回「互不包含、但确实相交」的层对（元素 id）。
//
// 这是 ADR-9 抹除算法管不到的一种情形：抹除的前提是包含关系——把子元素的矩形从父层
// 位图里挖掉，父层就不带子元素的像素。两个兄弟元素若部分重叠（谁都不包含谁），
// 两张层位图会各自烘进对方落在重叠区的像素，动谁另一个就跟着拖影（tasks 5.9）。
//
// 单张栅格推不出逐元素 alpha，所以这不是能修掉的缺陷，而是需要被看见的模型限制：
// 编译期把它变成一条诊断，比让它静默出现在成片里好。
func partialOverlaps(layers []Layer) [][2]string {
	var out [][2]string
	for i := 0; i < len(layers); i++ {
		for j := i + 1; j < len(layers); j++ {
			if !partialOverlap(layers[i], layers[j]) {
				continue
			}
			out = append(out, [2]string{layers[i].ElementID, layers[j].ElementID})
		}
	}
	return out
}

func partialOverlap(a, b Layer) bool {
	area := func(l Layer) int { return l.Box.W * l.Box.H }
	if area(a) <= 0 || area(b) <= 0 {
		return false
	}
	if layerContains(a, b) || layerContains(b, a) {
		return false
	}
	x0 := max(a.Box.X, b.Box.X)
	y0 := max(a.Box.Y, b.Box.Y)
	x1 := min(a.Box.X+a.Box.W, b.Box.X+b.Box.W)
	y1 := min(a.Box.Y+a.Box.H, b.Box.Y+b.Box.H)
	if x1 <= x0 || y1 <= y0 {
		return false
	}
	overlap := (x1 - x0) * (y1 - y0)
	smaller := min(area(a), area(b))
	return float64(overlap)/float64(smaller) >= partialOverlapMinRatio
}
