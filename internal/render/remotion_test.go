package render

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func onePagePlan(baseImage string, layers []Layer) *Timeline {
	return &Timeline{
		SchemaVersion: IRSchemaVersion,
		FPS:           30,
		Width:         200,
		Height:        100,
		Background:    "#FFFFFF",
		Pages: []Page{{
			Index:      0,
			SlideID:    "s0",
			DurationMs: 1000,
			BaseImage:  baseImage,
			Layers:     layers,
			Transition: Transition{Type: "none"},
		}},
	}
}

// staging 是 backend 的内部契约：底板必须生成、文件名要写回计划副本、原计划不能被动。
func TestStageBuildsPlateAndLeavesPlanUntouched(t *testing.T) {
	dir := t.TempDir()
	white := color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	blue := color.NRGBA{0x1E, 0x3A, 0x8A, 0xFF}
	page := flatPage(200, 100, white)
	fillRect(page, 20, 20, 120, 80, blue)
	writePNG(t, filepath.Join(dir, "pages", "p000.png"), page)

	plan := onePagePlan("pages/p000.png", []Layer{layer("title", Box{X: 20, Y: 20, W: 100, H: 60})})
	b := &RemotionBackend{}
	staged, err := b.stage(plan, dir)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}

	if staged.Pages[0].PlateImage != "plates/plate_000.png" {
		t.Errorf("plateImage = %q", staged.Pages[0].PlateImage)
	}
	if plan.Pages[0].PlateImage != "" {
		t.Errorf("stage mutated caller plan: %q", plan.Pages[0].PlateImage)
	}

	plateFile, err := os.Open(filepath.Join(dir, "plates", "plate_000.png"))
	if err != nil {
		t.Fatalf("plate file: %v", err)
	}
	defer plateFile.Close()
	plate, err := png.Decode(plateFile)
	if err != nil {
		t.Fatalf("decode plate: %v", err)
	}
	if got := plate.At(50, 40); !sameColor(got, white) {
		t.Errorf("plate at element center = %v, want white", got)
	}
}

// 层素材必须落盘并回填到计划副本：父层素材里的子元素应在这一步被抹掉（ADR-9）。
func TestStageWritesLayerAssets(t *testing.T) {
	dir := t.TempDir()
	white := color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	cardColor := color.NRGBA{0xF1, 0xF5, 0xF9, 0xFF}
	blue := color.NRGBA{0x1E, 0x3A, 0x8A, 0xFF}
	page := flatPage(200, 100, white)
	fillRect(page, 20, 20, 180, 90, cardColor)
	fillRect(page, 40, 40, 140, 60, blue)
	writePNG(t, filepath.Join(dir, "pages", "p000.png"), page)

	plan := onePagePlan("pages/p000.png", []Layer{
		layer("card", Box{X: 20, Y: 20, W: 160, H: 70}),
		layer("bullet", Box{X: 40, Y: 40, W: 100, H: 20}),
	})
	staged, err := (&RemotionBackend{}).stage(plan, dir)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}

	want := []string{"layers/layer_000_00.png", "layers/layer_000_01.png"}
	for i, name := range want {
		if got := staged.Pages[0].Layers[i].Image; got != name {
			t.Fatalf("layers[%d].image = %q, want %q", i, got, name)
		}
	}
	if plan.Pages[0].Layers[0].Image != "" {
		t.Errorf("stage mutated caller plan: %q", plan.Pages[0].Layers[0].Image)
	}

	f, err := os.Open(filepath.Join(dir, filepath.FromSlash(want[0])))
	if err != nil {
		t.Fatalf("open card asset: %v", err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode card asset: %v", err)
	}
	if got := img.At(70, 30); !sameColor(got, cardColor) {
		t.Errorf("nested child survived in card asset: %v", got)
	}
}

// sameColor 按通道值比较：png.Decode 产出的具体类型与内存里的 NRGBA 不同，
// 直接比较 color.Color 接口会因为动态类型不同而误报不等。
func sameColor(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

// 没有元素级图层就不该产出底板：多余的文件会让「为什么这页没有 plate」变得难以解释。
func TestStageSkipsPlateWithoutElementLayers(t *testing.T) {
	dir := t.TempDir()
	page := flatPage(200, 100, color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF})
	writePNG(t, filepath.Join(dir, "pages", "p000.png"), page)

	plan := onePagePlan("pages/p000.png", []Layer{layer("", Box{X: 0, Y: 0, W: 200, H: 100})})
	b := &RemotionBackend{}
	staged, err := b.stage(plan, dir)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if staged.Pages[0].PlateImage != "" {
		t.Errorf("unexpected plate for page layer: %q", staged.Pages[0].PlateImage)
	}
	if _, err := os.Stat(filepath.Join(dir, "plates")); !os.IsNotExist(err) {
		t.Errorf("plate dir should not exist when nothing to patch")
	}
}

// 噪声底要留下 warning：补丁在照片底上可能可见，静默通过等于把问题藏到成片里。
func TestStageLogsNoisyPatch(t *testing.T) {
	dir := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			shade := uint8(40 + (x/4)*4)
			img.SetNRGBA(x, y, color.NRGBA{shade, shade, shade, 0xFF})
		}
	}
	writePNG(t, filepath.Join(dir, "pages", "p000.png"), img)

	var logs []string
	b := &RemotionBackend{Logf: func(format string, args ...any) {
		logs = append(logs, format)
	}}
	plan := onePagePlan("pages/p000.png", []Layer{layer("title", Box{X: 40, Y: 20, W: 60, H: 40})})
	if _, err := b.stage(plan, dir); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if len(logs) == 0 || !strings.Contains(logs[0], "plate patch may be visible") {
		t.Fatalf("want noisy warning, got %v", logs)
	}
}

// 缺少脚本时必须给出「怎么配」的错误，而不是等到 exec 时才报一串 Node 堆栈。
func TestRemotionBackendRequiresScript(t *testing.T) {
	t.Setenv("PPTS_RENDERER_SCRIPT", "")
	b := &RemotionBackend{}
	_, err := b.Render(context.Background(), onePagePlan("pages/p.png", nil), t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "PPTS_RENDERER_SCRIPT") {
		t.Fatalf("want script config error, got %v", err)
	}
}

// Node 不存在时：错误要指向二进制，而不是「渲染失败」这种无法排查的描述。
func TestRemotionBackendReportsMissingNode(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "render.mjs")
	if err := os.WriteFile(script, []byte("// noop"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &RemotionBackend{
		Script:  script,
		NodeBin: filepath.Join(dir, "definitely-not-node"),
		Timeout: 5 * time.Second,
	}
	_, err := b.Render(context.Background(), onePagePlan("pages/p.png", nil), dir, nil)
	if err == nil || !strings.Contains(err.Error(), "start renderer") {
		t.Fatalf("want start failure, got %v", err)
	}
}

func TestWorkPathRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		rel  string
		ok   bool
		name string
	}{
		{"pages/p000.png", true, "nested"},
		{"../secret.png", false, "parent"},
		{"pages/../../secret.png", false, "nested parent"},
		{"/etc/passwd", false, "absolute"},
		{"", false, "empty"},
	}
	for _, c := range cases {
		_, err := workPath(dir, c.rel)
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected rejection for %q", c.name, c.rel)
		}
	}
}

func TestVerifyMP4(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.mp4")
	body := append([]byte{0, 0, 0, 0x20}, []byte("ftypisom")...)
	body = append(body, make([]byte, 2048)...)
	if err := os.WriteFile(good, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyMP4(good); err != nil {
		t.Errorf("valid container rejected: %v", err)
	}

	bad := filepath.Join(dir, "bad.mp4")
	if err := os.WriteFile(bad, []byte("not an mp4 at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyMP4(bad); err == nil {
		t.Errorf("non-mp4 accepted")
	}

	if err := verifyMP4(filepath.Join(dir, "missing.mp4")); err == nil {
		t.Errorf("missing file accepted")
	}
}

func TestRenderTimeoutScalesWithContent(t *testing.T) {
	// 5 分钟长片：实测 2 核 943s（ADR-16），常量 15min 会在第 27 页掐断。
	if got := renderTimeout(0, 300*time.Second); got < 900*time.Second {
		t.Errorf("5min 片超时 %v，装不下实测的 943s", got)
	}
	// 短视频不该被放宽到离谱：地板仍是调用方给的 15min。
	if got := renderTimeout(0, 5*time.Second); got != defaultRenderTimeout {
		t.Errorf("短视频超时 %v，want %v", got, defaultRenderTimeout)
	}
	// 急活儿可以把地板调低，但不能低到内容装不下。
	if got := renderTimeout(2*time.Minute, 60*time.Second); got != 11*time.Minute {
		t.Errorf("1min 片超时 %v，want 11m", got)
	}
	// 计划里的 durationMs 没有上界：必须夹在天花板上，否则畸形计划能长期占住并发位。
	if got := renderTimeout(0, 100*time.Hour); got != maxRenderTimeout {
		t.Errorf("超长片超时 %v，want %v", got, maxRenderTimeout)
	}
	// 内容时长为 0（异常计划）时退回地板，不能返回 0 让 ctx 立刻超时。
	if got := renderTimeout(0, 0); got != defaultRenderTimeout {
		t.Errorf("零时长超时 %v，want %v", got, defaultRenderTimeout)
	}
}

func TestContentDurationSumsPages(t *testing.T) {
	plan := &Timeline{Pages: []Page{{DurationMs: 6000}, {DurationMs: 9000}}}
	if got := contentDuration(plan); got != 15*time.Second {
		t.Errorf("contentDuration = %v, want 15s", got)
	}
	if got := contentDuration(&Timeline{}); got != 0 {
		t.Errorf("空计划 contentDuration = %v, want 0", got)
	}
}
