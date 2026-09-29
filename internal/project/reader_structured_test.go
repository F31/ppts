package project

import (
	"bytes"
	"context"
	"strings"
	"testing"

	pptx "github.com/F31/go-pptx/v2/pptx"
)

// 本文件验证表格/图表在解析产物里的**结构化**落库（TableCells / ChartSeries）。
// 表格与图表都直接用 go-pptx 写侧生成（AddTable / AddChart），再经 reader.Inspect
// 读回校验——保证「生成 → 解析」往返一致，且 ppts 侧只消费读侧产物。

// buildStructuredDeck 生成一页同时含表格与柱状图的 deck。
func buildStructuredDeck(t *testing.T) *bytes.Reader {
	t.Helper()
	p, err := pptx.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()
	layouts, err := p.Layouts()
	if err != nil || len(layouts) == 0 {
		t.Fatalf("Layouts: %v (n=%d)", err, len(layouts))
	}
	slide, err := p.AddSlide(layouts[0])
	if err != nil {
		t.Fatalf("AddSlide: %v", err)
	}
	if _, err := slide.AddTable(context.Background(), pptx.TableSpec{
		X: 914400, Y: 914400, Width: 4000000, Height: 2000000,
		Name: "对比表",
		Rows: [][]string{
			{"型号", "容量"},
			{"A100", "80GB"},
			{"H800", "128GB"},
		},
	}); err != nil {
		t.Fatalf("AddTable: %v", err)
	}
	if _, err := slide.AddChart(context.Background(), pptx.ChartSpec{
		Type:       pptx.ChartBar,
		Title:      "季度销量",
		Categories: []string{"Q1", "Q2", "Q3"},
		Series: []pptx.ChartSeries{
			{Name: "华东", Values: []float64{12.5, 20, 1676.0}},
			{Name: "华北", Values: []float64{30, 25, 8}},
		},
		X: 914400, Y: 3000000, Width: 6000000, Height: 4000000,
	}); err != nil {
		t.Fatalf("AddChart: %v", err)
	}
	var buf bytes.Buffer
	if _, err := p.Write(context.Background(), &buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestGoPPTXReaderExtractsTableCells(t *testing.T) {
	src := buildStructuredDeck(t)
	r := NewGoPPTXReader(Limits{})
	doc, err := r.Inspect(context.Background(), src, int64(src.Len()))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	var found *Shape
	for _, sh := range doc.Pages[0].Shapes {
		if strings.EqualFold(strings.TrimSpace(sh.Kind), "table") {
			found = sh
			break
		}
	}
	if found == nil {
		t.Fatalf("no table shape parsed; shapes=%+v", doc.Pages[0].Shapes)
	}
	if found.TableRows != 3 || found.TableCols != 2 {
		t.Fatalf("table dims = %d x %d, want 3 x 2", found.TableRows, found.TableCols)
	}
	if len(found.TableCells) != 3 || len(found.TableCells[0]) != 2 {
		t.Fatalf("tableCells shape = %dx%d, want 3x2", len(found.TableCells), len(found.TableCells[0]))
	}
	if found.TableCells[0][0] != "型号" || found.TableCells[2][1] != "128GB" {
		t.Fatalf("tableCells content = %+v", found.TableCells)
	}
	if !strings.Contains(found.Text, "型号") || !strings.Contains(found.Text, "128GB") {
		t.Fatalf("table text projection missing content: %q", found.Text)
	}
}

func TestGoPPTXReaderExtractsChartSeries(t *testing.T) {
	src := buildStructuredDeck(t)
	r := NewGoPPTXReader(Limits{})
	doc, err := r.Inspect(context.Background(), src, int64(src.Len()))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	var found *Shape
	for _, sh := range doc.Pages[0].Shapes {
		if strings.EqualFold(strings.TrimSpace(sh.Kind), "chart") {
			found = sh
			break
		}
	}
	if found == nil {
		t.Fatalf("no chart shape parsed; shapes=%+v", doc.Pages[0].Shapes)
	}
	if found.ChartType != "bar" {
		t.Fatalf("chartType = %q, want bar", found.ChartType)
	}
	if len(found.ChartCategories) != 3 || found.ChartCategories[1] != "Q2" {
		t.Fatalf("chartCategories = %v", found.ChartCategories)
	}
	if len(found.ChartSeries) != 2 {
		t.Fatalf("chartSeries count = %d, want 2", len(found.ChartSeries))
	}
	if found.ChartSeries[0].Name != "华东" || len(found.ChartSeries[0].Values) != 3 {
		t.Fatalf("chartSeries[0] = %+v", found.ChartSeries[0])
	}
}
