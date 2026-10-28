package sidecar

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/share"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// testWAV 生成 24kHz 单声道 PCM16 静音，frames 决定时长（2400 frames = 100ms）。
func testWAV(frames int) []byte {
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

func writeFile(t *testing.T, root, name string, data []byte) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// localProject 造一份可离线打包的素材目录，返回 root 与请求。
func localProject(t *testing.T) (string, ExportBundleRequest) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "pages/1.png", testPNG(t, 640, 360))
	writeFile(t, root, "pages/2.png", testPNG(t, 640, 360))
	writeFile(t, root, "audio/1.wav", testWAV(2400))
	writeFile(t, root, "audio/2.wav", testWAV(2400))
	writeFile(t, root, "subs/cues.vtt", []byte("WEBVTT\n\n00:00:00.000 --> 00:00:05.000\n第一页\n"))
	writeFile(t, root, "subs/cues.srt", []byte("1\r\n00:00:00,000 --> 00:00:05,000\r\n第一页\r\n"))

	timeline := media.Timeline{
		SchemaVersion: media.TimelineSchemaVersion,
		DurationUS:    200_000,
		Slides: []media.SlideCue{
			{
				SlideID: "slide-1", StartUS: 0, EndUS: 100_000,
				Segments: []media.SegmentCue{{SegmentID: "seg-1", AudioKey: "audio/1.wav", StartUS: 0, EndUS: 100_000}},
			},
			{
				SlideID: "slide-2", StartUS: 100_000, EndUS: 200_000,
				Segments: []media.SegmentCue{{SegmentID: "seg-2", AudioKey: "audio/2.wav", StartUS: 100_000, EndUS: 200_000}},
			},
		},
		Subtitles: []media.SubtitleCue{
			{SlideID: "slide-1", SegmentID: "seg-1", StartUS: 0, EndUS: 100_000, Text: "第一页。"},
			{SlideID: "slide-2", SegmentID: "seg-2", StartUS: 100_000, EndUS: 200_000, Text: "第二页。"},
		},
	}
	raw, err := json.Marshal(timeline)
	if err != nil {
		t.Fatalf("marshal timeline: %v", err)
	}
	writeFile(t, root, "timeline.json", raw)

	return root, ExportBundleRequest{
		Root:        root,
		OutDir:      filepath.Join(t.TempDir(), "bundle"),
		VTTKey:      "subs/cues.vtt",
		SRTKey:      "subs/cues.srt",
		PagePNGKeys: []string{"pages/1.png", "pages/2.png"},
		Title:       "隐私模式示例",
		Width:       1920,
		Height:      1080,
		FPS:         30,
	}
}

func callExport(t *testing.T, req ExportBundleRequest) ExportBundleResult {
	t.Helper()
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	resp, err := NewEngine().HandleJSON(context.Background(), reqJSON("e1", MethodExportBundle, string(payload)))
	if err != nil {
		t.Fatalf("HandleJSON: %v", err)
	}
	var parsed struct {
		Result ExportBundleResult `json:"result"`
		Error  *Error             `json:"error"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if parsed.Error != nil {
		t.Fatalf("export_bundle failed: %s (%s)", parsed.Error.Message, parsed.Error.Code)
	}
	return parsed.Result
}

func TestExportBundleWritesPlayableDirectory(t *testing.T) {
	_, req := localProject(t)
	result := callExport(t, req)

	if result.OutDir != req.OutDir {
		t.Fatalf("outDir = %q, want %q", result.OutDir, req.OutDir)
	}
	if result.Entry != share.FileIndexHTML {
		t.Fatalf("entry = %q", result.Entry)
	}
	if result.Slides != 2 || result.AudioSegments != 2 || !result.AudioMix {
		t.Fatalf("unexpected summary: %+v", result)
	}
	if result.DurationUS != 200_000 {
		t.Fatalf("durationUs = %d", result.DurationUS)
	}
	for _, name := range []string{share.FileIndexHTML, share.FileManifestJSON, "slides/001.jpg", "audio/mix.wav", share.FileAppJS} {
		if _, err := os.Stat(filepath.Join(req.OutDir, filepath.FromSlash(name))); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if result.Bytes <= 0 {
		t.Fatalf("bytes = %d, want positive", result.Bytes)
	}
}

func TestExportBundleRejectsNonAbsolutePaths(t *testing.T) {
	root, req := localProject(t)
	req.Root = filepath.Base(root) // 相对路径依赖 cwd，必须拒绝
	resp, _ := NewEngine().HandleJSON(context.Background(), reqJSON("e1", MethodExportBundle, mustJSON(t, req)))
	var parsed struct {
		Error *Error `json:"error"`
	}
	_ = json.Unmarshal(resp, &parsed)
	if parsed.Error == nil || parsed.Error.Code != ErrCodeInvalidRequest {
		t.Fatalf("relative root must be INVALID_REQUEST, got %s", resp)
	}
}

func TestExportBundleRejectsNetworkKey(t *testing.T) {
	_, req := localProject(t)
	req.PagePNGKeys = []string{"https://example.com/page.png"}
	resp, _ := NewEngine().HandleJSON(context.Background(), reqJSON("e1", MethodExportBundle, mustJSON(t, req)))
	var parsed struct {
		Error *Error `json:"error"`
	}
	_ = json.Unmarshal(resp, &parsed)
	if parsed.Error == nil {
		t.Fatalf("external key must fail, got %s", resp)
	}
	if !strings.Contains(parsed.Error.Message, "external reference") {
		t.Fatalf("unexpected error: %s", parsed.Error.Message)
	}
}

func TestExportBundleRejectsNonEmptyOutDir(t *testing.T) {
	_, req := localProject(t)
	if err := os.MkdirAll(req.OutDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, req.OutDir, "leftover.txt", []byte("x"))
	resp, _ := NewEngine().HandleJSON(context.Background(), reqJSON("e1", MethodExportBundle, mustJSON(t, req)))
	var parsed struct {
		Error *Error `json:"error"`
	}
	_ = json.Unmarshal(resp, &parsed)
	if parsed.Error == nil || parsed.Error.Code != ErrCodeInvalidRequest {
		t.Fatalf("non-empty outDir must be INVALID_REQUEST, got %s", resp)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}
