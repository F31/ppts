package app

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/integrations/objectstore"
	sliderenderer "github.com/F31/ppts/internal/integrations/render"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/render"
	"github.com/F31/ppts/internal/share"
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
	pages, err := bundleAnimations(plan, [][]byte{syntheticPage(t)}, nil)
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
	pages, err := bundleAnimations(plan, [][]byte{syntheticPage(t)}, nil)
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
	pages, err := bundleAnimations(animatedPlan(), [][]byte{nil}, nil)
	if err != nil {
		t.Fatalf("bundleAnimations: %v", err)
	}
	if len(pages[0].PlatePNG) != 0 || len(pages[0].Layers) != 0 {
		t.Fatal("expected empty animation for missing page image")
	}
}

// 生产链路验收（G3-5）：带 Crop 的镜头页必须真的裁出特写 PNG，而非只活在夹具里。
//
// 画布与源图同为 200×100，因此 sx=sy=1，裁切框 1:1 落到源像素；
// 红块位于 (80,30)-(120,70)，裁出后中心应仍是红的。
func TestBundleAnimationsBuildsShotAssets(t *testing.T) {
	pagePNG := syntheticPage(t) // 200×100 白底，中央 40×40 红块
	plan := &render.Timeline{
		Width: 200, Height: 100, FPS: 30,
		Pages: []render.Page{{
			Index:      0,
			SlideID:    "s1",
			DurationMs: 1000,
			Shots: []render.Shot{
				{Index: 0, Image: "slides/001.png", StartMs: 0, DurationMs: 500, CrossFadeMs: 0},
				{Index: 1, Image: "slides/001-shot-01.png", StartMs: 500, DurationMs: 500, CrossFadeMs: 120,
					Crop: &render.Box{X: 80, Y: 30, W: 40, H: 40}},
			},
		}},
	}
	pages, err := bundleAnimations(plan, [][]byte{pagePNG}, nil)
	if err != nil {
		t.Fatalf("bundleAnimations: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("pages = %d", len(pages))
	}
	anim := pages[0]
	if len(anim.Shots) != 2 {
		t.Fatalf("expected 2 shots, got %d", len(anim.Shots))
	}
	// 首镜复用页级图：PNG 必须留空。
	if len(anim.Shots[0].PNG) != 0 {
		t.Fatalf("first shot should reuse page image (empty PNG), got %d bytes", len(anim.Shots[0].PNG))
	}
	// 次镜是被裁出的特写：PNG 必须非空，且尺寸等于裁切框（原生分辨率，不放大）。
	if len(anim.Shots[1].PNG) == 0 {
		t.Fatal("second shot must have cropped PNG bytes")
	}
	shotImg, err := png.Decode(bytes.NewReader(anim.Shots[1].PNG))
	if err != nil {
		t.Fatalf("decode shot: %v", err)
	}
	if b := shotImg.Bounds(); b.Dx() != 40 || b.Dy() != 40 {
		t.Fatalf("shot image size = %dx%d, want 40x40", b.Dx(), b.Dy())
	}
	// 裁出的区域中心仍是红块：证明裁的是对的画面，而不是随机一块。
	if r, g, bb, _ := shotImg.At(20, 20).RGBA(); !(r>>8 > 200 && g>>8 < 80 && bb>>8 < 80) {
		t.Fatalf("shot center lost the red block: rgb=(%d,%d,%d)", r>>8, g>>8, bb>>8)
	}
	// 落盘 Crop 必须按源像素坐标，且比例与裁出 PNG 一致（checkCropAspect 会再拦一次）。
	wantCrop := share.SlideBox{X: 80, Y: 30, W: 40, H: 40}
	if anim.Shots[1].Crop == nil || *anim.Shots[1].Crop != wantCrop {
		t.Fatalf("shot crop = %+v, want %+v", anim.Shots[1].Crop, wantCrop)
	}
}

// syntheticPageSize 造一张 w×h 的白底图，中央画一个红块——用于模拟不同 DPI 的源页图。
func syntheticPageSize(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	cx, cy := w/2, h/2
	hw, hh := w/10, h/10
	for y := cy - hh; y < cy+hh; y++ {
		for x := cx - hw; x < cx+hw; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 220, G: 30, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode page: %v", err)
	}
	return buf.Bytes()
}

// ---- 300 DPI 重渲注入（G3-5 #25）的验收 ----

// fakeSlideRenderer 返回预设的逐页 PNG，模拟 300 DPI 渲染结果（不做真实光栅化）。
type fakeSlideRenderer struct {
	pages []sliderenderer.PageImage
	err   error
}

func (f *fakeSlideRenderer) Render(_ context.Context, _ io.ReaderAt, _ int64, _ sliderenderer.RenderOptions) (*sliderenderer.RenderResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &sliderenderer.RenderResult{Pages: f.pages, Report: sliderenderer.RenderReport{DPI: 300}}, nil
}

// fakeSourceRevisions 返回固定源版本。
type fakeSourceRevisions struct {
	rev *project.SourceRevision
	err error
}

func (f *fakeSourceRevisions) GetSourceRevision(_ context.Context, _, _ string, _ int) (*project.SourceRevision, error) {
	return f.rev, f.err
}

// fakeObjectStore 对任意 key 返回固定字节。
type fakeObjectStore struct{ data []byte }

func (f *fakeObjectStore) Put(_ context.Context, _ objectstore.ObjectKey, _ io.Reader, _ objectstore.ObjectMeta) error {
	return nil
}
func (f *fakeObjectStore) Get(_ context.Context, _ objectstore.ObjectKey) (io.ReadCloser, objectstore.ObjectMeta, error) {
	return io.NopCloser(bytes.NewReader(f.data)), objectstore.ObjectMeta{}, nil
}
func (f *fakeObjectStore) SignedURL(_ context.Context, _ objectstore.ObjectKey, _ objectstore.Operation, _ time.Duration) (string, error) {
	return "", nil
}
func (f *fakeObjectStore) Delete(_ context.Context, _ objectstore.ObjectKey) error { return nil }
func (f *fakeObjectStore) ApplyLifecyclePolicy(_ context.Context, _ string, _ objectstore.LifecyclePolicy) error {
	return nil
}

// TestRerenderHiResPagesSubstitutesCroppedPages 验证：带 Crop 的页被 300 DPI 源替换，
// 无 Crop 的页保持 150 DPI。这正是设计 §5「仅裁切页替换源图」的契约。
func TestRerenderHiResPagesSubstitutesCroppedPages(t *testing.T) {
	plan := &render.Timeline{
		Width: 200, Height: 100, FPS: 30,
		Pages: []render.Page{
			{Index: 0, SlideID: "s1", DurationMs: 1000},
			{Index: 1, SlideID: "s2", DurationMs: 1000, Shots: []render.Shot{
				{Index: 0, Image: "slides/002.png", StartMs: 0, DurationMs: 500},
				{Index: 1, Image: "slides/002-shot-01.png", StartMs: 500, DurationMs: 500,
					Crop: &render.Box{X: 10, Y: 10, W: 20, H: 20}},
			}},
		},
	}
	bundle := &TimelineAsset{Timeline: &media.Timeline{SourceRevisionNo: 3}}
	lowPNG := syntheticPage(t)                // 150 DPI 源：200×100
	highPNG := syntheticPageSize(t, 400, 200) // 300 DPI 源：2× 分辨率
	pagePNGs := [][]byte{lowPNG, lowPNG}

	h := &ExportHandler{
		slideRenderer: &fakeSlideRenderer{pages: []sliderenderer.PageImage{
			{Index: 0, Width: 200, Height: 100, PNG: lowPNG},
			{Index: 1, Width: 400, Height: 200, PNG: highPNG},
		}},
		sourceRevisions: &fakeSourceRevisions{rev: &project.SourceRevision{ObjectKey: "tenant/proj/rev/3/source.pptx"}},
		objects:         &fakeObjectStore{data: []byte("fake pptx bytes")},
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h.rerenderHiResPages(context.Background(), &pipeline.Job{TenantID: "tenant", ProjectID: "proj"}, plan, bundle, pagePNGs)
	if !bytes.Equal(pagePNGs[1], highPNG) {
		t.Fatal("cropped page must be replaced with 300dpi source")
	}
	if !bytes.Equal(pagePNGs[0], lowPNG) {
		t.Fatal("non-cropped page must keep 150dpi source")
	}
}

// TestRerenderHiResPagesSkipsWhenRendererNil 验证 graceful 降级：渲染器未注入时，
// 即便有 Crop 也不触碰 pagePNGs，整包退回 150 DPI 模糊特写路径。
func TestRerenderHiResPagesSkipsWhenRendererNil(t *testing.T) {
	plan := &render.Timeline{
		Width: 200, Height: 100, FPS: 30,
		Pages: []render.Page{{
			Index: 0, SlideID: "s1", DurationMs: 1000,
			Shots: []render.Shot{{Index: 0, Image: "x"}, {Index: 1, Crop: &render.Box{X: 1, Y: 1, W: 2, H: 2}}},
		}},
	}
	bundle := &TimelineAsset{Timeline: &media.Timeline{SourceRevisionNo: 3}}
	pagePNGs := [][]byte{syntheticPage(t)}
	h := &ExportHandler{} // 无渲染器、无源查询、无对象存储
	h.rerenderHiResPages(context.Background(), &pipeline.Job{TenantID: "t", ProjectID: "p"}, plan, bundle, pagePNGs)
	if len(pagePNGs[0]) == 0 {
		t.Fatal("without renderer, pagePNGs must be untouched")
	}
}

// TestPageHasCrop 验证镜头裁切检测：首镜（整页）不算 Crop，次镜带框才算。
func TestPageHasCrop(t *testing.T) {
	if pageHasCrop(render.Page{}) {
		t.Fatal("empty page has no crop")
	}
	if pageHasCrop(render.Page{Shots: []render.Shot{{Index: 0, Image: "x"}}}) {
		t.Fatal("first shot (whole page) is not a crop")
	}
	if !pageHasCrop(render.Page{Shots: []render.Shot{
		{Index: 0, Image: "x"},
		{Index: 1, Crop: &render.Box{X: 1, Y: 1, W: 2, H: 2}},
	}}) {
		t.Fatal("second shot with crop must be detected")
	}
}

// edgeEnergy 用灰度梯度之和近似「边缘锐度」：锐利的裁切图梯度能量高，一旦被重采样放大就骤降。
func edgeEnergy(img image.Image) float64 {
	b := img.Bounds()
	gray := func(c color.Color) float64 {
		r, g, bb, _ := c.RGBA()
		return (float64(r) + float64(g) + float64(bb)) / 3 / 256
	}
	var sum float64
	for y := b.Min.Y; y < b.Max.Y-1; y++ {
		for x := b.Min.X; x < b.Max.X-1; x++ {
			dx := gray(img.At(x+1, y)) - gray(img.At(x, y))
			dy := gray(img.At(x, y+1)) - gray(img.At(x, y))
			sum += math.Abs(dx) + math.Abs(dy)
		}
	}
	return sum
}

// TestShotCropPreservesEdgeEnergy 验收设计 §5.3：特写裁切（来自 300 DPI 重渲源）的
// 边缘梯度能量 ≥ 同区域「300 DPI 直接裁切参照」的 95%。cropShot 只做像素对齐的整数裁切、
// 不重采样，因此应等于参照（≈100%）；这条断言守住了「有人引入缩放会立刻跌破 95%」。
//
// 300 DPI 源由 #25 的 rerenderHiResPages 注入，这里只验证裁切本身不引入模糊。
func TestShotCropPreservesEdgeEnergy(t *testing.T) {
	// 600×300 高分辨率源，画一个锐利黑框（制造强边缘）。
	const W, H = 600, 300
	src := image.NewNRGBA(image.Rect(0, 0, W, H))
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	for y := 80; y < 220; y++ {
		for x := 120; x < 360; x++ {
			if y < 90 || y >= 210 || x < 130 || x >= 350 {
				src.SetNRGBA(x, y, color.NRGBA{R: 20, G: 20, B: 20, A: 255})
			}
		}
	}
	crop := render.Box{X: 100, Y: 60, W: 300, H: 180} // 画布=源，sx=sy=1
	pngBytes, err := cropShot(src, crop, W, H)
	if err != nil {
		t.Fatalf("cropShot: %v", err)
	}
	shotImg, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("decode shot: %v", err)
	}
	got := edgeEnergy(shotImg)
	// 参照：同一源、同一区域的「直接」裁切（独立实现，不走 cropShot），作为 300 DPI 直接裁切参照。
	ref := edgeEnergy(src.SubImage(image.Rect(crop.X, crop.Y, crop.X+crop.W, crop.Y+crop.H)))
	if ref == 0 {
		t.Fatal("reference edge energy is zero; test image has no edges")
	}
	ratio := got / ref
	if ratio < 0.95 {
		t.Fatalf("shot edge energy ratio = %.3f, want >= 0.95 (cropShot must not resample)", ratio)
	}
}

