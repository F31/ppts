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

// TestRenderASSSplitsLinesAndKaraoke 守护烧录字幕的逐行轮换 + 朗读高亮：
// 每个 cue 的文本按换行/长度切成多个 Dialogue 事件（一行一个），行内用 \k 卡拉OK 标签
// 让已朗读部分变色（与网页播放器一致）；ASS 特殊字符需转义。
func TestRenderASSSplitsLinesAndKaraoke(t *testing.T) {
	text := "第一行内容测试\n第二行内容测试\n第三行内容测试"
	runes := []rune(text)
	chars := make([]CharCue, len(runes))
	for i, r := range runes {
		start := int64(200_000 + i*100_000)
		chars[i] = CharCue{StartUS: start, EndUS: start + 100_000, Char: string(r)}
	}
	cues := []SubtitleCue{{
		SlideID: "s1", SegmentID: "a",
		StartUS: 200_000, EndUS: 200_000 + int64(len(runes))*100_000,
		Text: text, Chars: chars,
	}}
	ass, err := RenderASS(cues, ASSOptions{PlayResX: 1920, PlayResY: 1080})
	if err != nil {
		t.Fatalf("RenderASS: %v", err)
	}
	s := string(ass)
	parts := strings.Split(s, "Dialogue: ")
	if got := len(parts) - 1; got != 3 {
		t.Fatalf("dialogue lines = %d, want 3\n%s", got, s)
	}
	if !strings.Contains(parts[1], "{\\k") {
		t.Fatalf("missing karaoke tags: %q", parts[1])
	}
	// 去掉行内 \k 卡拉OK标签后应为该行纯文本（每字符 100ms → \k10）。
	firstLine := strings.ReplaceAll(parts[1], "{\\k10}", "")
	if !strings.Contains(firstLine, "第一行内容测试") || strings.Contains(firstLine, "第二行") {
		t.Fatalf("first dialogue should contain only its own line: %q", firstLine)
	}

	escaped, err := RenderASS([]SubtitleCue{{StartUS: 0, EndUS: 1_000_000, Text: "a{b}c\\d"}}, ASSOptions{})
	if err != nil {
		t.Fatalf("RenderASS escape: %v", err)
	}
	// 去掉卡拉OK标签后校验转义：花括号与反斜杠必须被转义。
	escapedText := strings.ReplaceAll(string(escaped), "{\\k14}", "")
	if !strings.Contains(escapedText, "a\\{b\\}c\\\\d") {
		t.Fatalf("ASS special chars not escaped: %q", escapedText)
	}
}

// TestRenderASSUsesTimelineAlignmentChars 守护"网页高亮与 MP4 烧录同源"：
// BuildTimeline 把 AlignProvider/estimated/estimated_vad/forced 的 tokens 转成 SubtitleCue.Chars；
// 播放器（前端）与 RenderASS（MP4 烧录）消费的是同一份 timeline 数据。这里验证 RenderASS
// 直接从 timeline.Subtitles 生成，字符内容与时间轴一致，不依赖任何独立的时间轴来源。
func TestRenderASSUsesTimelineAlignmentChars(t *testing.T) {
	text := "甲乙丙。"
	runes := []rune(text)
	tokens := make([]tts.TokenOffset, len(runes))
	for i, r := range runes {
		start := int64(200_000 + i*200_000)
		tokens[i] = tts.TokenOffset{StartUS: start, EndUS: start + 200_000, Char: string(r)}
	}
	timeline, err := BuildTimeline([]SlideInput{{
		SlideID: "s1",
		Segments: []SegmentInput{{
			SegmentID: "seg-1", DisplayText: text, AudioKey: "k.wav", DurationMS: 1000,
			Alignment: &tts.Alignment{Text: text, Method: tts.AlignEstimateVAD, Tokens: tokens},
		}},
	}}, Timing{})
	if err != nil {
		t.Fatalf("BuildTimeline: %v", err)
	}
	cue := timeline.Subtitles[0]
	if len(cue.Chars) != len(runes) {
		t.Fatalf("cue.Chars = %d, want %d", len(cue.Chars), len(runes))
	}
	ass, err := RenderASS(timeline.Subtitles, ASSOptions{})
	if err != nil {
		t.Fatalf("RenderASS: %v", err)
	}
	s := string(ass)
	if !strings.Contains(s, "Dialogue:") || !strings.Contains(s, "甲") || !strings.Contains(s, "丙") {
		t.Fatalf("RenderASS 未消费 timeline 的字符时间戳: %s", s)
	}
	// 段落的 AlignmentMethod 也随 timeline 落盘，便于前端/排查区分来源。
	if timeline.Slides[0].Segments[0].AlignmentMethod != tts.AlignEstimateVAD {
		t.Fatalf("segment method = %s", timeline.Slides[0].Segments[0].AlignmentMethod)
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
