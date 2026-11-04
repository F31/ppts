// stagechart 验一件事：内嵌图表切成逐柱之后，**渲染端的分层能不能直接吃**。
//
// 背景：`split_chart_bars.py` 从页面图量出了每根柱子的 box。直觉上下一步很简单——
// 把图表当父层、每根柱当子层交给 staging，柱子就能逐个淡入，focus 也能指单柱。
// 这个程序把那条路真跑一遍，看画面对不对。它 import 的是产品同一份
// `internal/render`（BuildPlate / BuildLayerAssets），不是复刻，所以结论不会漂。
//
// 它回答三个问题，都是像素级的：
//
//  1. 层拼回去能不能还原原图？（composed vs 原图 的逐像素差）
//  2. 柱子还没出场时，画面是什么样？（before-bars.png）
//  3. 父层抹掉子元素之后，被抹的位置留下什么？（patches 的 color / spread / noisy）
//
// 用法（输入来自 split_chart_bars.py 的产物）：
//
//	go run ./scripts/fidelity/stagechart \
//	    --png tmp/chart/hi-1.png --elements tmp/chart/elements-hi.json --out tmp/chart/stage
//
// 退出码：0 拼得回去；1 拼不回去（差值见输出）；2 缺输入或这页没有 bars。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"

	"github.com/F31/ppts/internal/render"
)

type elem struct {
	ID    string `json:"id"`
	Box   [4]int `json:"box"`
	Chart *struct {
		Bars []struct {
			ID    string  `json:"id"`
			Value float64 `json:"value"`
			Box   [4]int  `json:"box"`
		} `json:"bars"`
	} `json:"chart"`
}

type page struct {
	Index    int    `json:"index"`
	Elements []elem `json:"elements"`
}

func main() {
	pngPath := flag.String("png", "", "页面图（与 elements 同一坐标系，任意尺寸）")
	elemPath := flag.String("elements", "", "split_chart_bars.py 的输出")
	outDir := flag.String("out", "tmp/chart/stage", "输出目录")
	// 柱层向上多包几像素。默认 0：柱层只含柱体，柱顶的数字标签留在父层里。
	// 给 30 就能把标签一起切进柱层——代价是标签那一带的横向网格线也跟着柱层走。
	labelPad := flag.Int("label-pad", 0, "柱层向上扩展的像素（用于把数据标签包进柱层）")
	flag.Parse()
	if *pngPath == "" || *elemPath == "" {
		fmt.Println("缺输入：--png 与 --elements 都要给")
		os.Exit(2)
	}

	base, err := readPNG(*pngPath)
	if err != nil {
		fmt.Println("读页面图：", err)
		os.Exit(2)
	}
	var pages []page
	raw, err := os.ReadFile(*elemPath)
	if err != nil {
		fmt.Println("读元素清单：", err)
		os.Exit(2)
	}
	if err := json.Unmarshal(raw, &pages); err != nil {
		fmt.Println("解析元素清单：", err)
		os.Exit(2)
	}

	var chart *elem
	for i := range pages {
		for j := range pages[i].Elements {
			e := &pages[i].Elements[j]
			if e.Chart != nil && len(e.Chart.Bars) >= 2 {
				chart = e
			}
		}
	}
	if chart == nil {
		fmt.Println("这份清单里没有切出逐柱的图表（先跑 split_chart_bars.py）")
		os.Exit(2)
	}

	// 父层在下、子层在上。这与 render.stagePages 里 orderLayersParentsFirst 的
	// 结果一致：父层的 box 包住子层，顺序错了柱子会被父层盖掉。
	layers := []render.Layer{{ElementID: chart.ID, Kind: "element",
		Box: render.Box{X: chart.Box[0], Y: chart.Box[1], W: chart.Box[2], H: chart.Box[3]}}}
	for _, b := range chart.Chart.Bars {
		box := render.Box{X: b.Box[0], Y: b.Box[1], W: b.Box[2], H: b.Box[3]}
		if *labelPad > 0 && box.Y >= *labelPad {
			box.Y -= *labelPad
			box.H += *labelPad
		}
		layers = append(layers, render.Layer{ElementID: b.ID, Kind: "element", Box: box})
	}

	canvasW, canvasH := 1920, 1080
	plate, patches := render.BuildPlate(base, layers, canvasW, canvasH)
	assets := render.BuildLayerAssets(base, layers, canvasW, canvasH)
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Println("建目录：", err)
		os.Exit(2)
	}
	writePNG(filepath.Join(*outDir, "plate.png"), plate)
	for _, a := range assets {
		writePNG(filepath.Join(*outDir, "layer-"+a.ElementID+".png"), a.Image)
	}

	fmt.Printf("图表 %s：父层 %v + %d 根柱\n", chart.ID, chart.Box, len(chart.Chart.Bars))
	for _, p := range patches {
		fmt.Printf("  补洞 %-12s %v 色 %v spread=%d noisy=%v\n",
			p.ElementID, p.Box, p.Color, p.Spread, p.Noisy)
	}
	for _, a := range assets {
		fmt.Printf("  层图 %-12s %dx%d erased=%v\n", a.ElementID,
			a.Image.Bounds().Dx(), a.Image.Bounds().Dy(), a.Erased)
	}

	// 全部层按原位叠回底板 = 渲染末帧。差 0 才说明「切开再拼回来」是无损的。
	composed := image.NewNRGBA(plate.Bounds())
	blit(composed, plate, image.Point{})
	for _, a := range assets {
		for _, l := range layers {
			if l.ElementID == a.ElementID {
				blit(composed, a.Image, image.Pt(l.Box.X, l.Box.Y))
			}
		}
	}
	writePNG(filepath.Join(*outDir, "composed.png"), composed)

	// 柱还没出场：只有底板 + 图表父层。这一张用来判断「数字标签会不会提前出现」。
	before := image.NewNRGBA(plate.Bounds())
	blit(before, plate, image.Point{})
	for _, a := range assets {
		if a.ElementID == chart.ID {
			blit(before, a.Image, image.Pt(chart.Box[0], chart.Box[1]))
		}
	}
	writePNG(filepath.Join(*outDir, "before-bars.png"), before)

	_, maxDiff, n := compare(composed, base)
	total := composed.Bounds().Dx() * composed.Bounds().Dy()
	fmt.Printf("\n拼回原图：%d/%d 像素有差（%.4f%%），最大通道和差 %d → %s\n",
		n, total, 100*float64(n)/float64(total), maxDiff,
		map[bool]string{true: "无损", false: "有差，分层方案不成立"}[n == 0])

	// 中间帧诊断：柱子按住不出现时，标签是不是已经在了。
	// 标签在柱 box **之外**（柱顶上方 30px），所以它留在父层里、跟着父层一起出现。
	fmt.Println("\n出场前诊断（底板 + 图表父层，柱子全部按住）：")
	for _, b := range chart.Chart.Bars {
		box := render.Box{X: b.Box[0], Y: b.Box[1], W: b.Box[2], H: b.Box[3]}
		// 柱体区里还剩多少非白像素：为 0 说明柱子确实按住没出。
		inside := nonWhite(before, box)
		// 柱顶 30px 带与原图的差异：0 = 这一带已经和末帧一模一样，也就是标签提前出现了。
		label := render.Box{X: box.X, Y: box.Y - 30, W: box.W, H: 30}
		if label.Y < 0 {
			label.Y, label.H = 0, 0
		}
		labelDiff := regionDiff(before, base, label)
		fmt.Printf("  %-12s 值 %-4g 柱体区非白像素 %d（应 0）；柱顶标签带与原图差 %d 像素（0 = 标签已出现）\n",
			b.ID, b.Value, inside, labelDiff)
	}
	fmt.Printf("产物：%s（plate / layer-* / before-bars / composed）\n", *outDir)
	if n != 0 {
		os.Exit(1)
	}
}

func readPNG(path string) (*image.NRGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	decoded, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	// png.Decode 对不透明图返回 *image.RGBA；统一成 NRGBA，后面比差不用管实现类型。
	out := image.NewNRGBA(decoded.Bounds())
	blit(out, decoded, image.Point{})
	return out, nil
}

func writePNG(path string, img image.Image) {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}

func blit(dst *image.NRGBA, src image.Image, at image.Point) {
	b := src.Bounds()
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			if image.Pt(at.X+x, at.Y+y).In(dst.Bounds()) {
				dst.Set(at.X+x, at.Y+y, src.At(b.Min.X+x, b.Min.Y+y))
			}
		}
	}
}

func compare(a, b *image.NRGBA) (sum, max, n int) {
	for y := 0; y < a.Bounds().Dy(); y++ {
		for x := 0; x < a.Bounds().Dx(); x++ {
			c1, c2 := a.NRGBAAt(x, y), b.NRGBAAt(x, y)
			d := abs(int(c1.R)-int(c2.R)) + abs(int(c1.G)-int(c2.G)) + abs(int(c1.B)-int(c2.B))
			if d > 0 {
				sum += d
				n++
				if d > max {
					max = d
				}
			}
		}
	}
	return sum, max, n
}

// nonWhite 数一个矩形里「不是纯白」的像素。
func nonWhite(img *image.NRGBA, box render.Box) int {
	n := 0
	for y := box.Y; y < box.Y+box.H; y++ {
		for x := box.X; x < box.X+box.W; x++ {
			if !image.Pt(x, y).In(img.Bounds()) {
				continue
			}
			c := img.NRGBAAt(x, y)
			if c.R != 255 || c.G != 255 || c.B != 255 {
				n++
			}
		}
	}
	return n
}

// regionDiff 数两个图在矩形内不同的像素数。
func regionDiff(a, b *image.NRGBA, box render.Box) int {
	n := 0
	for y := box.Y; y < box.Y+box.H; y++ {
		for x := box.X; x < box.X+box.W; x++ {
			if !image.Pt(x, y).In(a.Bounds()) || !image.Pt(x, y).In(b.Bounds()) {
				continue
			}
			c1, c2 := a.NRGBAAt(x, y), b.NRGBAAt(x, y)
			if c1 != c2 {
				n++
			}
		}
	}
	return n
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
