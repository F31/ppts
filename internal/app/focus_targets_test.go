package app

import (
	"strings"
	"testing"

	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/render"
)

// 焦点候选清单是编辑器唯一的"能指什么"来源。这里守的不是渲染，
// 而是三件容易悄悄坏掉的事：
//   - 标签必须来自解析期的真实字段（类别/系列/数值、形状文字），而不是内部 id；
//   - 不可选的东西不能混进列表（没切开的图表、装饰形状），但必须**说明**为什么不列；
//   - 0 与"没有值"要分得开——图表里 0 是一个真实的数。

func TestFocusTargetsBarsThenTextThenPicture(t *testing.T) {
	page := &project.Page{SlideID: "slide-1", Shapes: []*project.Shape{
		{
			ID: "shape-2", Kind: "chart", ChartType: "bar", ChartOrientation: "vertical",
			ChartBars: []project.ChartBar{
				{ID: "p1-s1-b0", Category: "Q1", Series: "系列 A", Value: 174, Box: [4]int{100, 200, 60, 370}},
				{ID: "p1-s1-b1", Category: "Q2", Series: "系列 A", Value: 131, Box: [4]int{200, 200, 60, 280}},
			},
		},
		{ID: "shape-3", Kind: "text", Text: "一季度回顾", Bounds: &project.Box{X: 10, Y: 20, Width: 300, Height: 60}},
		{ID: "shape-4", Kind: "picture", Name: "架构图", Bounds: &project.Box{X: 5, Y: 6, Width: 7, Height: 8}},
		{ID: "shape-5", Kind: "line"}, // 无文字、无包围盒：装饰，不该出现
	}}

	got := FocusTargetsForPage(page)
	if len(got.Targets) != 4 {
		t.Fatalf("want 4 targets, got %d: %+v", len(got.Targets), got.Targets)
	}
	if got.Targets[0].BarID != "p1-s1-b0" || got.Targets[0].Kind != "bar" {
		t.Fatalf("柱子应排在形状声明位置，got %+v", got.Targets[0])
	}
	if got.Targets[0].Label != "Q1 · 系列 A = 174" {
		t.Fatalf("柱子标签 = %q", got.Targets[0].Label)
	}
	if !got.Targets[0].HasValue || got.Targets[0].Value != 174 {
		t.Fatalf("值应透传，got %+v", got.Targets[0])
	}
	if got.Targets[2].Label != "一季度回顾" || got.Targets[2].Kind != "text" {
		t.Fatalf("文字形状 = %+v", got.Targets[2])
	}
	if got.Targets[3].Kind != "picture" || got.Targets[3].Label != "架构图" {
		t.Fatalf("图片 = %+v", got.Targets[3])
	}
	if got.Orientation != "vertical" {
		t.Fatalf("朝向 = %q", got.Orientation)
	}
}

// 柱子的候选框要与成片里那块留孔的矩形一致：柱体 + 这根自己的数字。
// 这里若还给柱体，编辑器将来做悬停高亮时会出现「选的时候框住一块、亮的是另一块」。
func TestFocusTargetsBarBoxIncludesLabelBand(t *testing.T) {
	page := &project.Page{SlideID: "slide-1", Shapes: []*project.Shape{{
		ID: "shape-2", Kind: "chart", ChartOrientation: "vertical",
		ChartBars: []project.ChartBar{
			{ID: "b0", Category: "Q1", Value: 174, Box: [4]int{100, 200, 60, 370},
				LabelDir: "up", LabelPad: 42},
			{ID: "b1", Category: "Q2", Value: 131, Box: [4]int{200, 200, 60, 280}},
		},
	}}}
	got := FocusTargetsForPage(page)
	if len(got.Targets) != 2 {
		t.Fatalf("want 2 targets, got %+v", got.Targets)
	}
	if want := (render.EMUBox{
		X: pxToEMU(100), Y: pxToEMU(158), Width: pxToEMU(60), Height: pxToEMU(412),
	}); got.Targets[0].Box != want {
		t.Fatalf("带标签的柱子候选框 = %+v, want %+v", got.Targets[0].Box, want)
	}
	if want := (render.EMUBox{
		X: pxToEMU(200), Y: pxToEMU(200), Width: pxToEMU(60), Height: pxToEMU(280),
	}); got.Targets[1].Box != want {
		t.Fatalf("没标签的柱子候选框 = %+v, want %+v（不该被扩）", got.Targets[1].Box, want)
	}
}

// 图表存在但没切开时必须显式说明。静默给一份空清单，用户会以为"这张图没有柱子"。
func TestFocusTargetsUnslicedChartSaysWhy(t *testing.T) {
	page := &project.Page{SlideID: "slide-1", Shapes: []*project.Shape{
		{ID: "shape-2", Kind: "chart", ChartType: "line", Name: "趋势"},
	}}
	got := FocusTargetsForPage(page)
	if len(got.Targets) != 0 {
		t.Fatalf("没切开就不该给目标，got %+v", got.Targets)
	}
	if !strings.Contains(got.Note, "未逐柱切开") || !strings.Contains(got.Note, "趋势") {
		t.Fatalf("note 应说明是哪张图，got %q", got.Note)
	}
}

func TestFocusTargetsTextFirstLineAndTruncation(t *testing.T) {
	long := "第一行标题\n第二行不该出现"
	page := &project.Page{SlideID: "slide-1", Shapes: []*project.Shape{
		{ID: "s1", Kind: "text", Text: long, Bounds: &project.Box{Width: 10, Height: 10}},
		{ID: "s2", Kind: "text", Text: strings.Repeat("字", 30), Bounds: &project.Box{Width: 10, Height: 10}},
	}}
	got := FocusTargetsForPage(page)
	if got.Targets[0].Label != "第一行标题" {
		t.Fatalf("应只取首行，got %q", got.Targets[0].Label)
	}
	if runes := []rune(got.Targets[1].Label); len(runes) != 25 || !strings.HasSuffix(got.Targets[1].Label, "…") {
		t.Fatalf("应截到 24 字加省略号，got %q (%d runes)", got.Targets[1].Label, len(runes))
	}
}

// 0 是真实数值：HasValue 为真且标签写成 "= 0"，不能因为零值被当成"没有值"。
func TestFocusTargetsZeroValueIsAValue(t *testing.T) {
	page := &project.Page{SlideID: "slide-1", Shapes: []*project.Shape{
		{ID: "shape-1", Kind: "chart", ChartType: "bar",
			ChartBars: []project.ChartBar{{ID: "b0", Category: "Q3", Value: 0, Box: [4]int{1, 2, 3, 4}}}},
	}}
	got := FocusTargetsForPage(page)
	if !got.Targets[0].HasValue {
		t.Fatal("值为 0 时 HasValue 必须为真")
	}
	if got.Targets[0].Label != "Q3 = 0" {
		t.Fatalf("label = %q", got.Targets[0].Label)
	}
}

// 类别与系列都没有时给位置描述，不返回空标签——空白选项在列表里等于不可选。
func TestFocusTargetsBarWithoutCategoryGetsPosition(t *testing.T) {
	page := &project.Page{SlideID: "slide-1", Shapes: []*project.Shape{
		{ID: "shape-1", Kind: "chart", ChartType: "bar",
			ChartBars: []project.ChartBar{
				{ID: "b0", Value: 42, Box: [4]int{1, 2, 3, 4}},
				{ID: "b1", Value: 1.5, Box: [4]int{5, 6, 7, 8}},
			}},
	}}
	got := FocusTargetsForPage(page)
	if got.Targets[0].Label != "第 1 根 = 42" {
		t.Fatalf("label = %q", got.Targets[0].Label)
	}
	if got.Targets[1].Label != "第 2 根 = 1.5" {
		t.Fatalf("小数不该被截成整数，label = %q", got.Targets[1].Label)
	}
}

// 子形状（组合/容器里）也必须能选：标题与图表常被放进同一个组合。
func TestFocusTargetsWalkChildren(t *testing.T) {
	page := &project.Page{SlideID: "slide-1", Shapes: []*project.Shape{
		{ID: "group-1", Kind: "group", Children: []*project.Shape{
			{ID: "child-1", Kind: "text", Text: "组里的标题", Bounds: &project.Box{Width: 10, Height: 10}},
		}},
	}}
	got := FocusTargetsForPage(page)
	if len(got.Targets) != 1 || got.Targets[0].ShapeID != "child-1" {
		t.Fatalf("应遍历子形状，got %+v", got.Targets)
	}
}

// 两个图表朝向不同时不给统一朝向：返回一个"随便挑的"会让前端画错方向。
func TestFocusTargetsMixedOrientationIsEmpty(t *testing.T) {
	page := &project.Page{SlideID: "slide-1", Shapes: []*project.Shape{
		{ID: "c1", Kind: "chart", ChartType: "bar", ChartOrientation: "vertical",
			ChartBars: []project.ChartBar{{ID: "b0", Value: 1, Box: [4]int{1, 2, 3, 4}}}},
		{ID: "c2", Kind: "chart", ChartType: "bar", ChartOrientation: "horizontal",
			ChartBars: []project.ChartBar{{ID: "b1", Value: 2, Box: [4]int{1, 2, 3, 4}}}},
	}}
	if got := FocusTargetsForPage(page); got.Orientation != "" {
		t.Fatalf("混合朝向应为空，got %q", got.Orientation)
	}
}
