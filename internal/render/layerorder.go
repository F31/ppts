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
