package app

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/project"
)

var (
	cbWhite = color.RGBA{255, 255, 255, 255}
	cbBlue  = color.RGBA{79, 129, 189, 255}
	cbBlack = color.RGBA{0, 0, 0, 255}
)

// 一份 16:9 页图 + 满页图表形状：三根柱、柱顶有标签带。
// 页图 1920×1080 对应 12192000×6858000 EMU，于是 px/EMU = 1080/6858000。
func chartPagePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(m, m.Bounds(), image.NewUniform(cbWhite), image.Point{}, draw.Src)
	baseY := h * 3 / 4
	for i, bh := range []int{h / 4, h / 6, h / 9} {
		x := w/8 + i*(w/5)
		bw := w / 8
		draw.Draw(m, image.Rect(x, baseY-bh, x+bw, baseY), image.NewUniform(cbBlue), image.Point{}, draw.Src)
		// 标签带：柱顶上方的一小块墨。
		draw.Draw(m, image.Rect(x+bw/4, baseY-bh-20, x+bw*3/4, baseY-bh-6),
			image.NewUniform(cbBlack), image.Point{}, draw.Src)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, m); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func chartShape(bounds *project.Box) *project.Shape {
	return &project.Shape{
		ID: "p1-s2", Kind: "chart", ChartType: "column_clustered",
		ChartCategories: []string{"2 核", "4 核", "8 核"},
		ChartSeries: []project.ChartSeriesData{
			{Name: "耗时", Values: []float64{174, 131, 96}},
		},
		Bounds: bounds,
	}
}

func TestAnnotateChartBars(t *testing.T) {
	img := chartPagePNG(t, 1920, 1080)
	sh := chartShape(&project.Box{X: 0, Y: 0, Width: 12192000, Height: 6858000})
	pg := &project.Page{Index: 0, SlideID: "slide-0", Shapes: []*project.Shape{sh}}

	n, notes := AnnotateChartBars([]*project.Page{pg}, [][]byte{img})
	if n != 1 || len(sh.ChartBars) != 3 {
		t.Fatalf("应补 1 个图表 / 3 根柱，实得 %d / %d：%v", n, len(sh.ChartBars), notes)
	}
	if sh.ChartOrientation != "vertical" {
		t.Fatalf("朝向应 vertical，实得 %q", sh.ChartOrientation)
	}
	for i, b := range sh.ChartBars {
		if b.ID != []string{"p1-s2-b0", "p1-s2-b1", "p1-s2-b2"}[i] {
			t.Fatalf("第 %d 根的 id 不对：%s", i, b.ID)
		}
		if b.Category != []string{"2 核", "4 核", "8 核"}[i] {
			t.Fatalf("第 %d 根的类别不对：%s", i, b.Category)
		}
		if b.Value != []float64{174, 131, 96}[i] {
			t.Fatalf("第 %d 根的数值不对：%v", i, b.Value)
		}
		if b.Box[2] <= 0 || b.Box[3] <= 0 {
			t.Fatalf("第 %d 根的框无效：%v", i, b.Box)
		}
		// 柱高应当与数值同向：174 那根最高。
		if i == 0 && b.Box[3] <= sh.ChartBars[2].Box[3] {
			t.Fatalf("174 那根应比 96 那根高：%d vs %d", b.Box[3], sh.ChartBars[2].Box[3])
		}
		// 标签块画在柱顶上方 20px、高 14px（见 chartPagePNG）→ 20+3 = 23。
		// 断言具体值而不是 >0：只判 >0 时，「量到最近一排墨」（10）也会过。
		if b.LabelDir != "up" || b.LabelPad != 23 {
			t.Fatalf("第 %d 根应带向上 23px 的标签带，实得 %q/%d", i, b.LabelDir, b.LabelPad)
		}
	}
}

// 页高不是标准 7.5in 时 px/EMU 会算偏，那时**切不出来**（报未切），而不是切出错的框。
func TestAnnotateChartBarsWrongScaleFailsClosed(t *testing.T) {
	// 把页图高度砍一半：映射后图表区域只覆盖下半页，柱子被裁掉一部分。
	img := chartPagePNG(t, 1920, 1080)
	sh := chartShape(&project.Box{X: 0, Y: 0, Width: 12192000, Height: 6858000})
	// 形状只占下半页的 1/4：换算后落在柱子下半截，切不出三根完整的柱。
	sh.Bounds = &project.Box{X: 0, Y: 5143500, Width: 12192000, Height: 1714500}
	pg := &project.Page{Index: 0, SlideID: "slide-0", Shapes: []*project.Shape{sh}}

	n, notes := AnnotateChartBars([]*project.Page{pg}, [][]byte{img})
	if n != 0 || len(sh.ChartBars) != 0 {
		t.Fatalf("切不全时应留空，实得 %d 根", len(sh.ChartBars))
	}
	if len(notes) == 0 || notes[0] == "" {
		t.Fatalf("应留下未切原因，实得 %v", notes)
	}
}

// 图表形状不在页角时，柱子坐标必须**加回图表的原点**。漏了这一步，所有框会整体
// 平移一个「图表在页面上的位置」——框仍然压在柱子上（因为柱子也在图里），
// 只是指偏了；真实渲染上实测偏了 144/173px，靠肉眼几乎发现不了。
func TestAnnotateChartBarsKeepsPageOrigin(t *testing.T) {
	img := chartPagePNG(t, 1920, 1080)
	// 形状框从页内 (120, 60) 起，覆盖到柱子外沿：切出来的是块内坐标。
	sh := chartShape(&project.Box{
		X: 120 * 6350, Y: 60 * 6350,
		Width: 1700 * 6350, Height: 900 * 6350,
	})
	pg := &project.Page{Index: 0, SlideID: "slide-0", Shapes: []*project.Shape{sh}}

	if n, notes := AnnotateChartBars([]*project.Page{pg}, [][]byte{img}); n != 1 {
		t.Fatalf("应切出一张图表：%v", notes)
	}
	// chartPagePNG 的第一根柱画在 x = 1920/8 = 240，柱底在 y = 1080*3/4 = 810。
	first := sh.ChartBars[0]
	if first.Box[0] != 240 {
		t.Fatalf("柱 x 应回到页坐标 240（不是块内 120），实得 %d", first.Box[0])
	}
	if bottom := first.Box[1] + first.Box[3]; bottom != 810 {
		t.Fatalf("柱底应回到页坐标 810，实得 %d", bottom)
	}
}

// 饼图不切：画面上不是「一根柱占一个带」，切出来的段没有语义。
func TestAnnotateChartBarsRejectsNonBar(t *testing.T) {
	img := chartPagePNG(t, 1920, 1080)
	pie := chartShape(&project.Box{X: 0, Y: 0, Width: 12192000, Height: 6858000})
	pie.ID = "p1-s3"
	pie.ChartType = "pie"
	pg := &project.Page{Index: 0, SlideID: "slide-0", Shapes: []*project.Shape{pie}}

	n, notes := AnnotateChartBars([]*project.Page{pg}, [][]byte{img})
	if n != 0 {
		t.Fatalf("饼图不该切，实得 %d", n)
	}
	if joined := strings.Join(notes, " | "); !strings.Contains(joined, "不是柱状") {
		t.Fatalf("未切原因应写清楚，实得 %s", joined)
	}
}

// 数值说「这根是负的」、像素却画在零线上方 → 不切。
//
// 这条是负值支持的安全网：符号来自数值，位置来自像素，两者对不上说明这张图
// （或这份数值）有一边不对。此时按符号去取基线会得到 180px 的偏差，正好被
// 「正负柱共用同一条零线」这条校验抓住——否则会带着错位的基线去扩标签带。
func TestAnnotateChartBarsRejectsMismatchedNegative(t *testing.T) {
	img := chartPagePNG(t, 1920, 1080)
	sh := chartShape(&project.Box{X: 0, Y: 0, Width: 12192000, Height: 6858000})
	sh.ChartSeries[0].Values = []float64{174, -131, 96}
	pg := &project.Page{Index: 0, SlideID: "slide-0", Shapes: []*project.Shape{sh}}

	n, notes := AnnotateChartBars([]*project.Page{pg}, [][]byte{img})
	if n != 0 || len(sh.ChartBars) != 0 {
		t.Fatalf("数值与像素矛盾时不该切，实得 %d 根", len(sh.ChartBars))
	}
	if joined := strings.Join(notes, " | "); !strings.Contains(joined, "零线") {
		t.Fatalf("应报正负柱基线不一致，实得 %s", joined)
	}
}

// 负值柱能切：框在零线下方，标签方向是 down（数字画在柱体下面）。
//
// 这条守的是「数值真的被用上了」：chartgeo 的符号来自 values，若调用方忘了传，
// 或传成了空矩阵，这里会退化成「按正值处理」——框仍然压在柱子上，标签却扩到
// 上方，正好把数字留在纱外面。
func TestAnnotateChartBarsNegative(t *testing.T) {
	const w, h = 1920, 1080
	baseY := h * 3 / 4
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(m, m.Bounds(), image.NewUniform(cbWhite), image.Point{}, draw.Src)
	// -40 那根从零线往下；120 / 80 往上。长度比 40:80:120 与数值同向。
	for i, b := range []struct{ up, down int }{{0, 100}, {240, 0}, {160, 0}} {
		x, bw := w/8+i*(w/5), w/8
		draw.Draw(m, image.Rect(x, baseY-b.up, x+bw, baseY+b.down),
			image.NewUniform(cbBlue), image.Point{}, draw.Src)
	}
	// 标签：负值在下方，其余在上方。
	draw.Draw(m, image.Rect(300, baseY+106, 420, baseY+120), image.NewUniform(cbBlack), image.Point{}, draw.Src)
	draw.Draw(m, image.Rect(684, baseY-260, 804, baseY-246), image.NewUniform(cbBlack), image.Point{}, draw.Src)
	draw.Draw(m, image.Rect(1068, baseY-180, 1188, baseY-166), image.NewUniform(cbBlack), image.Point{}, draw.Src)

	var buf bytes.Buffer
	if err := png.Encode(&buf, m); err != nil {
		t.Fatal(err)
	}
	sh := chartShape(&project.Box{X: 0, Y: 0, Width: 12192000, Height: 6858000})
	sh.ChartSeries[0].Values = []float64{-40, 120, 80}
	pg := &project.Page{Index: 0, SlideID: "slide-0", Shapes: []*project.Shape{sh}}

	n, notes := AnnotateChartBars([]*project.Page{pg}, [][]byte{buf.Bytes()})
	if n != 1 || len(sh.ChartBars) != 3 {
		t.Fatalf("负值柱应切出 3 根，实得 %d：%v", len(sh.ChartBars), notes)
	}
	b0 := sh.ChartBars[0]
	if b0.Value != -40 || b0.Box[1] < baseY {
		t.Fatalf("负值柱应在零线下方（y>=%d），实得 value=%v box=%v", baseY, b0.Value, b0.Box)
	}
	if b0.LabelDir != "down" || b0.LabelPad <= 0 {
		t.Fatalf("负值柱的标签应朝下，实得 %q/%d", b0.LabelDir, b0.LabelPad)
	}
	for i := 1; i < 3; i++ {
		if d := sh.ChartBars[i].LabelDir; d != "up" {
			t.Fatalf("第 %d 根是正值柱，标签应朝上，实得 %q", i, d)
		}
	}
}

// 页图与页数对不上时不猜：直接不补，并留一条诊断。
func TestAnnotateChartBarsLengthMismatch(t *testing.T) {
	sh := chartShape(&project.Box{X: 0, Y: 0, Width: 12192000, Height: 6858000})
	n, notes := AnnotateChartBars([]*project.Page{{Index: 0, Shapes: []*project.Shape{sh}}}, nil)
	if n != 0 || len(notes) == 0 {
		t.Fatalf("页数不一致时应拒绝，实得 %d / %v", n, notes)
	}
}
