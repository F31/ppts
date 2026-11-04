package main

import (
	"testing"

	"github.com/F31/ppts/internal/render"
)

// barRects 是把「讲稿里写的柱子 id」翻成「焦点矩形」的那一步。
// 三个口径都在这里守：扩标签、不扩无标签、贴顶边时不越界。
func TestBarRects(t *testing.T) {
	elems := []element{
		{ID: "p1-s2", Kind: "chart", Chart: &chartInfo{
			DataLabels: true,
			Bars: []chartBar{
				{ID: "p1-s2-b0", Box: [4]int{385, 429, 173, 370}},
				{ID: "p1-s2-b1", Box: [4]int{818, 30, 174, 100}},
				{ID: "p1-s2-b9", Box: [4]int{0, 0, 0, 0}},
			},
		}},
		{ID: "p2-s1", Kind: "chart", Chart: &chartInfo{
			DataLabels: false,
			Bars:       []chartBar{{ID: "p2-s1-b0", Box: [4]int{100, 200, 50, 60}}},
		}},
	}
	got := barRects(elems)

	// 带标签：向上扩 45px，高度同步加上——标签画在柱体包围盒之外，
	// 不扩进来「讲的是这根」对了，但这根自己的数字被压在纱下面。
	if r := got["p1-s2-b0"]; r != (render.Box{X: 385, Y: 429 - focusLabelPadPx, W: 173, H: 370 + focusLabelPadPx}) {
		t.Fatalf("带标签的柱子应向上扩 %dpx，实得 %+v", focusLabelPadPx, r)
	}
	// 贴顶边：能扩多少扩多少，矩形不得越出画布（Validate 会拒绝负原点）。
	if r := got["p1-s2-b1"]; r.Y != 0 || r.H != 100+30 {
		t.Fatalf("贴顶边应扩到 0 为止（Y=0,H=130），实得 %+v", r)
	}
	// 无标签：不扩。向上扩出一块空白等于在柱子上方留一条没被压暗的带。
	if r := got["p2-s1-b0"]; r != (render.Box{X: 100, Y: 200, W: 50, H: 60}) {
		t.Fatalf("无标签的柱子不该扩，实得 %+v", r)
	}
	// 无效框不写进表：0×0 会被当成「已指定」，让焦点静默无效。
	if _, ok := got["p1-s2-b9"]; ok {
		t.Fatal("0×0 的柱子不该进表")
	}
	if len(got) != 3 {
		t.Fatalf("应摊平 3 根柱子，实得 %d：%v", len(got), got)
	}
}

// 没有 chart 块的元素不该被当成图表。
func TestBarRectsIgnoresPlainElements(t *testing.T) {
	if got := barRects([]element{{ID: "t1", Kind: "title", Box: [4]int{0, 0, 10, 10}}}); len(got) != 0 {
		t.Fatalf("普通元素不该产出柱子矩形：%v", got)
	}
}
