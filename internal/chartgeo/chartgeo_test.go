package chartgeo

import (
	"image"
	"image/color"
	"image/draw"
	"strings"
	"testing"
)

var (
	white = color.RGBA{255, 255, 255, 255}
	blue  = color.RGBA{79, 129, 189, 255}
	red   = color.RGBA{192, 80, 72, 255}
	black = color.RGBA{0, 0, 0, 255}
	gray  = color.RGBA{135, 135, 135, 255}
)

const (
	imgW, imgH = 1280, 720
	baseY      = 531
)

func newImg() *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, imgW, imgH))
	draw.Draw(m, m.Bounds(), image.NewUniform(white), image.Point{}, draw.Src)
	return m
}

func fill(m *image.RGBA, x0, y0, x1, y1 int, c color.Color) {
	draw.Draw(m, image.Rect(x0, y0, x1, y1), image.NewUniform(c), image.Point{}, draw.Src)
}

// label 在柱顶上方画一块墨，模拟数据标签（只用于量 labelPad，不追求字形）。
func label(m *image.RGBA, x, y int) { fill(m, x, y, x+30, y+14, black) }

func TestSplitVertical(t *testing.T) {
	m := newImg()
	for gy := 286; gy < baseY; gy += 24 { // 网格线：低饱和，不该被算成柱子
		fill(m, 236, gy, 1100, gy+1, gray)
	}
	heights := []int{246, 185, 135}
	for i, h := range heights {
		x := 256 + i*289
		fill(m, x, baseY-h, x+116, baseY, blue)
		label(m, x+30, baseY-h-20)
	}
	bars, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 1, false)
	if len(bars) != 3 {
		t.Fatalf("应切出 3 根，实得 %d：%s", len(bars), why)
	}
	for i, b := range bars {
		if b.Cat != i || b.Ser != 0 {
			t.Fatalf("第 %d 根的 类别/系列 应是 %d/0，实得 %d/%d", i, i, b.Cat, b.Ser)
		}
		if b.Rect.H != heights[i] {
			t.Fatalf("第 %d 根高度应 %d，实得 %d", i, heights[i], b.Rect.H)
		}
		if b.LabelDir != "up" || b.LabelPad <= 0 {
			t.Fatalf("第 %d 根应量到向上的标签带，实得 %q/%d", i, b.LabelDir, b.LabelPad)
		}
	}
}

func TestSplitHorizontalCategoryOrder(t *testing.T) {
	m := newImg()
	// 水平条的类别轴自下而上：最底下那根是第 0 个类别（174 最长）。
	// 自上而下 96 / 131 / 174，长度 135 / 185 / 246。
	type row struct{ y, length int }
	rows := []row{{300, 135}, {420, 185}, {540, 246}}
	for _, r := range rows {
		fill(m, 236, r.y, 236+r.length, r.y+41, blue)
	}
	bars, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 1, true)
	if len(bars) != 3 {
		t.Fatalf("应切出 3 根，实得 %d：%s", len(bars), why)
	}
	wantLen := []int{246, 185, 135}
	for i, b := range bars {
		if b.Cat != i {
			t.Fatalf("第 %d 根的类别下标应 %d，实得 %d", i, i, b.Cat)
		}
		if b.Rect.W != wantLen[i] {
			t.Fatalf("第 %d 根长度应 %d（自下而上），实得 %d", i, wantLen[i], b.Rect.W)
		}
	}
}

func TestSplitMultiSeries(t *testing.T) {
	m := newImg()
	// 每类别两根（蓝 130 宽 + 红 130 宽，间隔 4px），两组数值序不同——
	// 按系列分开校验才过得去，跨系列比会误判成「映射反了」。
	x0 := []int{256, 566, 876}
	hA := []int{246, 185, 135}
	hB := []int{135, 246, 185}
	for i := range x0 {
		fill(m, x0[i], baseY-hA[i], x0[i]+130, baseY, blue)
		fill(m, x0[i]+134, baseY-hB[i], x0[i]+264, baseY, red)
	}
	bars, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 2, false)
	if len(bars) != 6 {
		t.Fatalf("应切出 6 根，实得 %d：%s", len(bars), why)
	}
	// 排好序后是「两核(0) 两根、四核(1) 两根、八核(2) 两根」
	for i, b := range bars {
		if b.Cat != i/2 {
			t.Fatalf("第 %d 根的类别下标应 %d，实得 %d", i, i/2, b.Cat)
		}
		if b.Ser != i%2 {
			t.Fatalf("第 %d 根的系列下标应 %d，实得 %d", i, i%2, b.Ser)
		}
	}
	if bars[0].Rect.H != 246 || bars[1].Rect.H != 135 {
		t.Fatalf("两核两根应 246/135，实得 %d/%d", bars[0].Rect.H, bars[1].Rect.H)
	}
}

// 图例里同色的方块会被多切一段；它比柱子薄得多，按厚度滤掉后仍应切出 3 根。
func TestSplitIgnoresLegendSwatch(t *testing.T) {
	m := newImg()
	for i := 0; i < 3; i++ {
		x := 256 + i*289
		fill(m, x, baseY-100, x+116, baseY, blue)
	}
	fill(m, 1150, 200, 1200, 230, blue) // 图例色块：51×30
	bars, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 1, false)
	if len(bars) != 3 {
		t.Fatalf("图例色块应被厚度滤掉，仍切 3 根，实得 %d：%s", len(bars), why)
	}
}

// 柱宽不一致 → 说明量到的不是同一张图里的柱子，宁可未测。
func TestSplitRejectsUnevenWidth(t *testing.T) {
	m := newImg()
	fill(m, 256, baseY-100, 372, baseY, blue)
	fill(m, 545, baseY-100, 661, baseY, blue)
	fill(m, 834, baseY-100, 994, baseY, blue) // 160 宽
	_, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 1, false)
	if !strings.Contains(why, "不像同一张图") {
		t.Fatalf("应报柱宽不一致，实得 %q", why)
	}
}

// 水平 + 多系列：组内次序随坐标翻转，没有样本可验就不猜。
func TestSplitRejectsHorizontalMultiSeries(t *testing.T) {
	m := newImg()
	_, why := Split(m, Rect{0, 0, imgW, imgH}, 2, 2, true)
	if !strings.Contains(why, "单系列") {
		t.Fatalf("水平多系列应明确未切，实得 %q", why)
	}
}

// 段落数对不上（多一个同厚度的色块）→ 未测，而不是把邻居那根当成一根。
func TestSplitRejectsExtraSegment(t *testing.T) {
	m := newImg()
	for i := 0; i < 4; i++ { // 4 根同宽，但类别只有 3 个
		x := 200 + i*220
		fill(m, x, baseY-100, x+116, baseY, blue)
	}
	_, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 1, false)
	if !strings.Contains(why, "应为类别数 3") {
		t.Fatalf("应报段数与类别数不符，实得 %q", why)
	}
}
