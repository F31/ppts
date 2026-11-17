package render

import (
	"sort"

	"github.com/F31/ppts/internal/animation"
)

// 版式词表：同一套注视，按页结构换叠加，不换渲染器。
const (
	gazeLayoutGrid   = "grid"
	gazeLayoutList   = "list"
	gazeLayoutFigure = "figure"
	gazeLayoutHero   = "hero"
	gazeLayoutMixed  = "mixed"
)

// classifyGazeLayout 从元素几何判断这一页更像六宫格、条目、图/表还是大图。
func classifyGazeLayout(elements []Element, scale float64, canvasW, canvasH int) string {
	if canvasW <= 0 || canvasH <= 0 {
		return gazeLayoutMixed
	}
	pageArea := float64(canvasW) * float64(canvasH)
	var frames, bullets, figures []Box
	for _, el := range elements {
		box, _, ok, _ := boxOf(el.Bounds, scale, canvasW, canvasH)
		if !ok {
			continue
		}
		area := boxArea(box)
		switch el.Kind {
		case animation.KindShape:
			if pageArea > 0 {
				r := area / pageArea
				if r >= 0.02 && r < gazeMaxFrameRatio {
					frames = append(frames, box)
				}
			}
		case animation.KindBullet:
			bullets = append(bullets, box)
		case animation.KindImage, animation.KindChart, animation.KindTable:
			figures = append(figures, box)
		}
	}
	if similarGrid(frames) {
		return gazeLayoutGrid
	}
	var maxFig float64
	for _, b := range figures {
		if r := boxArea(b) / pageArea; r > maxFig {
			maxFig = r
		}
	}
	if maxFig >= 0.35 && len(bullets) <= 2 {
		return gazeLayoutHero
	}
	if maxFig >= 0.12 && len(frames) < 4 {
		return gazeLayoutFigure
	}
	if len(bullets) >= 3 && len(frames) < 4 {
		return gazeLayoutList
	}
	return gazeLayoutMixed
}

func similarGrid(frames []Box) bool {
	if len(frames) < 4 {
		return false
	}
	areas := make([]float64, len(frames))
	for i, f := range frames {
		areas[i] = boxArea(f)
	}
	sort.Float64s(areas)
	med := areas[len(areas)/2]
	if med <= 0 {
		return false
	}
	similar := 0
	cols := map[int]int{}
	for _, f := range frames {
		a := boxArea(f)
		if a < med*0.6 || a > med*1.5 {
			continue
		}
		similar++
		cols[f.X/48]++
	}
	if similar < 4 {
		return false
	}
	nCols := 0
	for _, n := range cols {
		if n >= 2 {
			nCols++
		}
	}
	return nCols >= 2
}

// applyGazeLayoutVocab 按版式改纱 / 圈选。讲课片一律整页：crop-to-fill 会把文字放大出画。
// catalog 的 lookCrop 留给以后「切近大图」开关，compile 在这里否决。
func applyGazeLayoutVocab(page *Page, elements []Element, scale float64, canvasW, canvasH int) {
	if page == nil || len(page.Focus) == 0 {
		return
	}
	page.LookCrop = false
	page.LookCropPadPx = 0
	if classifyGazeLayout(elements, scale, canvasW, canvasH) == gazeLayoutList {
		bumpFocusVeil(page, 1.15)
	}
}

func bumpFocusVeil(page *Page, k float64) {
	if page == nil || k <= 0 {
		return
	}
	for i := range page.Focus {
		a := page.Focus[i].VeilAlpha
		if a <= 0 {
			a = 0.45
		}
		a *= k
		if a > 0.60 {
			a = 0.60
		}
		page.Focus[i].VeilAlpha = a
	}
}
