package app

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/render"
)

// 本文件回答「编辑器该让用户指什么」。
//
// 与 FocusBox 的分工：FocusBox 只回答"框在哪"——导出侧拿到矩形就够了，它不需要名字。
// 但编辑器面对的是人：一串 `shape-7` / `p1-s2-b0` 无法选择，必须给出
// 「Q1 · 系列 A = 174」这样的标签，否则"选焦点"会变成猜内部 id。
//
// 两份数据都从同一份 extracted document 来，不额外落产物：标签（类别/系列/数值、
// 形状文字）在解析期就已经写在 document.json 里了，这里只是换个face呈现。

// FocusTarget 是一条候选焦点。
type FocusTarget struct {
	ShapeID string
	// BarID 空 = 整个形状；非空 = 图表里的某一根柱子。
	BarID string
	// Kind 是 text / picture / bar。前端按它决定图标与分组。
	Kind  string
	Label string
	// Category/Series/Value 只在柱子上出现；HasValue 区分"值就是 0"与"没有值"。
	Category string
	Series   string
	Value    float64
	HasValue bool
	// Box 是页坐标 EMU，与 FocusBox.Box 同一口径（前端目前不用，将来画布点选要用）。
	Box render.EMUBox
}

// PageFocusTargets 是一页的候选清单。
type PageFocusTargets struct {
	Targets []FocusTarget
	// Note 说明这份清单为什么可能不全（例如某张图没切开）。空 = 没有要交代的。
	//
	// 不写进 Targets：一个"不可选"的条目混在可选列表里，用户会点它然后发现无效；
	// 而完全不提，用户又会以为这张图本来就不能指。
	Note string
	// Orientation 是本页图表的朝向（vertical/horizontal），多个图表朝向不同时为空。
	Orientation string
}

// FocusTargetsForPage 把一页的形状摊成候选清单（形状按声明顺序，柱子按切柱顺序）。
//
// 只做单页：编辑器一次只开一页，而整份文档的解码成本已经在读 document.json 时付过了。
// 要做批量时再加，别先写一个没人调的整篇版本。
func FocusTargetsForPage(page *project.Page) PageFocusTargets {
	var targets []FocusTarget
	var notes []string
	orientations := map[string]bool{}

	var walk func([]*project.Shape)
	walk = func(shapes []*project.Shape) {
		for _, sh := range shapes {
			if sh == nil || sh.ID == "" {
				continue
			}
			if len(sh.ChartBars) > 0 {
				// 图表切开后只列柱子，不列整块图表：整块压暗等于"这一页只剩图表亮"，
				// 视觉上没有意义；而"讲整体趋势"本来也不该抢走逐柱的指代。
				if sh.ChartOrientation != "" {
					orientations[sh.ChartOrientation] = true
				}
				for i, bar := range sh.ChartBars {
					if bar.ID == "" {
						continue
					}
					targets = append(targets, FocusTarget{
						ShapeID: sh.ID, BarID: bar.ID, Kind: "bar",
						Label:    barLabel(bar, i),
						Category: bar.Category, Series: bar.Series,
						Value: bar.Value, HasValue: true,
						Box: render.EMUBox{
							X: pxToEMU(bar.Box[0]), Y: pxToEMU(bar.Box[1]),
							Width: pxToEMU(bar.Box[2]), Height: pxToEMU(bar.Box[3]),
						},
					})
				}
			} else if sh.ChartType != "" {
				// 是图表但没切开（折线/饼图/柱宽不一致/数值与像素矛盾/渲染器不可用）。
				// 注意负值与水平多系列 2026-11-04 起能切了，不要再写在这里——
				// 把「能指」说成「不能指」会让用户放弃一个本来就有的选择。
				// 这里必须说清楚，否则用户会以为"这张图没有柱子可指"。
				notes = append(notes, fmt.Sprintf("图表 %s 未逐柱切开，不能单独指某一根", shapeDisplayName(sh)))
			} else if sh.Bounds != nil {
				if text := shapeTextLabel(sh); text != "" {
					targets = append(targets, FocusTarget{
						ShapeID: sh.ID, Kind: "text", Label: text,
						Box: emuBoxOf(sh.Bounds),
					})
				} else if sh.Kind == "picture" {
					targets = append(targets, FocusTarget{
						ShapeID: sh.ID, Kind: "picture", Label: shapeDisplayName(sh),
						Box: emuBoxOf(sh.Bounds),
					})
				}
				// 其余（装饰线、底板、无文字的占位）不列：一页几十个形状会给出一串
				// 「未命名形状 12」，把真正的选项淹掉。
			}
			if len(sh.Children) > 0 {
				walk(sh.Children)
			}
		}
	}
	walk(page.Shapes)

	pt := PageFocusTargets{Targets: targets}
	if len(notes) > 0 {
		pt.Note = strings.Join(notes, "；")
	}
	if len(orientations) == 1 {
		for k := range orientations {
			pt.Orientation = k
		}
	}
	return pt
}

// barLabel 生成柱子的可读标签：类别 · 系列 = 值。
//
// 缺哪一段就省哪一段（单系列图表的 Series 常为空；类别轴偶尔也没写）。
// 全都缺时退成「第 N 根」——宁可给个位置描述，也不给一个空标签让人无可选择。
func barLabel(bar project.ChartBar, index int) string {
	parts := make([]string, 0, 2)
	if c := strings.TrimSpace(bar.Category); c != "" {
		parts = append(parts, c)
	}
	if s := strings.TrimSpace(bar.Series); s != "" {
		parts = append(parts, s)
	}
	label := strings.Join(parts, " · ")
	if label == "" {
		label = fmt.Sprintf("第 %d 根", index+1)
	}
	return label + " = " + formatFocusValue(bar.Value)
}

// formatFocusValue 让整数值不带 ".0"（174 而不是 174.0）：标签是给人看的。
func formatFocusValue(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "—"
	}
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// shapeTextLabel 取形状文字的第一行并截断。
//
// 截断是必要的：一个内容框可能有上百字，整段塞进下拉列表会把选择器撑爆。
// 按 rune 截而不是按字节，中文不会截出半个字。
func shapeTextLabel(sh *project.Shape) string {
	text := strings.TrimSpace(sh.Text)
	if text == "" {
		return ""
	}
	if i := strings.IndexAny(text, "\r\n"); i >= 0 {
		text = strings.TrimSpace(text[:i])
	}
	const maxRunes = 24
	runes := []rune(text)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return text
}

// shapeDisplayName 是形状的名字兜底：Name → AltText → ID。
func shapeDisplayName(sh *project.Shape) string {
	if n := strings.TrimSpace(sh.Name); n != "" {
		return n
	}
	if alt := strings.TrimSpace(sh.AltText); alt != "" {
		return alt
	}
	return sh.ID
}

func emuBoxOf(b *project.Box) render.EMUBox {
	return render.EMUBox{X: b.X, Y: b.Y, Width: b.Width, Height: b.Height}
}
