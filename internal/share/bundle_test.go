package share

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/F31/ppts/internal/media"
)

// mapReader 是测试与本地导出共用的最小 ResourceReader 实现。
type mapReader struct {
	pages   map[string][]byte
	objects map[string][]byte
}

func (m *mapReader) OpenPagePNG(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := m.pages[key]
	if !ok {
		return nil, errors.New("page not found: " + key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *mapReader) OpenObject(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := m.objects[key]
	if !ok {
		return nil, errors.New("object not found: " + key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func pngBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// pcm16WAV 生成静音 PCM16 单声道 WAV，时长由 frames 决定（24kHz）。
func pcm16WAV(frames int) []byte {
	const sampleRate = 24000
	dataSize := frames * 2
	buf := make([]byte, 44+dataSize)
	copy(buf[0:], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+dataSize))
	copy(buf[8:], "WAVE")
	copy(buf[12:], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:], 16)
	binary.LittleEndian.PutUint16(buf[20:], 1)
	binary.LittleEndian.PutUint16(buf[22:], 1)
	binary.LittleEndian.PutUint32(buf[24:], sampleRate)
	binary.LittleEndian.PutUint32(buf[28:], sampleRate*2)
	binary.LittleEndian.PutUint16(buf[32:], 2)
	binary.LittleEndian.PutUint16(buf[34:], 16)
	copy(buf[36:], "data")
	binary.LittleEndian.PutUint32(buf[40:], uint32(dataSize))
	return buf
}

func twoPageTimeline() *media.Timeline {
	return &media.Timeline{
		SchemaVersion: media.TimelineSchemaVersion,
		DurationUS:    250_000,
		Slides: []media.SlideCue{
			{
				SlideID: "slide-1", StartUS: 0, EndUS: 100_000,
				Segments: []media.SegmentCue{{SegmentID: "seg-1", AudioKey: "tenants/t1/projects/p1/audio/1.wav", StartUS: 0, EndUS: 100_000}},
			},
			{
				SlideID: "slide-2", StartUS: 100_000, EndUS: 200_000,
				Segments: []media.SegmentCue{{SegmentID: "seg-2", AudioKey: "tenants/t1/projects/p1/audio/2.wav", StartUS: 100_000, EndUS: 200_000}},
			},
		},
		Subtitles: []media.SubtitleCue{
			{
				SlideID: "slide-1", SegmentID: "seg-1", StartUS: 0, EndUS: 100_000,
				Text:  "第一页的讲解内容比较长，需要按标点切分成多行，才能验证逐行轮换的行为是否与站内播放器一致。",
				Chars: []media.CharCue{{StartUS: 0, EndUS: 20_000, Char: "第"}},
			},
			{
				SlideID: "slide-2", SegmentID: "seg-2", StartUS: 100_000, EndUS: 200_000,
				Text: "第二页。",
			},
		},
	}
}

func buildInput() BundleInput {
	return BundleInput{
		Timeline:     twoPageTimeline(),
		PagePNGKeys:  []string{"tenants/t1/projects/p1/pages/1.png", "tenants/t1/projects/p1/pages/2.png"},
		SRTKey:       "tenants/t1/projects/p1/subs/cues.srt",
		VTTKey:       "tenants/t1/projects/p1/subs/cues.vtt",
		Width:        1920,
		Height:       1080,
		FPS:          30,
		Title:        "季度复盘 <script>alert(1)</script>",
		SnapshotHash: "sha256:abc",
	}
}

func baseReader(t *testing.T) *mapReader {
	t.Helper()
	return &mapReader{
		pages: map[string][]byte{
			"tenants/t1/projects/p1/pages/1.png": pngBytes(t, 1920, 1080),
			"tenants/t1/projects/p1/pages/2.png": pngBytes(t, 1280, 720),
		},
		objects: map[string][]byte{
			"tenants/t1/projects/p1/audio/1.wav":   pcm16WAV(2400),
			"tenants/t1/projects/p1/audio/2.wav":   pcm16WAV(2400),
			"tenants/t1/projects/p1/subs/cues.vtt": []byte("WEBVTT\n\n00:00:00.000 --> 00:00:10.000\n第一页\n"),
			"tenants/t1/projects/p1/subs/cues.srt": []byte("1\r\n00:00:00,000 --> 00:00:10,000\r\n第一页\r\n"),
		},
	}
}

func TestBuildProducesPlayableBundle(t *testing.T) {
	reader := baseReader(t)
	var buf bytes.Buffer
	manifest, err := NewBundleBuilder(reader, nil).Build(context.Background(), buildInput(), &buf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	parsed, files, err := VerifyBundle(buf.Bytes())
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}
	if parsed.SchemaVersion != ManifestSchemaVersion {
		t.Fatalf("schemaVersion = %d", parsed.SchemaVersion)
	}
	if got := CountExternalRefs(files); got != 0 {
		t.Fatalf("external refs = %d, want 0", got)
	}
	for _, name := range []string{
		FileIndexHTML, FileManifestJSON, FileManifestJS, FileSubtitlesVTT, FileSubtitlesSRT,
		FileSubtitlesJS, FileTimelineJSON, FileAppJS, FileAppCSS, FileAudioMix,
	} {
		if _, ok := files[name]; !ok {
			t.Fatalf("bundle is missing %s (have %v)", name, keys(files))
		}
	}
	if len(manifest.Slides) != 2 || manifest.Slides[0].Image != "slides/001.jpg" {
		t.Fatalf("slides = %+v", manifest.Slides)
	}
	// 1080p 输入必须被压到长边 ≤1600，且宽高比误差 ≤0.5%。
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(files[manifest.Slides[0].Image]))
	if err != nil {
		t.Fatalf("page 1 is not a jpeg: %v", err)
	}
	if cfg.Width != 1600 || cfg.Height != 900 {
		t.Fatalf("page 1 size = %dx%d, want 1600x900", cfg.Width, cfg.Height)
	}
	if ratio := float64(cfg.Width) / float64(cfg.Height); ratio < 1.7 || ratio > 1.8 {
		t.Fatalf("page 1 aspect ratio = %f", ratio)
	}
	// 720p 输入不放大。
	cfg2, err := jpeg.DecodeConfig(bytes.NewReader(files[manifest.Slides[1].Image]))
	if err != nil || cfg2.Width != 1280 || cfg2.Height != 720 {
		t.Fatalf("page 2 size = %v (err %v), want 1280x720", cfg2, err)
	}
	// 标题是不可信输入：必须被转义，不能原样出现 <script>。
	index := string(files[FileIndexHTML])
	if strings.Contains(index, "<script>alert(1)</script>") {
		t.Fatalf("index.html contains unescaped title: %s", index)
	}
	if !strings.Contains(index, "&lt;script&gt;") {
		t.Fatalf("index.html does not escape title: %s", index)
	}
	if !strings.Contains(index, "Content-Security-Policy") || !strings.Contains(index, "script-src 'self' file:") {
		t.Fatalf("index.html is missing CSP: %s", index)
	}
	for _, want := range []string{`src="manifest.js"`, `src="subtitles.js"`, `src="app.js"`, `href="app.css"`} {
		if !strings.Contains(index, want) {
			t.Fatalf("index.html missing %s", want)
		}
	}
	// 清单脚本必须是经典脚本（file:// 下可用），且字幕 cue 与时间轴同源。
	if !strings.HasPrefix(string(files[FileManifestJS]), "window.__PPT_MANIFEST__ = {") {
		t.Fatalf("manifest.js is not a classic script assignment: %s", files[FileManifestJS])
	}
	if !strings.Contains(string(files[FileSubtitlesJS]), `"text":"第二页。"`) {
		t.Fatalf("subtitles.js does not carry timeline cues: %s", files[FileSubtitlesJS])
	}
	if manifest.AudioMix != FileAudioMix {
		t.Fatalf("audioMix = %q", manifest.AudioMix)
	}
}

func TestBuildReusesPreviousFrameWhenPageMissing(t *testing.T) {
	reader := baseReader(t)
	input := buildInput()
	input.PagePNGKeys = []string{input.PagePNGKeys[0], ""}

	var buf bytes.Buffer
	manifest, err := NewBundleBuilder(reader, nil).Build(context.Background(), input, &buf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if manifest.Slides[1].Image != manifest.Slides[0].Image {
		t.Fatalf("page 2 image = %q, want reuse of %q", manifest.Slides[1].Image, manifest.Slides[0].Image)
	}
	if _, _, err := VerifyBundle(buf.Bytes()); err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}
}

func TestBuildUsesPlaceholderWhenFirstPageMissing(t *testing.T) {
	reader := baseReader(t)
	input := buildInput()
	input.PagePNGKeys = nil

	var buf bytes.Buffer
	manifest, err := NewBundleBuilder(reader, nil).Build(context.Background(), input, &buf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	_, files, err := VerifyBundle(buf.Bytes())
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}
	for i, slide := range manifest.Slides {
		if _, ok := files[slide.Image]; !ok {
			t.Fatalf("slide %d image %q missing", i, slide.Image)
		}
		if _, err := jpeg.DecodeConfig(bytes.NewReader(files[slide.Image])); err != nil {
			t.Fatalf("slide %d placeholder is not a jpeg: %v", i, err)
		}
	}
}

func TestBuildFallsBackToOriginalBytesWhenDecodeFails(t *testing.T) {
	reader := baseReader(t)
	reader.pages["tenants/t1/projects/p1/pages/1.png"] = []byte("not an image")

	var buf bytes.Buffer
	manifest, err := NewBundleBuilder(reader, nil).Build(context.Background(), buildInput(), &buf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.HasSuffix(manifest.Slides[0].Image, ".png") {
		t.Fatalf("fallen-back page image = %q, want .png", manifest.Slides[0].Image)
	}
	_, files, err := VerifyBundle(buf.Bytes())
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}
	if got := string(files[manifest.Slides[0].Image]); got != "not an image" {
		t.Fatalf("fallen-back bytes = %q", got)
	}
}

func TestBuildRejectsExternalResourceKeys(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*BundleInput)
	}{
		{"page url", func(in *BundleInput) { in.PagePNGKeys = []string{"https://evil.example.com/a.png"} }},
		{"page absolute", func(in *BundleInput) { in.PagePNGKeys = []string{"/etc/passwd"} }},
		{"page parent", func(in *BundleInput) { in.PagePNGKeys = []string{"../../etc/passwd"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := buildInput()
			tc.mutate(&input)
			var buf bytes.Buffer
			if _, err := NewBundleBuilder(baseReader(t), nil).Build(context.Background(), input, &buf); err == nil {
				t.Fatalf("Build accepted an unsafe resource key")
			}
		})
	}
}

func TestBuildRequiresSubtitlesAndAudio(t *testing.T) {
	t.Run("missing vtt key", func(t *testing.T) {
		input := buildInput()
		input.VTTKey = ""
		var buf bytes.Buffer
		if _, err := NewBundleBuilder(baseReader(t), nil).Build(context.Background(), input, &buf); err == nil {
			t.Fatal("Build accepted an empty vtt key")
		}
	})
	t.Run("missing audio", func(t *testing.T) {
		reader := baseReader(t)
		delete(reader.objects, "tenants/t1/projects/p1/audio/2.wav")
		var buf bytes.Buffer
		_, err := NewBundleBuilder(reader, nil).Build(context.Background(), buildInput(), &buf)
		if err == nil || !strings.Contains(err.Error(), "audio") {
			t.Fatalf("Build error = %v, want audio failure", err)
		}
	})
	t.Run("missing vtt object", func(t *testing.T) {
		reader := baseReader(t)
		delete(reader.objects, "tenants/t1/projects/p1/subs/cues.vtt")
		var buf bytes.Buffer
		if _, err := NewBundleBuilder(reader, nil).Build(context.Background(), buildInput(), &buf); err == nil {
			t.Fatal("Build accepted a missing vtt object")
		}
	})
}

func TestManifestValidate(t *testing.T) {
	base := func() *Manifest {
		m := newManifest(twoPageTimeline(), 1920, 1080, 30, "t", "", time.Time{})
		m.Slides = []SlideEntry{
			{SlideID: "slide-1", StartUS: 0, EndUS: 100_000, Image: "slides/001.jpg"},
			{SlideID: "slide-2", StartUS: 100_000, EndUS: 200_000, Image: "slides/002.jpg"},
		}
		m.Audio = []AudioEntry{{SegmentID: "seg-1", Src: "audio/001.wav", StartUS: 0, EndUS: 100_000}}
		return m
	}
	cases := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"gap between slides", func(m *Manifest) { m.Slides[1].StartUS = 120_000 }},
		{"slides exceed duration", func(m *Manifest) { m.Slides[1].EndUS = 400_000 }},
		{"external image", func(m *Manifest) { m.Slides[0].Image = "https://x/a.jpg" }},
		{"absolute image", func(m *Manifest) { m.Slides[0].Image = "/a.jpg" }},
		{"parent image", func(m *Manifest) { m.Slides[0].Image = "../a.jpg" }},
		{"no slides", func(m *Manifest) { m.Slides = nil }},
		{"bad video", func(m *Manifest) { m.Video.FPS = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.mutate(m)
			if err := m.Validate(nil); err == nil {
				t.Fatalf("Validate accepted invalid manifest")
			}
		})
	}
	if err := base().Validate(nil); err != nil {
		t.Fatalf("Validate rejected a valid manifest: %v", err)
	}
}

func TestFitLongEdge(t *testing.T) {
	cases := []struct{ w, h, limit, wantW, wantH int }{
		{1920, 1080, 1600, 1600, 900},
		{1280, 720, 1600, 1280, 720},
		{1080, 1920, 1600, 900, 1600},
		{100, 50, 1600, 100, 50},
	}
	for _, tc := range cases {
		gotW, gotH := fitLongEdge(tc.w, tc.h, tc.limit)
		if gotW != tc.wantW || gotH != tc.wantH {
			t.Fatalf("fitLongEdge(%d,%d,%d) = %d,%d want %d,%d", tc.w, tc.h, tc.limit, gotW, gotH, tc.wantW, tc.wantH)
		}
	}
}

func TestCountExternalRefs(t *testing.T) {
	files := map[string][]byte{
		"index.html": []byte(`<img src="slides/001.jpg"><script src="app.js"></script><img src="//cdn/x.png"><link href="https://cdn/x.css">`),
		"app.css":    []byte(`body{background:url("https://cdn/bg.png")} .x{background:url(data:image/png;base64,AA)}`),
		"app.js":     []byte(`var u = "https://example.com";`),
	}
	if got := CountExternalRefs(files); got != 3 {
		t.Fatalf("CountExternalRefs = %d, want 3 (string literals are not refs)", got)
	}
}

func TestGoldenBundleStructure(t *testing.T) {
	// golden 用例比对**结构**而非字节（zip 时间戳不稳定，字节级比对必然抖动）：
	// 条目集合、清单字段与时间关系是契约，压缩率与元数据不是。
	reader := baseReader(t)
	var buf bytes.Buffer
	manifest, err := NewBundleBuilder(reader, nil).Build(context.Background(), buildInput(), &buf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	_, files, err := VerifyBundle(buf.Bytes())
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}
	want := []string{
		"app.css", "app.js",
		"audio/001.wav", "audio/002.wav", "audio/mix.wav",
		"index.html", "manifest.js", "manifest.json",
		"slides/001.jpg", "slides/002.jpg",
		"subtitles.js", "subtitles.srt", "subtitles.vtt",
		"timeline.json",
	}
	got := keys(files)
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("bundle entries = %v, want %v", got, want)
	}
	if manifest.Video.Width != 1920 || manifest.Video.Height != 1080 || manifest.Video.FPS != 30 {
		t.Fatalf("video = %+v", manifest.Video)
	}
	if manifest.Title != "季度复盘 <script>alert(1)</script>" {
		t.Fatalf("manifest title must keep the raw value (escaping is a rendering concern): %q", manifest.Title)
	}
	if len(manifest.Audio) != 2 || manifest.Audio[1].StartUS != 100_000 {
		t.Fatalf("audio entries = %+v", manifest.Audio)
	}
	if manifest.Subtitles.Src != FileSubtitlesVTT || manifest.Subtitles.Inline != FileSubtitlesJS {
		t.Fatalf("subtitles = %+v", manifest.Subtitles)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
