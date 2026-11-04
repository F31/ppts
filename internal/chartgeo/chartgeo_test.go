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
	bars, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 1, false, [][]float64{{174, 131, 96}})
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
	bars, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 1, true, [][]float64{{174, 131, 96}})
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
	bars, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 2, false, [][]float64{{174, 131, 96}, {96, 174, 131}})
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
	bars, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 1, false, [][]float64{{174, 131, 96}})
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
	_, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 1, false, [][]float64{{174, 131, 96}})
	if !strings.Contains(why, "不像同一张图") {
		t.Fatalf("应报柱宽不一致，实得 %q", why)
	}
}

// 水平 + 多系列：组内 y 升序对应系列**倒序**。
//
// 布局照 LibreOffice 渲染的自造 deck 摆：每个类别一组，组内上面那根是最后声明的
// 系列（图例次序也一样倒着写）。这里刻意让两个系列的值序不同——若把组内次序
// 当成正序，系列会互相张冠李戴，而框本身看着完全正常。
func TestSplitHorizontalMultiSeries(t *testing.T) {
	m := newImg()
	// (y, 系列0 长度, 系列1 长度)：y 从上到下是类别 2、1、0。
	type grp struct{ y, len0, len1 int }
	groups := []grp{{300, 135, 200}, {420, 185, 250}, {540, 246, 90}}
	for _, g := range groups {
		fill(m, 236, g.y, 236+g.len1, g.y+41, red)     // 系列 1 在上
		fill(m, 236, g.y+45, 236+g.len0, g.y+86, blue) // 系列 0 在下
		// 数据标签画在条的右端之外，两根各在自己的行带里——量的是「这根自己的数字」。
		fill(m, 240+g.len1, g.y+12, 264+g.len1, g.y+26, black)
		fill(m, 240+g.len0, g.y+57, 264+g.len0, g.y+71, black)
	}
	bars, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 2, true, [][]float64{
		{174, 131, 96}, // 系列 0（耗时）
		{60, 200, 140}, // 系列 1（预算）
	})
	if len(bars) != 6 {
		t.Fatalf("应切出 6 根，实得 %d：%s", len(bars), why)
	}
	// 排好序是「类别 0 的两根、类别 1 的两根、类别 2 的两根」。
	want := []struct{ cat, ser, length int }{
		{0, 0, 246}, {0, 1, 90},
		{1, 0, 185}, {1, 1, 250},
		{2, 0, 135}, {2, 1, 200},
	}
	for i, w := range want {
		b := bars[i]
		if b.Cat != w.cat || b.Ser != w.ser {
			t.Fatalf("第 %d 根应是 类别%d/系列%d，实得 %d/%d", i, w.cat, w.ser, b.Cat, b.Ser)
		}
		if b.Rect.W != w.length {
			t.Fatalf("第 %d 根长度应 %d，实得 %d", i, w.length, b.Rect.W)
		}
	}
	for i, b := range bars {
		if b.LabelDir != "right" || b.LabelPad <= 0 {
			t.Fatalf("第 %d 根应量到向右的标签带，实得 %q/%d", i, b.LabelDir, b.LabelPad)
		}
	}
}

// 段落数对不上（多一个同厚度的色块）→ 未测，而不是把邻居那根当成一根。
func TestSplitRejectsExtraSegment(t *testing.T) {
	m := newImg()
	for i := 0; i < 4; i++ { // 4 根同宽，但类别只有 3 个
		x := 200 + i*220
		fill(m, x, baseY-100, x+116, baseY, blue)
	}
	_, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 1, false, [][]float64{{174, 131, 96}})
	if !strings.Contains(why, "应为类别数 3") {
		t.Fatalf("应报段数与类别数不符，实得 %q", why)
	}
}

// 垂直柱含负值：负值柱翻到零线以下，基线从「底边」换成「顶边」。
//
// 这条是照真实渲染（LibreOffice + 三页 deck）摆的：零线在 y=402，-40 那根从零线
// 往下长 82px，标签落在柱体**下方**。若沿用「底边即基线」，负值柱的底边会落在
// 它自己的下沿上，与正值柱差 82px——旧实现正是靠这条差异把它判成「量错了」，
// 于是整张图报未测。
func TestSplitVerticalNegative(t *testing.T) {
	m := newImg()
	const zeroY = 402
	type bar struct {
		x, top, height int
	}
	bars := []bar{
		{256, zeroY, 82},        // -40：零线以下
		{545, zeroY - 246, 246}, // 120
		{834, zeroY - 164, 164}, // 80
	}
	for _, b := range bars {
		fill(m, b.x, b.top, b.x+116, b.top+b.height, blue)
	}
	fill(m, 286, 490, 310, 504, black) // -40 的数字在柱体下方
	fill(m, 575, zeroY-266, 599, zeroY-252, black)
	fill(m, 864, zeroY-184, 888, zeroY-170, black)

	got, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 1, false, [][]float64{{-40, 120, 80}})
	if len(got) != 3 {
		t.Fatalf("应切出 3 根，实得 %d：%s", len(got), why)
	}
	if got[0].Rect.H != 82 || got[0].Rect.Y != zeroY {
		t.Fatalf("负值柱应是零线下方 82px，实得 y=%d h=%d", got[0].Rect.Y, got[0].Rect.H)
	}
	if got[0].LabelDir != "down" || got[0].LabelPad <= 0 {
		t.Fatalf("负值柱的标签应朝下，实得 %q/%d", got[0].LabelDir, got[0].LabelPad)
	}
	for i := 1; i < 3; i++ {
		if got[i].LabelDir != "up" || got[i].LabelPad <= 0 {
			t.Fatalf("第 %d 根是正值柱，标签应朝上，实得 %q/%d", i, got[i].LabelDir, got[i].LabelPad)
		}
	}
}

// 水平条含负值：负值条向**左**伸出，标签在左端。
func TestSplitHorizontalNegative(t *testing.T) {
	m := newImg()
	const zeroX = 343
	type bar struct {
		y, left, width int
	}
	// 自下而上是类别 0、1、2。
	bars := []bar{
		{540, zeroX - 82, 82}, // -40：零线左侧
		{420, zeroX, 246},     // 120
		{300, zeroX, 164},     // 80
	}
	for _, b := range bars {
		fill(m, b.left, b.y, b.left+b.width, b.y+41, blue)
	}
	fill(m, 220, 550, 250, 564, black) // -40 的数字在条左端之外
	fill(m, 600, 430, 624, 444, black)
	fill(m, 515, 310, 539, 324, black)

	got, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 1, true, [][]float64{{-40, 120, 80}})
	if len(got) != 3 {
		t.Fatalf("应切出 3 根，实得 %d：%s", len(got), why)
	}
	if got[0].Rect.X != zeroX-82 || got[0].Rect.W != 82 {
		t.Fatalf("负值条应是零线左侧 82px，实得 x=%d w=%d", got[0].Rect.X, got[0].Rect.W)
	}
	if got[0].LabelDir != "left" || got[0].LabelPad <= 0 {
		t.Fatalf("负值条的标签应朝左，实得 %q/%d", got[0].LabelDir, got[0].LabelPad)
	}
	for i := 1; i < 3; i++ {
		if got[i].LabelDir != "right" || got[i].LabelPad <= 0 {
			t.Fatalf("第 %d 根是正值条，标签应朝右，实得 %q/%d", i, got[i].LabelDir, got[i].LabelPad)
		}
	}
}

// 正负柱的基线不在同一条零线上 → 未测。
//
// 这是「数值说这边、像素画那边」的形态：把一个正值柱往下挪 22px，它的底边就
// 不再是零线。交叉校验必须抓住它——否则焦点框会按错位的基线去扩标签带。
func TestSplitRejectsSplitBaseline(t *testing.T) {
	m := newImg()
	const zeroY = 402
	fill(m, 256, zeroY, 372, zeroY+82, blue)     // -40，正确
	fill(m, 545, zeroY-130, 661, zeroY-22, blue) // 120，底边离零线 22px
	fill(m, 834, zeroY-164, 950, zeroY, blue)    // 80，正确
	_, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 1, false, [][]float64{{-40, 120, 80}})
	if !strings.Contains(why, "零线") {
		t.Fatalf("应报正负柱基线不一致，实得 %q", why)
	}
}

// 长度序与数值序相反 → 未测。这条是「框看着对、指错了」的最后一道门：
// 类别/系列标错时框仍然落在柱子上，只有长度序能发现。
func TestSplitRejectsReversedLengthOrder(t *testing.T) {
	m := newImg()
	heights := []int{135, 185, 246} // 与数值序 174/131/96 相反
	for i, h := range heights {
		x := 256 + i*289
		fill(m, x, baseY-h, x+116, baseY, blue)
	}
	_, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 1, false, [][]float64{{174, 131, 96}})
	if !strings.Contains(why, "长度序与数值序相反") {
		t.Fatalf("应报长度序相反，实得 %q", why)
	}
}

// 数值矩阵与声明的类别/系列对不上 → 未测，而不是少切一根当成切全了。
func TestSplitRejectsValueShapeMismatch(t *testing.T) {
	m := newImg()
	for i := 0; i < 3; i++ {
		x := 256 + i*289
		fill(m, x, baseY-100, x+116, baseY, blue)
	}
	_, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 1, false, [][]float64{{174, 131}})
	if !strings.Contains(why, "只有 2 个数值") {
		t.Fatalf("应报数值不足，实得 %q", why)
	}
	if _, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 2, false, [][]float64{{174, 131, 96}}); !strings.Contains(why, "数值矩阵有 1 个系列") {
		t.Fatalf("应报系列数不足，实得 %q", why)
	}
}
