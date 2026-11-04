package app

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"strings"

	"github.com/F31/ppts/internal/chartgeo"
	"github.com/F31/ppts/internal/project"
)

// 本文件把「页面图 → 逐柱包围盒」接进解析流程。
//
// 为什么在产品里做这件事：`render.FocusSpan.Rect` 已经能表达「这一刻指一块矩形」，
// 但矩形得有人产出。内嵌图表的柱子不是图层（图表是一张位图，切不出可合成的层），
// 它只能从渲染后的页面图上量——而页面图正好在解析期就有（render.Pages）。
// 量出来的结果写进 document/extracted.json，下游（编辑器、编排层、导出）拿同一份
// 解析产物就能引用某根柱子，不必各自再跑一遍识别。
//
// 为什么不在导出期现算：导出会读同一份解析产物两次（几何 + 讲稿），识别是一次
// 纯函数式的像素运算，做一次落盘比每次导出重算稳——重算意味着「同一份讲稿两次
// 导出可能指向不同的框」，而那种差异只在成片里看得见。

// stdSlideHeightEMU 是辨认比例用的标准页高（7.5 in）。16:9 与 4:3 都是这个高，
// 只有页宽不同；因此「像素 / EMU」这一个标量就够了，不需要先知道页宽。
//
// 这不是精确解而是产品里既有的约定：PageBindElements 与 render 的页宽推断用的是
// 同一条（见 internal/render/compile.go 的注释）。自定义页高的 deck 会算偏，
// 表现是切不出柱子（报未切），而不会切出错的框——这条取舍是为了「宁可不指」。
const stdSlideHeightEMU = 6858000

// AnnotateChartBars 给一页上的每个图表形状补 `ChartBars`。
//
// pages 与 pngs 必须同序等长，且 pages 是**可见页**（渲染会跳过隐藏页）。返回补上的
// 图表个数；notes 是没补上的原因，逐条写给解析诊断用——「未测」必须留痕，
// 否则它和「这页没有图表」在产物里长得一样。
func AnnotateChartBars(pages []*project.Page, pngs [][]byte) (int, []string) {
	if len(pages) != len(pngs) {
		return 0, []string{fmt.Sprintf("页图与页数不一致（%d/%d），逐柱包围盒未补",
			len(pngs), len(pages))}
	}
	done := 0
	var notes []string
	for i, pg := range pages {
		if pg == nil || len(pngs[i]) == 0 {
			continue
		}
		shapes := chartShapes(pg)
		if len(shapes) == 0 {
			continue
		}
		img, err := png.Decode(bytes.NewReader(pngs[i]))
		if err != nil {
			notes = append(notes, fmt.Sprintf("页 %d：页图解不开（%v），逐柱包围盒未补", pg.Index, err))
			continue
		}
		// 像素 / EMU：只用一个标量。页宽由图片宽高比自带，不必单独知道。
		pxPerEMU := float64(img.Bounds().Dy()) / stdSlideHeightEMU
		for _, sh := range shapes {
			n, note := annotateShape(img, sh, pxPerEMU)
			if note != "" {
				notes = append(notes, fmt.Sprintf("页 %d 的图表 %s：%s", pg.Index, sh.ID, note))
			}
			if n > 0 {
				done++
			}
		}
	}
	return done, notes
}

// chartShapes 摊平一棵形状树里的图表形状（组合形状里的图表同样要量）。
func chartShapes(pg *project.Page) []*project.Shape {
	var out []*project.Shape
	var walk func([]*project.Shape)
	walk = func(list []*project.Shape) {
		for _, sh := range list {
			if sh == nil {
				continue
			}
			if len(sh.ChartSeries) > 0 && len(sh.ChartCategories) > 0 && sh.Bounds != nil {
				out = append(out, sh)
			}
			if len(sh.Children) > 0 {
				walk(sh.Children)
			}
		}
	}
	walk(pg.Shapes)
	return out
}

func annotateShape(img image.Image, sh *project.Shape, pxPerEMU float64) (int, string) {
	ctype := strings.ToLower(sh.ChartType)
	horizontal := false
	switch {
	case strings.HasPrefix(ctype, "column"):
	case strings.HasPrefix(ctype, "bar"):
		horizontal = true
	default:
		// 折线/饼/面积：画面上不是「一根柱占一个带」，切出来的段没有语义。
		// 这不是「暂不支持」，是做了会指错——所以留一条明确的未测原因。
		return 0, fmt.Sprintf("图表类型 %s 不是柱状/条形，未切", ctype)
	}
	if negativeValue(sh) {
		// 负值柱会翻到基线另一侧，长度不再反映大小，长度序校验会失效。
		return 0, "含负值，柱会翻到基线另一侧，未切"
	}
	b := sh.Bounds
	box := chartgeo.Rect{
		X: int(float64(b.X) * pxPerEMU), Y: int(float64(b.Y) * pxPerEMU),
		W: int(float64(b.Width) * pxPerEMU), H: int(float64(b.Height) * pxPerEMU),
	}
	bars, why := chartgeo.Split(img, box, len(sh.ChartCategories), len(sh.ChartSeries), horizontal)
	if len(bars) == 0 {
		return 0, why
	}

	// 图坐标 → 画布坐标（1920×1080）。下游的 Box/焦点矩形都在这一套坐标里。
	k := bindCanvasW / float64(img.Bounds().Dx())
	out := make([]project.ChartBar, 0, len(bars))
	for n, bar := range bars {
		if bar.Ser >= len(sh.ChartSeries) {
			continue
		}
		vals := sh.ChartSeries[bar.Ser].Values
		if bar.Cat >= len(vals) {
			continue
		}
		cb := project.ChartBar{
			ID:       fmt.Sprintf("%s-b%d", sh.ID, n),
			Category: chartCategoryAt(sh.ChartCategories, bar.Cat),
			Series:   sh.ChartSeries[bar.Ser].Name,
			Value:    vals[bar.Cat],
			LabelDir: bar.LabelDir,
			LabelPad: int(float64(bar.LabelPad) * k),
			// chartgeo 返回的是**裁剪块内**的坐标，必须加回图表的原点，
			// 否则整页所有柱子都会差一个「图表在页面上的位置」——框还在柱子上，
			// 只是整体平移，看起来像框对了，实际指偏了 144/173px。
			Box: [4]int{
				int(float64(box.X+bar.Rect.X) * k), int(float64(box.Y+bar.Rect.Y) * k),
				int(float64(bar.Rect.W) * k), int(float64(bar.Rect.H) * k),
			},
		}
		out = append(out, cb)
	}
	if len(out) < 2 {
		return 0, "切出的柱子不足 2 根，未采用"
	}
	sh.ChartBars = out
	sh.ChartOrientation = "vertical"
	if horizontal {
		sh.ChartOrientation = "horizontal"
	}
	return 1, ""
}

func chartCategoryAt(cats []string, i int) string {
	if i >= 0 && i < len(cats) {
		return cats[i]
	}
	return fmt.Sprintf("类别 %d", i+1)
}

func negativeValue(sh *project.Shape) bool {
	for _, s := range sh.ChartSeries {
		for _, v := range s.Values {
			if v < 0 {
				return true
			}
		}
	}
	return false
}
