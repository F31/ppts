package app

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/render"
)

// 造一张 200×100 的白底图，中央画一个 40×40 的红块——它就是要被抬升成图层的元素。
func syntheticPage(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	for y := 30; y < 70; y++ {
		for x := 80; x < 120; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 220, G: 30, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode page: %v", err)
	}
	return buf.Bytes()
}

func animatedPlan() *render.Timeline {
	return &render.Timeline{
		Width: 200, Height: 100, FPS: 30,
		Pages: []render.Page{{
			Index: 0, SlideID: "s1", DurationMs: 1000,
			Transition: render.Transition{Type: "fade", DurationMs: 300},
			Layers: []render.Layer{{
				ElementID: "box-1", Kind: "shape",
				Box: render.Box{X: 80, Y: 30, W: 40, H: 40},
				Clips: []render.Motion{{Effect: "fadeIn", StartMs: 0, DurationMs: 500,
					Easing: animation.CubicBezier{X1: 0, Y1: 0, X2: 1, Y2: 1},
					Props:  []animation.MotionProp{{Name: "opacity", From: 0, To: 1}}}},
			}},
		}},
	}
}

func TestBundleAnimationsBlanksPlateAndCropsLayer(t *testing.T) {
	plan := animatedPlan()
	pages, err := bundleAnimations(plan, [][]byte{syntheticPage(t)})
	if err != nil {
		t.Fatalf("bundleAnimations: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("pages = %d", len(pages))
	}
	anim := pages[0]
	if len(anim.PlatePNG) == 0 || len(anim.Layers) != 1 {
		t.Fatalf("expected plate + 1 layer, got plate=%d layers=%d", len(anim.PlatePNG), len(anim.Layers))
	}
	if anim.Transition.Type != "fade" || anim.Transition.DurationMs != 300 {
		t.Fatalf("transition = %+v", anim.Transition)
	}

	plate, err := png.Decode(bytes.NewReader(anim.PlatePNG))
	if err != nil {
		t.Fatalf("decode plate: %v", err)
	}
	// 底板必须把元素本体填平：中心若还是红的，入场时静态副本与移动副本会重叠（ADR-6）。
	if r, g, b, _ := plate.At(100, 50).RGBA(); r>>8 > 200 && g>>8 < 80 && b>>8 < 80 {
		t.Fatalf("plate still contains the element at center: rgb=(%d,%d,%d)", r>>8, g>>8, b>>8)
	}

	layer := anim.Layers[0]
	if layer.Box.W != 40 || layer.Box.H != 40 {
		t.Fatalf("layer box = %+v", layer.Box)
	}
	layerImg, err := png.Decode(bytes.NewReader(layer.PNG))
	if err != nil {
		t.Fatalf("decode layer: %v", err)
	}
	if b := layerImg.Bounds(); b.Dx() != 40 || b.Dy() != 40 {
		t.Fatalf("layer image size = %dx%d", b.Dx(), b.Dy())
	}
	// 层图里必须仍留着这个元素本身（否则播出来是个空心框）。
	if r, g, bb, _ := layerImg.At(20, 20).RGBA(); !(r>>8 > 200 && g>>8 < 80 && bb>>8 < 80) {
		t.Fatalf("layer lost its own pixels: rgb=(%d,%d,%d)", r>>8, g>>8, bb>>8)
	}
	// clips 必须带过去，播放器靠它编译关键帧。
	if len(layer.Clips) != 1 || layer.Clips[0].DurationMs != 500 {
		t.Fatalf("clips = %+v", layer.Clips)
	}
}

// 没有 clip 的轨道不该出图：挖空它只会多一张几乎等于底板的位图，
// 而且清单里会出现一批「本来就不该动」的层。
func TestBundleAnimationsSkipsPagesWithoutClips(t *testing.T) {
	plan := animatedPlan()
	plan.Pages[0].Layers[0].Clips = nil
	pages, err := bundleAnimations(plan, [][]byte{syntheticPage(t)})
	if err != nil {
		t.Fatalf("bundleAnimations: %v", err)
	}
	if len(pages[0].PlatePNG) != 0 || len(pages[0].Layers) != 0 {
		t.Fatalf("page without clips should stay static: plate=%d layers=%d",
			len(pages[0].PlatePNG), len(pages[0].Layers))
	}
}

// 页图缺失时不能造半个包：没有底图就无从挖空，应交给打包器的缺图降级。
func TestBundleAnimationsToleratesMissingPageImage(t *testing.T) {
	pages, err := bundleAnimations(animatedPlan(), [][]byte{nil})
	if err != nil {
		t.Fatalf("bundleAnimations: %v", err)
	}
	if len(pages[0].PlatePNG) != 0 || len(pages[0].Layers) != 0 {
		t.Fatal("expected empty animation for missing page image")
	}
}
