package share

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/media"
)

// shotsTimeline 是单页 4s 的时间轴：页内两镜各 2s，够放 120ms 叠化。
func shotsTimeline() *media.Timeline {
	return &media.Timeline{
		SchemaVersion: media.TimelineSchemaVersion,
		DurationUS:    4_000_000,
		Slides: []media.SlideCue{{
			SlideID: "slide-1", StartUS: 0, EndUS: 4_000_000,
			Segments: []media.SegmentCue{{
				SegmentID: "seg-1", AudioKey: "tenants/t1/projects/p1/audio/1.wav",
				StartUS: 0, EndUS: 4_000_000,
			}},
		}},
		Subtitles: []media.SubtitleCue{{
			SlideID: "slide-1", SegmentID: "seg-1", StartUS: 0, EndUS: 4_000_000,
			Text: "第一页。",
		}},
	}
}

func shotsReader(t *testing.T) *mapReader {
	t.Helper()
	return &mapReader{
		pages: map[string][]byte{
			"tenants/t1/projects/p1/pages/1.png": pngBytes(t, 1920, 1080),
		},
		objects: map[string][]byte{
			"tenants/t1/projects/p1/audio/1.wav":   pcm16WAV(96_000), // 24kHz × 4s
			"tenants/t1/projects/p1/subs/cues.vtt": []byte("WEBVTT\n\n00:00:00.000 --> 00:00:04.000\n第一页\n"),
			"tenants/t1/projects/p1/subs/cues.srt": []byte("1\r\n00:00:00,000 --> 00:00:04,000\r\n第一页\r\n"),
		},
	}
}

func shotsInput(t *testing.T) BundleInput {
	t.Helper()
	in := buildInput()
	in.Timeline = shotsTimeline()
	in.PagePNGKeys = []string{"tenants/t1/projects/p1/pages/1.png"}
	in.Pages = []PageAnimation{{
		Shots: []ShotAsset{
			// 首镜不填 PNG：它就是页级图，打包器复用那一份字节。
			{StartMs: 0, DurationMs: 2000},
			{
				PNG:         pngBytes(t, 800, 450),
				Crop:        &SlideBox{X: 100, Y: 50, W: 800, H: 450}, // 16:9，与图一致
				StartMs:     2000,
				DurationMs:  2000,
				CrossFadeMs: 120,
			},
		},
	}}
	return in
}

func TestBuildWritesShots(t *testing.T) {
	var buf bytes.Buffer
	manifest, err := NewBundleBuilder(shotsReader(t), nil).Build(context.Background(), shotsInput(t), &buf)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	parsed, files, err := VerifyBundle(buf.Bytes())
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}

	shots := parsed.Slides[0].Shots
	if len(shots) != 2 {
		t.Fatalf("shots = %+v, want 2", shots)
	}
	// 首镜必须复用页级图：另存一份会让同一张画面在包里出现两次，且两份可能不同步。
	if shots[0].Image != parsed.Slides[0].Image {
		t.Fatalf("shot 0 image %q != slide image %q", shots[0].Image, parsed.Slides[0].Image)
	}
	if _, ok := files["slides/001-shot-00.png"]; ok {
		t.Fatal("shot 0 must not be written as a separate file")
	}
	// 第 2 镜必须是自己的文件，且真的在包里（清单校验会核对 files 集合）。
	if shots[1].Image != "slides/001-shot-01.png" {
		t.Fatalf("shot 1 image = %q", shots[1].Image)
	}
	if _, ok := files[shots[1].Image]; !ok {
		t.Fatalf("shot 1 file %q missing from bundle", shots[1].Image)
	}
	if shots[1].CrossFadeMs != 120 || shots[1].StartMs != 2000 {
		t.Fatalf("shot 1 timing = %+v", shots[1])
	}
	if manifest.Slides[0].Shots[1].Crop == nil {
		t.Fatal("crop was dropped from the manifest")
	}
}

func TestBuildRejectsCropAspectMismatch(t *testing.T) {
	// 800×800 的图配 800×450 的裁切框：比例差了 78%，画到舞台上会被横向拉伸。
	// 这种错误不会报错也不会崩，只会让元素「看着有点扁」——只能在这里拦。
	in := shotsInput(t)
	in.Pages[0].Shots[1].PNG = pngBytes(t, 800, 800)
	var buf bytes.Buffer
	_, err := NewBundleBuilder(shotsReader(t), nil).Build(context.Background(), in, &buf)
	if err == nil || !strings.Contains(err.Error(), "within 1%") {
		t.Fatalf("want crop aspect error, got %v", err)
	}
}

func TestBuildRejectsShotWithoutImage(t *testing.T) {
	// 第 2 镜没给字节：若静默跳过，清单里会指向一个不存在的文件，
	// 而播放器在那一刻只会白一下。
	in := shotsInput(t)
	in.Pages[0].Shots[1].PNG = nil
	var buf bytes.Buffer
	_, err := NewBundleBuilder(shotsReader(t), nil).Build(context.Background(), in, &buf)
	if err == nil || !strings.Contains(err.Error(), "no image bytes") {
		t.Fatalf("want missing image error, got %v", err)
	}
}
