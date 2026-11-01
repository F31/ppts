package render

import "github.com/F31/ppts/internal/animation"

// continuationCarryMS 是「静态盒补偿」的收敛时长上限。
//
// 为什么是短的固定值、而不是铺满整条 clip 的剩余时长：carry 不是作者写的动效，它是页缝处
// 静态盒差的**修正量**。修正应当尽快结束——否则元素会整段戏都在缓慢漂移，看起来像没对准。
// 取 500ms：30fps / 60fps 下都是整数帧，且短于一次常规转场（360ms）之后的注意力停留。
const continuationCarryMS int64 = 500

// CarryDuration 返回盒补偿的收敛时长：不超过 clip 的剩余时间、不超过本页时长、上限 500ms。
//
// 返回 0 表示没有可用的收敛窗口（clip 已在该页演完，或页长为 0），调用方应跳过补偿。
func CarryDuration(remaining, pageMS int64) int64 {
	ms := continuationCarryMS
	if remaining < ms {
		ms = remaining
	}
	if pageMS < ms {
		ms = pageMS
	}
	if ms < 0 {
		return 0
	}
	return ms
}

// CarryProps 计算跨页延续的「静态盒补偿」属性：把次页层从上一页的盒线性搬回本页的盒。
//
// 只补盒差（位置 + 尺寸），不补内容：同一个 elementId 在不同页上的图案差异（换了图、换了
// 颜色）无法插值，也不该插值——那已经不是「同一个东西在动」了。这条边界由 elementId 的
// 语义决定：elementId 相同 = 同一个元素的两种布局，不是两个元素。
//
// 数学（与 shared/motion.ts 的合成规则一致：translate 相加、scale 相乘、锚点来自 transform-origin）：
//
//	层节点：left=Cx top=Cy 宽=Cw 高=Ch，锚点局部偏移 O，变换 translate(t)·scale(s)
//	视觉左上角 = C + O·(1-s) + t      视觉尺寸 = (sx·Cw, sy·Ch)
//
// 令 p=0 时的视觉矩形等于上一页的盒 B：
//
//	sx = Bw/Cw, sy = Bh/Ch
//	tx = (Bx-Cx) - Ox·(1-sx),  ty = (By-Cy) - Oy·(1-sy)
//
// 关键在 `O·(1-s)` 这一项：缺了它，凡锚点不在左上角且尺寸有变化的延续都会在页缝偏一段
// （center 锚点、缩到 0.8 时偏 10% 的尺寸）。p=1 时 s=1、t=0，即本页盒本身。
//
// 两个盒逐字段相同时返回 nil——此时**不产生任何 clip**。这保证作者按老写法（相邻页把元素
// 放在同一位置）产出的清单与引入本能力前逐字节一致，planHash 也不变。
func CarryProps(prev, next Box, origin string) []animation.MotionProp {
	if prev == next || next.W <= 0 || next.H <= 0 {
		return nil
	}
	sx := float64(prev.W) / float64(next.W)
	sy := float64(prev.H) / float64(next.H)
	ox, oy := originOffset(origin, next.W, next.H)
	tx := float64(prev.X-next.X) - ox*(1-sx)
	ty := float64(prev.Y-next.Y) - oy*(1-sy)

	props := make([]animation.MotionProp, 0, 4)
	add := func(name string, from, to float64) {
		// 恒等分量（scale 1→1 / translate 0→0）不写：它们对像素没有任何作用，只会让清单
		// 更难读，也让「位置相同、仅尺寸变了」这类情形看起来像出了四个属性。
		if from != to {
			props = append(props, animation.MotionProp{Name: name, From: from, To: to})
		}
	}
	add(animation.PropScaleX, sx, 1)
	add(animation.PropScaleY, sy, 1)
	add(animation.PropTranslateX, tx, 0)
	add(animation.PropTranslateY, ty, 0)
	if len(props) == 0 {
		return nil
	}
	return props
}

// originOffset 把锚点名翻成层内局部像素偏移，与 shared/motion.ts 的 originCss 一一对应。
func originOffset(origin string, w, h int) (float64, float64) {
	fw, fh := float64(w), float64(h)
	switch origin {
	case OriginTop:
		return fw / 2, 0
	case OriginBottom:
		return fw / 2, fh
	case OriginLeft:
		return 0, fh / 2
	case OriginRight:
		return fw, fh / 2
	default: // "" 与 OriginCenter
		return fw / 2, fh / 2
	}
}
