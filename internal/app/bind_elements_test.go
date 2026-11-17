package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/project"
)

// 夹具来自绑定器的金标（同一份 elements.json），id 形如 `p4-s12`。
// Go 解析侧的形状 ID 是 `shape-<pptx shapeId>`，两者靠 shapeId 对齐。
type q8Fixture struct {
	Pages []struct {
		Index    int `json:"index"`
		Elements []struct {
			ID   string     `json:"id"`
			Kind string     `json:"kind"`
			Box  [4]float64 `json:"box"`
			Text string     `json:"text"`
		} `json:"elements"`
	} `json:"pages"`
}

func loadQ8Fixture(t *testing.T) q8Fixture {
	t.Helper()
	raw, err := os.ReadFile("../animation/bind/testdata/q8-golden.json")
	if err != nil {
		t.Fatalf("读夹具: %v", err)
	}
	var doc q8Fixture
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析夹具: %v", err)
	}
	return doc
}

// 被测页：pptx 的第 1/2/4/8/10 页（0 基 index 0/1/3/7/9）。
var q8SlideIndexes = []int{0, 1, 3, 7, 9}

// TestPageBindElementsMatchesExperiment 钉住「适配层的版面分类与实验脚本一致」。
//
// 这条测试是 tasks 2.1 的验收：绑定器核心的保真靠 bind 包的金标测试，而**输入**的保真
// 靠这一条。两者缺一不可——核心再准，若适配层把眉标当标题，线上看到的仍然是错的。
//
// 已知差异不在分类上而在**清单范围**：Go 侧平铺了组合形状子项、保留了零面积空框
// （实验脚本按面积过滤掉了），所以 Go 的元素数比夹具多。这里只比对两边都有的 ID。
func TestPageBindElementsMatchesExperiment(t *testing.T) {
	f, err := os.Open("../animation/animdefault/testdata/sample-customer-deck.pptx")
	if err != nil {
		t.Fatalf("打开样本: %v", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := project.NewGoPPTXReader(project.Limits{}).Inspect(context.Background(), f, st.Size())
	if err != nil {
		t.Fatalf("解析样本: %v", err)
	}

	fixture := loadQ8Fixture(t)
	if len(fixture.Pages) != len(q8SlideIndexes) {
		t.Fatalf("夹具页数 = %d，期望 %d", len(fixture.Pages), len(q8SlideIndexes))
	}

	kindDiff, boxDiff, compared := 0, 0, 0
	for i, slideIdx := range q8SlideIndexes {
		if slideIdx >= len(doc.Pages) {
			t.Fatalf("样本只有 %d 页，取不到第 %d 页", len(doc.Pages), slideIdx)
		}
		got := PageBindElements(doc.Pages[slideIdx], 12192000, 6858000)
		byID := make(map[string]string, len(got)) // shape-12 → kind
		boxByID := make(map[string][4]float64, len(got))
		for _, e := range got {
			byID[e.ID] = e.Kind
			boxByID[e.ID] = [4]float64{e.Box.X, e.Box.Y, e.Box.W, e.Box.H}
		}

		for _, want := range fixture.Pages[i].Elements {
			id := goShapeID(want.ID)
			kind, ok := byID[id]
			if !ok {
				if want.Kind == "table" {
					if _, sliced := byID[id+"-r1"]; sliced {
						continue
					}
				}
				t.Errorf("page %d: 夹具里的 %s 在 Go 侧不存在（id=%s）", i, want.ID, id)
				continue
			}
			compared++
			if kind != want.Kind {
				kindDiff++
				t.Errorf("page %d: %s 分类不一致 go=%s 实验=%s", i, want.ID, kind, want.Kind)
			}
			gb := boxByID[id]
			for k := 0; k < 4; k++ {
				if math.Abs(gb[k]-want.Box[k]) > 2 {
					boxDiff++
					t.Errorf("page %d: %s 包围盒偏差过大 go=%v 实验=%v", i, want.ID, gb, want.Box)
					break
				}
			}
		}
	}
	if compared == 0 {
		t.Fatal("一个元素都没比对上（夹具与样本不是同一份？）")
	}
	if kindDiff > 0 || boxDiff > 0 {
		t.Fatalf("分类差异 %d 处、包围盒差异 %d 处（比对 %d 个元素）", kindDiff, boxDiff, compared)
	}
	t.Logf("比对 %d 个元素，分类与包围盒均一致", compared)
}

func TestPageBindElementsEmitsBarsNotChart(t *testing.T) {
	page := &project.Page{SlideID: "s1", Shapes: []*project.Shape{
		{ID: "title", Kind: "text", Text: "季度对比", Bounds: &project.Box{X: 500000, Y: 200000, Width: 8000000, Height: 800000}},
		{
			ID: "chart-1", Kind: "chart", ChartType: "col",
			Bounds: &project.Box{X: 1000000, Y: 2000000, Width: 4000000, Height: 3000000},
			ChartBars: []project.ChartBar{
				{ID: "chart-1-b0", Category: "Q1", Value: 174, Box: [4]int{300, 250, 60, 250}},
				{ID: "chart-1-b1", Category: "Q2", Value: 96, Box: [4]int{420, 300, 60, 200}},
			},
		},
	}}
	got := PageBindElements(page, 12192000, 6858000)
	byID := map[string]string{}
	for _, e := range got {
		byID[e.ID] = e.Kind
	}
	if _, ok := byID["chart-1"]; ok {
		t.Fatalf("whole chart should be replaced by bars: %+v", byID)
	}
	if byID["chart-1-b0"] != "bar" || byID["chart-1-b1"] != "bar" {
		t.Fatalf("bars missing: %+v", byID)
	}
	for _, e := range got {
		if e.ID == "chart-1-b0" {
			if e.Text != "Q1" {
				t.Fatalf("bar text = %q, want Q1", e.Text)
			}
			if e.Box.X != 300 || e.Box.W != 60 {
				t.Fatalf("bar box = %+v, want canvas px 300/60", e.Box)
			}
		}
	}
}

func TestPageBindElementsEmitsRowsNotTable(t *testing.T) {
	page := &project.Page{SlideID: "s1", Shapes: []*project.Shape{
		{ID: "title", Kind: "text", Text: "演进节点", Bounds: &project.Box{X: 500000, Y: 200000, Width: 8000000, Height: 800000}},
		{
			ID: "shape-7", Kind: "table", TableRows: 6, TableCols: 3,
			Bounds: &project.Box{X: 457200, Y: 1280160, Width: 8229600, Height: 914400},
			TableCells: [][]string{
				{"时间", "节点", "要点"},
				{"2023-04", "Qwen", "开源"},
				{"2023-08", "Qwen-72B", "长文"},
				{"2024-02", "Qwen1.5", "MoE"},
				{"2024-06", "Qwen2", "多模态"},
				{"2024-09", "Qwen2.5", "代码"},
			},
		},
	}}
	got := PageBindElements(page, 12192000, 6858000)
	byID := map[string]string{}
	for _, e := range got {
		byID[e.ID] = e.Kind
	}
	if _, ok := byID["shape-7"]; ok {
		t.Fatalf("whole table should be replaced by rows: %+v", byID)
	}
	if _, ok := byID["shape-7-r0"]; ok {
		t.Fatalf("header row must not bind: %+v", byID)
	}
	if byID["shape-7-r1"] != "row" || byID["shape-7-r5"] != "row" {
		t.Fatalf("rows missing: %+v", byID)
	}
	for _, e := range got {
		if e.ID == "shape-7-r1" {
			if e.Text != "2023-04 Qwen 开源" {
				t.Fatalf("row text = %q", e.Text)
			}
			wantY := pxOf(1280160+914400/6, 6858000, bindCanvasH)
			if math.Abs(e.Box.Y-wantY) > 0.01 {
				t.Fatalf("row y = %v, want %v", e.Box.Y, wantY)
			}
		}
	}
}

// goShapeID 把夹具 id（`p4-s12`，页号从 1 起）映射成 Go 解析侧的 `shape-12`。
func goShapeID(fixtureID string) string {
	i := strings.LastIndex(fixtureID, "-s")
	if i < 0 {
		return fixtureID
	}
	n, err := strconv.Atoi(fixtureID[i+2:])
	if err != nil {
		return fixtureID
	}
	return fmt.Sprintf("shape-%d", n)
}
