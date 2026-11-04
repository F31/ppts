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
// 三种形态各跑一遍：垂直单系列、水平多系列（组内系列倒序）、垂直含负值（基线
// 换到柱体另一端）。后两种是 2026-11-04 才做的，正是因为两边都改过，才更要对齐。
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

	cases := []struct {
		name       string
		chartType  string
		cats       []string
		series     []map[string]any
		horizontal bool
		draw       func(*image.RGBA)
	}{
		{
			// 3 根柱 + 标签带，尺寸与保真链实测的 1280×720 一致。
			name: "vertical", chartType: "column_clustered",
			cats:   []string{"2 核", "4 核", "8 核"},
			series: []map[string]any{{"name": "耗时", "values": []int{174, 131, 96}}},
			draw: func(m *image.RGBA) {
				for i, h := range []int{246, 185, 135} {
					x := 256 + i*289
					fill(m, x, baseY-h, x+116, baseY, blue)
					label(m, x+30, baseY-h-20)
				}
			},
		},
		{
			// 水平 3×2：组内 y 升序对应系列倒序（最上面那根是最后声明的系列）。
			name: "horizontal-multi", chartType: "bar_clustered", horizontal: true,
			cats: []string{"华东", "华北", "华南"},
			series: []map[string]any{
				{"name": "耗时", "values": []int{174, 131, 96}},
				{"name": "预算", "values": []int{60, 200, 140}},
			},
			draw: func(m *image.RGBA) {
				// y 从上到下是类别 2、1、0；每组的 l1 在上、l0 在下。
				for _, g := range []struct{ y, l0, l1 int }{
					{300, 135, 200}, {420, 185, 250}, {540, 246, 90},
				} {
					fill(m, 236, g.y, 236+g.l1, g.y+41, red)
					fill(m, 236, g.y+45, 236+g.l0, g.y+86, blue)
					label(m, 240+g.l1, g.y+12)
					label(m, 240+g.l0, g.y+57)
				}
			},
		},
		{
			// 垂直含负值：零线在 baseY，-40 那根往下长，标签在柱体下方。
			name: "vertical-negative", chartType: "column_clustered",
			cats:   []string{"华东", "华北", "华南"},
			series: []map[string]any{{"name": "耗时", "values": []int{-40, 120, 80}}},
			draw: func(m *image.RGBA) {
				fill(m, 256, baseY, 372, baseY+82, blue)
				label(m, 286, baseY+88) // 负值柱的标签在下方
				fill(m, 545, baseY-246, 661, baseY, blue)
				label(m, 575, baseY-266)
				fill(m, 834, baseY-164, 950, baseY, blue)
				label(m, 864, baseY-184)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newImg()
			tc.draw(m)

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
						"chartType": tc.chartType, "dataLabels": true,
						"categories": tc.cats, "series": tc.series,
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

			data, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatal(err)
			}
			var pages []struct {
				Elements []struct {
					Chart struct {
						Bars []struct {
							Category string `json:"category"`
							Series   string `json:"series"`
							Box      []int  `json:"box"`
							LabelPad *struct {
								Dir string `json:"dir"`
								Px  int    `json:"px"`
							} `json:"labelPad"`
						} `json:"bars"`
					} `json:"chart"`
				} `json:"elements"`
			}
			if err := json.Unmarshal(data, &pages); err != nil {
				t.Fatal(err)
			}
			want := pages[0].Elements[0].Chart.Bars
			if len(want) != len(tc.cats)*len(tc.series) {
				t.Fatalf("python 侧应切出 %d 根，实得 %d：%s",
					len(tc.cats)*len(tc.series), len(want), data)
			}

			values := make([][]float64, len(tc.series))
			for si, s := range tc.series {
				for _, v := range s["values"].([]int) {
					values[si] = append(values[si], float64(v))
				}
			}
			bars, why := Split(m, Rect{0, 0, imgW, imgH}, len(tc.cats), len(tc.series),
				tc.horizontal, values)
			if len(bars) != len(want) {
				t.Fatalf("Go 侧应切出 %d 根，实得 %d：%s", len(want), len(bars), why)
			}
			// 计划坐标 = 图坐标 × 1920/1280（保真链把框换算回计划系）。
			k := 1920.0 / imgW
			for i, b := range bars {
				got := []int{
					int(round(float64(b.Rect.X) * k)), int(round(float64(b.Rect.Y) * k)),
					int(round(float64(b.Rect.W) * k)), int(round(float64(b.Rect.H) * k)),
				}
				for j := range got {
					if d := got[j] - want[i].Box[j]; d < -1 || d > 1 {
						t.Fatalf("第 %d 根第 %d 个分量：Go %d / Python %d（差 %d）——两份实现已漂",
							i, j, got[j], want[i].Box[j], d)
					}
				}
				if b.Cat != i/len(tc.series) || b.Ser != i%len(tc.series) {
					t.Fatalf("第 %d 根的类别/系列：Go %d/%d，应 %d/%d",
						i, b.Cat, b.Ser, i/len(tc.series), i%len(tc.series))
				}
				if want[i].LabelPad != nil && want[i].LabelPad.Dir != b.LabelDir {
					t.Fatalf("第 %d 根的标签方向：Go %q / Python %q",
						i, b.LabelDir, want[i].LabelPad.Dir)
				}
				// 距离也要对：只比方向时，两边「量到最近的墨」和「量到最远的墨」都会过，
				// 而框会不会把数字包进去，差的就是这十几像素（up/left 曾经各差一半）。
				// Python 的 px 已经除过 px/单位（换算回 1920 计划系），这里乘回去。
				if want[i].LabelPad != nil {
					got := float64(b.LabelPad) * k
					if d := got - float64(want[i].LabelPad.Px); d < -2 || d > 2 {
						t.Fatalf("第 %d 根的标签距离：Go %.1f / Python %d（差 %.1f）——两份实现已漂",
							i, got, want[i].LabelPad.Px, d)
					}
				}
			}
		})
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
