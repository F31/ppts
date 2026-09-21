package media

import (
	"strconv"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/integrations/tts"
)

func TestBuildTimelineUsesAlignmentAndPreservesSlideOrder(t *testing.T) {
	timeline, err := BuildTimeline([]SlideInput{
		{
			SlideID: "slide-2",
			Segments: []SegmentInput{
				{
					SegmentID: "seg-2-1", DisplayText: "A < B", DurationMS: 1000,
					AudioKey: "audio-1",
					Alignment: &tts.Alignment{Method: tts.AlignProvider, Tokens: []tts.TokenOffset{
						{StartUS: 200_000, EndUS: 400_000, Char: "A"},
						{StartUS: 500_000, EndUS: 800_000, Char: "B"},
					}},
				},
				{SegmentID: "seg-2-2", DisplayText: "第二段", DurationMS: 500, AudioKey: "audio-2"},
			},
		},
		{
			SlideID:  "slide-1",
			Segments: []SegmentInput{{SegmentID: "seg-1-1", DisplayText: "后一页", DurationMS: 1000}},
		},
	}, Timing{LeadInMS: 100, GapMS: 200, TailHoldMS: 300})
	if err != nil {
		t.Fatalf("BuildTimeline: %v", err)
	}
	if timeline.DurationUS != 3_900_000 {
		t.Fatalf("duration = %d", timeline.DurationUS)
	}
	if timeline.Slides[0].SlideID != "slide-2" || timeline.Slides[0].StartUS != 0 || timeline.Slides[0].EndUS != 2_300_000 {
		t.Fatalf("first slide = %+v", timeline.Slides[0])
	}
	if timeline.Slides[1].SlideID != "slide-1" || timeline.Slides[1].StartUS != 2_300_000 {
		t.Fatalf("second slide = %+v", timeline.Slides[1])
	}
	if got := timeline.Subtitles[0]; got.StartUS != 300_000 || got.EndUS != 900_000 {
		t.Fatalf("aligned subtitle = %+v", got)
	} else if len(got.Chars) != 2 || got.Chars[0].Char != "A" || got.Chars[0].StartUS != 300_000 || got.Chars[1].EndUS != 900_000 {
		t.Fatalf("subtitle chars = %+v", got.Chars)
	}
	if timeline.Subtitles[1].Chars != nil {
		t.Fatalf("segment without alignment must not emit chars")
	}
	if got := timeline.Subtitles[1]; got.StartUS != 1_300_000 || got.EndUS != 1_800_000 {
		t.Fatalf("fallback subtitle = %+v", got)
	}
}

func TestTimelineLongDurationHasNoAccumulatedDrift(t *testing.T) {
	const pages = 1800
	slides := make([]SlideInput, 0, pages)
	for i := 0; i < pages; i++ {
		slides = append(slides, SlideInput{
			SlideID: "slide-" + strconv.Itoa(i),
			Segments: []SegmentInput{{
				SegmentID: "segment-" + strconv.Itoa(i), DisplayText: "x", DurationMS: 1000,
			}},
		})
	}
	timeline, err := BuildTimeline(slides, Timing{})
	if err != nil {
		t.Fatalf("BuildTimeline: %v", err)
	}
	if timeline.DurationUS != int64(pages)*1_000_000 {
		t.Fatalf("duration = %d", timeline.DurationUS)
	}
	last := timeline.Slides[len(timeline.Slides)-1]
	if last.EndUS != timeline.DurationUS {
		t.Fatalf("last end=%d total=%d", last.EndUS, timeline.DurationUS)
	}
}

func TestTimelineRejectsInvalidAlignmentAndDuplicateIDs(t *testing.T) {
	_, err := BuildTimeline([]SlideInput{{
		SlideID: "slide-1",
		Segments: []SegmentInput{{
			SegmentID: "seg-1", DisplayText: "x", DurationMS: 100,
			Alignment: &tts.Alignment{Tokens: []tts.TokenOffset{{StartUS: 0, EndUS: 101_000}}},
		}},
	}}, Timing{})
	if err == nil {
		t.Fatal("expected invalid alignment error")
	}

	_, err = BuildTimeline([]SlideInput{
		{SlideID: "slide-1", Segments: []SegmentInput{{SegmentID: "same", DisplayText: "x", DurationMS: 1}}},
		{SlideID: "slide-2", Segments: []SegmentInput{{SegmentID: "same", DisplayText: "y", DurationMS: 1}}},
	}, Timing{})
	if err == nil {
		t.Fatal("expected duplicate segment error")
	}
}

func TestRenderSRTAndWebVTTUseSameCues(t *testing.T) {
	cues := []SubtitleCue{
		{SlideID: "s1", SegmentID: "a", StartUS: 300_000, EndUS: 900_000, Text: "A < B"},
		{SlideID: "s1", SegmentID: "b", StartUS: 1_300_000, EndUS: 1_800_000, Text: "第二段"},
	}
	srt, err := RenderSRT(cues)
	if err != nil {
		t.Fatalf("RenderSRT: %v", err)
	}
	if !strings.Contains(string(srt), "00:00:00,300 --> 00:00:00,900\r\nA < B") {
		t.Fatalf("srt = %q", srt)
	}
	vtt, err := RenderWebVTT(cues)
	if err != nil {
		t.Fatalf("RenderWebVTT: %v", err)
	}
	if !strings.HasPrefix(string(vtt), "WEBVTT\n\n") || !strings.Contains(string(vtt), "00:00:00.300 --> 00:00:00.900\nA &lt; B") {
		t.Fatalf("vtt = %q", vtt)
	}
}

func TestSubtitleRendererRejectsOverlap(t *testing.T) {
	_, err := RenderWebVTT([]SubtitleCue{
		{StartUS: 0, EndUS: 2_000, Text: "a"},
		{StartUS: 1_000, EndUS: 3_000, Text: "b"},
	})
	if err == nil {
		t.Fatal("expected overlap error")
	}
}
