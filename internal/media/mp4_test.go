package media

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func solidPNG(w, h int, c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Pix[i*4], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3] = c.R, c.G, c.B, c.A
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func TestMP4EncodeTwoPages(t *testing.T) {
	e, err := NewMP4Encoder()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	out := filepath.Join(t.TempDir(), "out.mp4")
	opts := MP4EncodeOptions{
		OutPath: out,
		FPS:     1,
		Width:   320,
		Height:  180,
		PagePNGs: [][]byte{
			solidPNG(320, 180, color.RGBA{R: 200, G: 40, B: 40, A: 255}),
			solidPNG(320, 180, color.RGBA{R: 40, G: 40, B: 200, A: 255}),
		},
	}
	res, err := e.Encode(context.Background(), opts)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if res.VideoCodec != "h264" || res.AudioCodec != "aac" {
		t.Fatalf("codecs: video=%s audio=%s", res.VideoCodec, res.AudioCodec)
	}
	if res.Width != 320 || res.Height != 180 {
		t.Fatalf("resolution: %dx%d", res.Width, res.Height)
	}
	if res.Duration < 1.9 || res.Duration > 2.6 {
		t.Fatalf("duration: %v (want ~2s)", res.Duration)
	}
}

func TestMP4EncodeSinglePageKeepsAspect(t *testing.T) {
	e, err := NewMP4Encoder()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	out := filepath.Join(t.TempDir(), "out.mp4")
	// 竖长页面放入 16:9 目标框，等比适配留边不拉伸。
	res, err := e.Encode(context.Background(), MP4EncodeOptions{
		OutPath:  out,
		FPS:      1,
		Width:    320,
		Height:   180,
		PagePNGs: [][]byte{solidPNG(180, 320, color.RGBA{G: 200, A: 255})},
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if res.VideoCodec != "h264" || res.Width != 320 || res.Height != 180 {
		t.Fatalf("result: %+v", res)
	}
}

func TestMP4EncodeUsesTimelinePageDurationsAndAudio(t *testing.T) {
	e, err := NewMP4Encoder()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	timeline, err := BuildTimeline([]SlideInput{
		{SlideID: "slide-1", Segments: []SegmentInput{{SegmentID: "seg-1", DisplayText: "one", AudioKey: "a1", DurationMS: 700}}},
		{SlideID: "slide-2", Segments: []SegmentInput{{SegmentID: "seg-2", DisplayText: "two", AudioKey: "a2", DurationMS: 300}}},
	}, Timing{LeadInMS: 100, GapMS: 100, TailHoldMS: 300})
	if err != nil {
		t.Fatal(err)
	}
	audio, err := AssembleTimelineWAV(timeline, map[string][]byte{
		"a1": pcmWAVForTest(1000, make([]int16, 700)),
		"a2": pcmWAVForTest(1000, make([]int16, 300)),
	})
	if err != nil {
		t.Fatalf("AssembleTimelineWAV: %v", err)
	}
	out := filepath.Join(t.TempDir(), "timeline.mp4")
	res, err := e.Encode(context.Background(), MP4EncodeOptions{
		OutPath: out, FPS: 20, Width: 320, Height: 180,
		PagePNGs: [][]byte{
			solidPNG(320, 180, color.RGBA{R: 220, G: 20, B: 20, A: 255}),
			solidPNG(320, 180, color.RGBA{R: 20, G: 20, B: 220, A: 255}),
		},
		PageDurationsMS: []int64{
			(timeline.Slides[0].EndUS - timeline.Slides[0].StartUS) / 1000,
			(timeline.Slides[1].EndUS - timeline.Slides[1].StartUS) / 1000,
		},
		AudioWAV: audio,
	})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if res.Duration < 1.95 || res.Duration > 2.05 {
		t.Fatalf("duration = %v", res.Duration)
	}
	assertFrameColorAt(t, e, out, 500_000, true)
	assertFrameColorAt(t, e, out, 1_500_000, false)
}

func assertFrameColorAt(t *testing.T, encoder *MP4Encoder, video string, positionUS int64, red bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "frame.png")
	position := strconv.FormatFloat(float64(positionUS)/1_000_000, 'f', 6, 64)
	if err := extractFrame(context.Background(), encoder.ffmpeg, video, position, path); err != nil {
		t.Fatalf("extractFrame: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	r, _, b, _ := img.At(img.Bounds().Dx()/2, img.Bounds().Dy()/2).RGBA()
	if red && r <= b {
		t.Fatalf("frame at %dus is not red: r=%d b=%d", positionUS, r, b)
	}
	if !red && b <= r {
		t.Fatalf("frame at %dus is not blue: r=%d b=%d", positionUS, r, b)
	}
}
