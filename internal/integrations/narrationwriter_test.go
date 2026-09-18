package integrations

import (
	"bytes"
	"context"
	"testing"
	"time"

	pptx "github.com/F31/go-pptx/v2/pptx"

	"github.com/F31/ppts/internal/project"
)

// minimalWAV 生成一段最小合法 WAV（1 秒，8000Hz，8bit 单声道），满足 go-pptx probe。
func minimalWAV(seconds int) []byte {
	const hdr = 44
	samples := 8000 * seconds
	buf := make([]byte, hdr+samples)
	h := buf[:hdr]
	// RIFF/WAVE 头
	copy(h[0:4], "RIFF")
	le32(h[4:], uint32(hdr-8+samples))
	copy(h[8:12], "WAVE")
	copy(h[12:16], "fmt ")
	le32(h[16:], 16)   // fmt chunk size
	le16(h[20:], 1)    // PCM
	le16(h[22:], 1)    // mono
	le32(h[24:], 8000) // sample rate
	le32(h[28:], 8000) // byte rate
	le16(h[32:], 1)    // block align
	le16(h[34:], 8)    // bits per sample
	copy(h[36:40], "data")
	le32(h[40:], uint32(samples))
	for i := 0; i < samples; i++ {
		buf[hdr+i] = byte(128 + (i/50)%40) // 简单波形
	}
	return buf
}

func le16(b []byte, v uint16) {
	b[0], b[1] = byte(v), byte(v>>8)
}

func le32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

// buildSourceDeck 用 go-pptx 生成带文本与备注的源副本。
func buildSourceDeck(t *testing.T) *bytes.Reader {
	t.Helper()
	p, err := pptx.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()
	layouts, err := p.Layouts()
	if err != nil || len(layouts) == 0 {
		t.Fatalf("Layouts: %v", err)
	}
	slide, err := p.AddSlide(layouts[0])
	if err != nil {
		t.Fatalf("AddSlide: %v", err)
	}
	if _, err := slide.AddTextBox(pptx.TextBoxSpec{
		X: 914400, Y: 914400, Width: 6000000, Height: 914400,
		Text: "保持原文",
	}); err != nil {
		t.Fatalf("AddTextBox: %v", err)
	}
	var buf bytes.Buffer
	if _, err := p.Write(context.Background(), &buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestNarrationWriterApplyRoundTrip(t *testing.T) {
	src := buildSourceDeck(t)
	w := NewGoPPTXNarrationWriter()
	audio := minimalWAV(1)
	plan := NarrationPlan{Slides: []SlideNarration{{
		Index: 0, TrackKey: "track-page-0", Audio: audio, MIME: "audio/wav",
		Advance: 3 * time.Second,
	}}}
	rep, err := w.Apply(context.Background(), src, int64(src.Len()), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Output) == 0 {
		t.Fatalf("Apply produced empty output")
	}
	if len(rep.TrackKeys) != 1 || rep.TrackKeys[0] != "track-page-0" {
		t.Fatalf("track keys: %v", rep.TrackKeys)
	}
	if len(rep.ChangedSlides) != 1 || rep.ChangedSlides[0] != 0 {
		t.Fatalf("changed slides: %v", rep.ChangedSlides)
	}

	// 回读：仍可被读适配器解析，文本保留，Validate 零错误。
	out := bytes.NewReader(rep.Output)
	reader := project.NewGoPPTXReader(project.Limits{})
	doc, err := reader.Inspect(context.Background(), out, int64(out.Len()))
	if err != nil {
		t.Fatalf("Inspect after narration: %v", err)
	}
	if len(doc.Pages) != 1 {
		t.Fatalf("pages after narration: %d", len(doc.Pages))
	}
	var textFound bool
	for _, sh := range doc.Pages[0].Shapes {
		if sh.Text == "保持原文" {
			textFound = true
		}
	}
	if !textFound {
		t.Fatalf("original text lost after narration write")
	}

	// go-pptx Validate 直接复验。
	p, err := pptx.OpenReader(bytes.NewReader(rep.Output), int64(len(rep.Output)))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer p.Close()
	if report := p.Validate(context.Background()); report.HasErrors() {
		t.Fatalf("validate after narration: diagnostics=%v", report.Diagnostics)
	}
}

func TestNarrationWriterIdempotentTrackKey(t *testing.T) {
	src := buildSourceDeck(t)
	w := NewGoPPTXNarrationWriter()
	audio := minimalWAV(1)
	plan := NarrationPlan{Slides: []SlideNarration{{
		Index: 0, TrackKey: "track-page-0", Audio: audio, MIME: "audio/wav",
	}}}
	ctx := context.Background()
	rep1, err := w.Apply(ctx, src, int64(src.Len()), plan)
	if err != nil {
		t.Fatalf("Apply#1: %v", err)
	}
	// 同一 TrackKey 重跑：幂等，不产生重复音轨，输出仍可校验。
	rep2, err := w.Apply(ctx, bytes.NewReader(rep1.Output), int64(len(rep1.Output)), plan)
	if err != nil {
		t.Fatalf("Apply#2 (same TrackKey): %v", err)
	}
	p, err := pptx.OpenReader(bytes.NewReader(rep2.Output), int64(len(rep2.Output)))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer p.Close()
	if report := p.Validate(ctx); report.HasErrors() {
		t.Fatalf("validate after re-apply: diagnostics=%v", report.Diagnostics)
	}
}

func TestNarrationWriterRejectsOutOfRangeSlide(t *testing.T) {
	src := buildSourceDeck(t)
	w := NewGoPPTXNarrationWriter()
	plan := NarrationPlan{Slides: []SlideNarration{{
		Index: 5, TrackKey: "k", Audio: minimalWAV(1), MIME: "audio/wav",
	}}}
	if _, err := w.Apply(context.Background(), src, int64(src.Len()), plan); err == nil {
		t.Fatalf("out-of-range slide should error")
	}
}
