package main

import (
	"bytes"
	"image/png"
	"testing"
)

// 合成动画素材是浏览器门禁的**测试夹具**，不是产品代码：它坏了不会影响任何用户，
// 但会让 V17 的四条判据静默失去被测对象。所以这里守三条：
//   - 层数与 clips 真的非空（否则 `.ppts-layer` 根本不会被建出来）；
//   - 位移起点为正（全零位移等于"没动"，"默认态存在位移"会红）；
//   - 底板与层图都能解码，且尺寸落回 Box（尺寸不符会被播放器按比例拉伸，判据仍过但画面不对）。
func TestSyntheticAnimation(t *testing.T) {
	const w, h, count = 1920, 1080, 2
	page := syntheticAnimation(w, h, 1, count)

	if page.Transition.Type != "fade" || page.Transition.DurationMs <= 0 {
		t.Fatalf("transition = %+v，期望 fade 且时长 > 0", page.Transition)
	}
	plate, err := png.Decode(bytes.NewReader(page.PlatePNG))
	if err != nil {
		t.Fatalf("底板不是合法 PNG: %v", err)
	}
	if b := plate.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("底板尺寸 = %dx%d，期望 %dx%d", b.Dx(), b.Dy(), w, h)
	}

	if len(page.Layers) != count {
		t.Fatalf("层数 = %d，期望 %d", len(page.Layers), count)
	}
	for i, layer := range page.Layers {
		if layer.ElementID == "" || layer.Kind == "" {
			t.Fatalf("第 %d 层缺 elementId / kind：%+v", i, layer)
		}
		img, err := png.Decode(bytes.NewReader(layer.PNG))
		if err != nil {
			t.Fatalf("第 %d 层的位图不是合法 PNG: %v", i, err)
		}
		if b := img.Bounds(); b.Dx() != layer.Box.W || b.Dy() != layer.Box.H {
			t.Fatalf("第 %d 层位图 %dx%d 与 Box %dx%d 不符", i, b.Dx(), b.Dy(), layer.Box.W, layer.Box.H)
		}

		var opacity, translateY bool
		for _, clip := range layer.Clips {
			if clip.DurationMs <= 0 {
				t.Fatalf("第 %d 层有零时长 clip：%+v", i, clip)
			}
			for _, prop := range clip.Props {
				switch prop.Name {
				case "opacity":
					opacity = true
					if prop.From != 0 || prop.To != 1 {
						t.Fatalf("第 %d 层 opacity 应为 0→1，实际 %v→%v", i, prop.From, prop.To)
					}
				case "translateY":
					translateY = true
					// 起点为 0 的话「默认态存在位移」这条判据就没有位移可测。
					if prop.From == 0 {
						t.Fatalf("第 %d 层 translateY 起点为 0，没有位移可测", i)
					}
				}
			}
		}
		if !opacity || !translateY {
			t.Fatalf("第 %d 层缺 opacity 或 translateY 轨道：%+v", i, layer.Clips)
		}
	}
}
