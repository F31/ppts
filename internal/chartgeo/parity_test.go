package chartgeo

import (
	"encoding/json"
	"image"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// 产品侧（Go）与保真链（Python）是同一套算法的两份实现。它们会漂——阈值改一边
// 没改另一边，量出来的框就会差几像素，而**差几像素的框看起来是对的**（还在柱子上），
// 只有「指谁谁灰」的观感会变形。所以用同一张合成页图跑两边，要求逐根对上。
//
// 跑不了 python / numpy / PIL 时 skip：这不是「通过」，但也不该让没装依赖的机器
// 红——CI 的 Go 作业不装 numpy，fidelity 作业只跑 python 侧的自检。
func TestParityWithPythonSplitter(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		py, err = exec.LookPath("python")
	}
	if err != nil {
		t.Skip("没有 python，跳过与保真链的对齐检查")
	}
	root := repoRoot(t)
	script := filepath.Join(root, "scripts", "fidelity", "split_chart_bars.py")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("找不到 %s：%v", script, err)
	}

	// 同一张图：3 根柱 + 标签带，尺寸与保真链实测的 1280×720 一致。
	m := newImg()
	heights := []int{246, 185, 135}
	for i, h := range heights {
		x := 256 + i*289
		fill(m, x, baseY-h, x+116, baseY, blue)
		label(m, x+30, baseY-h-20)
	}

	dir := t.TempDir()
	if err := writePNG(filepath.Join(dir, "page-1.png"), m); err != nil {
		t.Fatal(err)
	}
	// 元素清单按 1920×1080 计划坐标给（保真链的坐标系），整页一个图表框。
	elems := []map[string]any{{
		"index": 0, "slideId": "slide-0",
		"elements": []map[string]any{{
			"id": "p1-s2", "kind": "chart", "box": []int{0, 0, 1920, 1080},
			"chart": map[string]any{
				"chartType": "column_clustered", "dataLabels": true,
				"categories": []string{"2 核", "4 核", "8 核"},
				"series":     []map[string]any{{"name": "耗时", "values": []int{174, 131, 96}}},
			},
		}},
	}}
	inPath := filepath.Join(dir, "elements.json")
	raw, _ := json.Marshal(elems)
	if err := os.WriteFile(inPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "bars.json")
	cmd := exec.Command(py, script, "--elements", inPath, "--png-dir", dir,
		"--pattern", "page-{i}.png", "--out", outPath, "--quiet")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("python 侧跑不起来（多半没装 numpy/PIL）：%v\n%s", err, out)
	}

	var pages []struct {
		Elements []struct {
			Chart struct {
				Bars []struct {
					Box []int `json:"box"`
				} `json:"bars"`
			} `json:"chart"`
		} `json:"elements"`
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &pages); err != nil {
		t.Fatal(err)
	}
	if len(pages) == 0 || len(pages[0].Elements) == 0 || len(pages[0].Elements[0].Chart.Bars) != 3 {
		t.Fatalf("python 侧应切出 3 根，实得 %s", data)
	}

	bars, why := Split(m, Rect{0, 0, imgW, imgH}, 3, 1, false)
	if len(bars) != 3 {
		t.Fatalf("Go 侧应切出 3 根，实得 %d：%s", len(bars), why)
	}
	// 计划坐标 = 图坐标 × 1920/1280（保真链把框换算回计划系）。
	k := 1920.0 / imgW
	for i, b := range bars {
		want := pages[0].Elements[0].Chart.Bars[i].Box
		got := []int{
			int(round(float64(b.Rect.X) * k)), int(round(float64(b.Rect.Y) * k)),
			int(round(float64(b.Rect.W) * k)), int(round(float64(b.Rect.H) * k)),
		}
		for j := range got {
			if d := got[j] - want[j]; d < -1 || d > 1 {
				t.Fatalf("第 %d 根第 %d 个分量：Go %d / Python %d（差 %d）——两份实现已漂",
					i, j, got[j], want[j], d)
			}
		}
	}
}

func writePNG(path string, m *image.RGBA) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, m)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("拿不到当前文件路径")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func round(f float64) float64 { return math.Round(f) }
