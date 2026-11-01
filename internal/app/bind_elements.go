package app

import (
	"regexp"
	"strings"

	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/project"
)

// 本文件是「形状 → 绑定器元素」的换算（changes/animation-narration-binding tasks 2.1）。
//
// 为什么不复用 PageElements：那份换算产出的是 render.Element，只剩 {ID, Kind, Bounds}——
// 文本在换算时被丢掉，而无文本的容器（卡片底、圆形底座）也被排除（它们挂不上预设动效）。
// 绑定器两样都要：文本是字面匹配的全部依据，容器是「结构推断」与「同块带出」的锚点。
// 所以绑定器走自己的换算，位置就在 PageElements 之上——同一个 project.Page，不同的投影。
//
// **与实验脚本的口径差异（诚实记录）**：Python 版的版面分类（extract_pptx_elements.py）
// 主要靠字号：标题 32pt、正文 10–11pt、眉标 12pt。project.Shape 没有字号
// （go-pptx 适配层没有提取，SchemaVersion 1.0 也不含它），因此这里改用几何近似：
// 上半页里包围盒最高的文字块当标题。在这份真实讲义的五张被测页上，两条口径给出的
// 分类**逐元素一致**（见 bind_elements_test.go），但这仍是近似而不是等价：
// 一份「上半页有两行等高等宽大字」的页面会让它选错。要根治得把字号提取进
// project.Shape，那是另一个变更（涉及解析产物 schema 与既有快照），不在本轮。

// 1080p 画布。与 render 的 Canvas、实验脚本的 PAGE_W/PAGE_H 同一套基准。
const (
	bindCanvasW = 1920.0
	bindCanvasH = 1080.0
	// minBindAreaPx 以下的形状不进清单：零宽连线与分隔条在画面上产不出可见变化，
	// 绑上只会让报告多出一堆「误绑」——那是噪声，不是发现。
	minBindAreaPx = 16.0
	// titleMinHeightPx 是标题的下界。没有它，一行 12px 的眉标在「上半页最高」里
	// 也可能胜出（当页面上半没有别的文字时）。
	titleMinHeightPx = 60.0
)

var bindMarkerRe = regexp.MustCompile(`^0?\p{Nd}{1,2}$`)

type bindRaw struct {
	el   bind.Element
	text bool
}

// PageBindElements 把一页的形状投影成绑定器输入：版面 kind + 1080p 包围盒 + 文本。
//
// slideWEMU/slideHEMU 是源页尺寸（EMU）。传 0 时按 16:9 标准页处理：绝大多数 pptx
// 都是这个尺寸，而绑定器只做相对比较（方位用 0.5 切线、簇用像素阈值），尺寸差一档
// 只会让 60px 的紧邻阈值等效变化，不会让结果翻掉。
func PageBindElements(page *project.Page, slideWEMU, slideHEMU int64) []bind.Element {
	if page == nil {
		return nil
	}
	if slideWEMU <= 0 {
		slideWEMU = 12192000
	}
	if slideHEMU <= 0 {
		slideHEMU = 6858000
	}

	var list []bindRaw
	var walk func([]*project.Shape)
	walk = func(shapes []*project.Shape) {
		for _, sh := range shapes {
			if sh == nil || sh.ID == "" || sh.Opaque || sh.Bounds == nil {
				continue
			}
			b := bind.Box{
				X: pxOf(sh.Bounds.X, slideWEMU, bindCanvasW),
				Y: pxOf(sh.Bounds.Y, slideHEMU, bindCanvasH),
				W: pxOf(sh.Bounds.Width, slideWEMU, bindCanvasW),
				H: pxOf(sh.Bounds.Height, slideHEMU, bindCanvasH),
			}
			// 负宽高不是合法包围盒（解析侧偶发的空几何），按 0 处理而不是取绝对值：
			// 取绝对值会把一条零高横线变成一条有高度的块，面积过滤就失效了。
			b.W = max(b.W, 0)
			b.H = max(b.H, 0)
			text := strings.TrimSpace(sh.Text)
			if b.W*b.H >= minBindAreaPx {
				list = append(list, bindRaw{
					el:   bind.Element{ID: sh.ID, Kind: baseBindKind(sh, b, text), Box: b, Text: text},
					text: text != "",
				})
			}
			if len(sh.Children) > 0 {
				walk(sh.Children)
			}
		}
	}
	walk(page.Shapes)

	// 第二遍：定标题。先定标题、再判眉标/页脚——反过来的话，顶部那行大字页标题
	// 会先被「顶部窄条」判成眉标，于是整页没有标题，标题先验跟着失效。
	if i := pickBindTitle(list); i >= 0 {
		list[i].el.Kind = "title"
	}
	out := make([]bind.Element, 0, len(list))
	for _, r := range list {
		out = append(out, r.el)
	}
	return out
}

// pickBindTitle 返回上半页包围盒最高的文字块下标（无合适候选返回 -1）。
//
// 「上半页」用中心 y < 0.5H：page 2 的结论段落在表格下方、高度 130px，比页标题的
// 129px 还高——不加位置约束，整页的标题会变成表格下面那句总结。
func pickBindTitle(list []bindRaw) int {
	best := -1
	for i, r := range list {
		if !r.text {
			continue
		}
		switch r.el.Kind {
		case "marker", "page_number", "shape", "image", "table", "chart":
			continue
		}
		if r.el.Box.Y+r.el.Box.H/2 >= bindCanvasH*0.5 {
			continue
		}
		if r.el.Box.H < titleMinHeightPx {
			continue
		}
		if best < 0 || r.el.Box.H > list[best].el.Box.H {
			best = i
		}
	}
	return best
}

func baseBindKind(sh *project.Shape, b bind.Box, text string) string {
	kind := strings.ToLower(strings.TrimSpace(sh.Kind))
	switch kind {
	case "picture", "image":
		return "image"
	case "chart":
		return "chart"
	case "table":
		return "table"
	}
	if sh.ChartType != "" {
		return "chart"
	}
	if sh.TableRows > 0 || sh.TableCols > 0 {
		return "table"
	}
	if text == "" {
		return "shape"
	}
	// 页码：右下角两位数字。位置判据不能少——正文里也有「01」这种编号。
	if bindMarkerRe.MatchString(text) && b.Y > bindCanvasH*0.85 && b.X > bindCanvasW*0.85 {
		return "page_number"
	}
	// 编号标记：纯数字的列表序号。与正文分开是有用的：计数规则只认它，
	// 不会把「174s」这种带单位的数字当成序号。
	if bindMarkerRe.MatchString(text) {
		return "marker"
	}
	if b.Y < bindCanvasH*0.15 && len([]rune(text)) <= 40 {
		return "eyebrow"
	}
	if b.Y > bindCanvasH*0.85 {
		return "note"
	}
	return "bullet"
}

func pxOf(emu, totalEMU int64, totalPx float64) float64 {
	if totalEMU <= 0 {
		return 0
	}
	return float64(emu) / float64(totalEMU) * totalPx
}
