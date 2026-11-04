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
		if b.LabelDir != "up" || b.LabelPad <= 0 {
			t.Fatalf("第 %d 根应带向上的标签带，实得 %q/%d", i, b.LabelDir, b.LabelPad)
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

// 负值与饼图都不切：一个会翻到基线另一侧，一个画面上不是「一根柱占一个带」。
func TestAnnotateChartBarsRefusesNegativeAndNonBar(t *testing.T) {
	img := chartPagePNG(t, 1920, 1080)
	neg := chartShape(&project.Box{X: 0, Y: 0, Width: 12192000, Height: 6858000})
	neg.ChartSeries[0].Values = []float64{174, -131, 96}
	pie := chartShape(&project.Box{X: 0, Y: 0, Width: 12192000, Height: 6858000})
	pie.ID = "p1-s3"
	pie.ChartType = "pie"
	pg := &project.Page{Index: 0, SlideID: "slide-0", Shapes: []*project.Shape{neg, pie}}

	n, notes := AnnotateChartBars([]*project.Page{pg}, [][]byte{img})
	if n != 0 {
		t.Fatalf("两个都不该切，实得 %d", n)
	}
	joined := strings.Join(notes, " | ")
	if !strings.Contains(joined, "负值") || !strings.Contains(joined, "不是柱状") {
		t.Fatalf("未切原因应写清楚，实得 %s", joined)
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
