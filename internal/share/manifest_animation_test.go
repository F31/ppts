package share

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/animation"
)

// 有动画的页：底板 + 每层位图都进包，清单带 plate/transition/layers。
func TestBuildWritesLayeredSlides(t *testing.T) {
	reader := baseReader(t)
	in := buildInput()
	in.Pages = []PageAnimation{
		{
			PlatePNG:   pngBytes(t, 1920, 1080),
			Transition: SlideTransition{Type: "fade", DurationMs: 400},
			Layers: []LayerAsset{{
				ElementID: "title-1", Kind: "title",
				Box: SlideBox{X: 120, Y: 96, W: 800, H: 120},
				PNG: pngBytes(t, 800, 120),
				Clips: []Clip{{
					ClipID: "title-1-in", Effect: "slideIn", StartMs: 100, DurationMs: 700,
					Easing: animation.CubicBezier{X1: 0.22, Y1: 1, X2: 0.36, Y2: 1},
					Props:  []animation.MotionProp{{Name: "opacity", From: 0, To: 1}},
				}},
			}},
		},
		{},
	}

	var buf bytes.Buffer
	manifest, err := NewBundleBuilder(reader, nil).Build(context.Background(), in, &buf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	_, files, err := VerifyBundle(buf.Bytes())
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}

	if manifest.SchemaVersion != 2 {
		t.Fatalf("schemaVersion = %d, want 2", manifest.SchemaVersion)
	}
	first := manifest.Slides[0]
	if !first.Plate {
		t.Fatal("slide 0 should be flagged as plate")
	}
	if first.Transition == nil || first.Transition.Type != "fade" || first.Transition.DurationMs != 400 {
		t.Fatalf("transition = %+v", first.Transition)
	}
	if len(first.Layers) != 1 {
		t.Fatalf("layers = %d, want 1", len(first.Layers))
	}
	layer := first.Layers[0]
	if layer.ElementID != "title-1" || layer.Box.W != 800 || layer.Box.H != 120 {
		t.Fatalf("layer = %+v", layer)
	}
	if _, ok := files[layer.Image]; !ok {
		t.Fatalf("layer image %q missing from bundle", layer.Image)
	}
	// clips 必须原样带过去：播放器要靠 easing 四点自己编译关键帧。
	if len(layer.Clips) != 1 || layer.Clips[0].Easing.X1 != 0.22 || layer.Clips[0].DurationMs != 700 {
		t.Fatalf("clips not preserved: %+v", layer.Clips)
	}
	// 没有动画的第二页保持 M6-1 的形态：不是底板、没有层。
	if manifest.Slides[1].Plate || len(manifest.Slides[1].Layers) != 0 {
		t.Fatalf("slide 1 should stay static: %+v", manifest.Slides[1])
	}
}

// 开关关闭的包必须带版本 2 的清单但不含任何动画字段——
// 与 M6-1 逐像素一致的判据就是「layers 为空、plate 为 false」。
func TestBuildWithoutAnimationStaysStatic(t *testing.T) {
	reader := baseReader(t)
	in := buildInput()
	var buf bytes.Buffer
	manifest, err := NewBundleBuilder(reader, nil).Build(context.Background(), in, &buf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for i, slide := range manifest.Slides {
		if slide.Plate {
			t.Fatalf("slide %d marked as plate without animation", i)
		}
		if len(slide.Layers) != 0 {
			t.Fatalf("slide %d has %d layers without animation", i, len(slide.Layers))
		}
		if slide.Transition != nil {
			t.Fatalf("slide %d has transition without animation", i)
		}
		// 仍是 JPEG 转码路径：无动画的包必须与 M6-1 逐字节同源，
		// 若这里变成 .png，说明静态路径被新逻辑顺带改了（体积会翻倍）。
		if !strings.HasSuffix(slide.Image, ".jpg") {
			t.Fatalf("slide %d image = %q, want a JPEG transcoded entry", i, slide.Image)
		}
	}

	// 逐字节：包里的页图必须**就是** EncodePage 的输出，说明分层分支没有
	// 顺带换掉静态页的转码（换质量、换尺寸都算回归），归档里也不能残留层图。
	_, files, err := VerifyBundle(buf.Bytes())
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}
	want, ext, err := EncodePage(reader.pages[in.PagePNGKeys[0]], NewBundleBuilder(reader, nil).maxLongEdge)
	if err != nil {
		t.Fatalf("EncodePage: %v", err)
	}
	got, ok := files["slides/001"+ext]
	if !ok {
		t.Fatalf("slides/001%s missing from bundle", ext)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("static page bytes differ from EncodePage output (%d vs %d bytes)", len(got), len(want))
	}
	for name := range files {
		if strings.Contains(name, "-layer-") {
			t.Fatalf("static bundle must not contain layer assets: %s", name)
		}
	}
}

// 有层但没挖空时必须在打包阶段失败：层图会盖在已含元素本体的完整页上，
// 同一元素被画两次，入场期间能看到重影——这种画面不报错，只会被当成「渲染有点糊」。
func TestManifestRejectsLayersWithoutPlate(t *testing.T) {
	m := &Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Video:         VideoInfo{Width: 1920, Height: 1080, FPS: 30},
		Artifact:      ArtifactInfo{DurationUS: 100_000},
		Subtitles:     SubtitleInfo{Src: FileSubtitlesVTT, Inline: FileSubtitlesJS},
		Slides: []SlideEntry{{
			SlideID: "s1", StartUS: 0, EndUS: 100_000, Image: "slides/001.jpg",
			Layers: []SlideLayer{{
				ElementID: "a", Image: "slides/001-layer-00.png",
				Box: SlideBox{W: 10, H: 10},
			}},
		}},
	}
	err := m.Validate(nil)
	if err == nil {
		t.Fatal("expected validation error for layers without plate")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("drawn twice")) {
		t.Fatalf("error should explain the double-draw risk, got: %v", err)
	}
}

// 缺图页不能沿用上一页的底板：底板是挖空的，拿来当完整页就是一块纯色疤。
func TestMissingPageNeverReusesPlate(t *testing.T) {
	reader := baseReader(t)
	in := buildInput()
	// 第一页有动画（底板），第二页的 key 指向不存在的对象。
	in.Pages = []PageAnimation{{
		PlatePNG:   pngBytes(t, 1920, 1080),
		Transition: SlideTransition{Type: "none"},
		Layers: []LayerAsset{{
			ElementID: "t", Box: SlideBox{W: 40, H: 20},
			PNG: pngBytes(t, 40, 20),
		}},
	}}
	in.PagePNGKeys = []string{"tenants/t1/projects/p1/pages/1.png", "tenants/t1/projects/p1/pages/missing.png"}

	var buf bytes.Buffer
	manifest, err := NewBundleBuilder(reader, nil).Build(context.Background(), in, &buf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if manifest.Slides[1].Image == manifest.Slides[0].Image {
		t.Fatalf("slide 1 reused plate image %q", manifest.Slides[0].Image)
	}
}

// 清单字段名是给播放器读的契约，改名字要改播放器。这里锁住 JSON 形状，
// 避免「字段名换了但两端都编译通过」这种静默不兼容。
func TestManifestAnimationJSONShape(t *testing.T) {
	entry := SlideEntry{
		SlideID: "s1", StartUS: 0, EndUS: 1000, Image: "slides/001.png",
		Plate: true, Transition: &SlideTransition{Type: "fade", DurationMs: 400},
		Layers: []SlideLayer{{
			ElementID: "t", Kind: "title", Box: SlideBox{X: 1, Y: 2, W: 3, H: 4},
			Image: "slides/001-layer-00.png",
			Clips: []Clip{{
				Effect: "fadeIn", StartMs: 0, DurationMs: 500,
				Easing: animation.CubicBezier{X1: 0, Y1: 0, X2: 1, Y2: 1},
				Props:  []animation.MotionProp{{Name: "opacity", From: 0, To: 1}},
			}},
		}},
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, want := range []string{
		`"plate":true`, `"transition":{"type":"fade","durationMs":400}`,
		`"elementId":"t"`, `"box":{"x":1,"y":2,"w":3,"h":4}`,
		`"clips":[`, `"easing":{"x1":0,"y1":0,"x2":1,"y2":1}`,
		`"props":[{"name":"opacity","from":0,"to":1}]`,
	} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatalf("manifest JSON missing %s\n%s", want, raw)
		}
	}
}
