package media

import (
	"strings"
	"testing"
)

// 水印走 ASS（ADR-029 ②）。三条判据：
//   1. 有文案时产出一条覆盖到片尾的 Watermark Dialogue；
//   2. 没有时长就不画（ASS 没有"到片尾"的写法，写死猜测值会让长片上水印提前消失）；
//   3. 水印与字幕可以共存——免费层最常见的组合就是"有字幕 + 有水印"。

func TestRenderASSWatermarkDialogue(t *testing.T) {
	ass, err := RenderASS(nil, ASSOptions{
		PlayResX: 1280, PlayResY: 720, Watermark: "由智讲PPT生成", DurationUS: 185_500_000,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := string(ass)
	if !strings.Contains(out, "Style: Watermark,") {
		t.Fatalf("缺少水印样式：\n%s", out)
	}
	// 对齐方式 9 = 右上角；与播放器 .ppts-watermark 的位置一致。
	if !strings.Contains(out, ",0,0,1,1.5,0,9,") {
		t.Fatalf("水印样式不是右上角对齐：\n%s", out)
	}
	if !strings.Contains(out, "Dialogue: 0,0:00:00.00,0:03:05.50,Watermark,,0,0,0,,由智讲PPT生成") {
		t.Fatalf("水印对白缺失或时长不对：\n%s", out)
	}
	if strings.Contains(out, "Style: Watermark,") && !strings.Contains(out, "PlayResX: 1280") {
		t.Fatal("脚本分辨率没跟上画布")
	}
}

func TestRenderASSWatermarkNeedsDuration(t *testing.T) {
	// 没有时长：宁可报错也不画一条寿命未知的水印。
	if _, err := RenderASS(nil, ASSOptions{Watermark: "由智讲PPT生成"}); err == nil {
		t.Fatal("缺时长的水印应当报错")
	}
}

func TestRenderASSWatermarkCoexistsWithCues(t *testing.T) {
	cues := []SubtitleCue{{StartUS: 0, EndUS: 2_000_000, Text: "第一句"}}
	ass, err := RenderASS(cues, ASSOptions{Watermark: "由智讲PPT生成", DurationUS: 4_000_000})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := string(ass)
	// 字幕是 Default 样式，水印是 Watermark 样式，两条都在。
	// 字幕文本按字拆开加了 \k 标签，所以只能断言样式段，不能断言整句连续出现。
	if !strings.Contains(out, ",Default,,0,0,0,,") || !strings.Contains(out, "第") {
		t.Fatalf("字幕对白丢失：\n%s", out)
	}
	if !strings.Contains(out, ",Watermark,,0,0,0,,由智讲PPT生成") {
		t.Fatalf("水印对白丢失：\n%s", out)
	}
	// 水印必须是最后一条：libass 按行顺序叠，水印在后面才盖在字幕上方的空白区，
	// 而且不会因为字幕后写入而被覆盖。
	if strings.LastIndex(out, "Watermark,,0,0,0,,") < strings.LastIndex(out, "Default,,0,0,0,,") {
		t.Fatal("水印对白应排在字幕之后")
	}
}

// 没有水印时输出必须与以前逐字节一致：存量成片与字幕文件不该因为这次改动而变化。
func TestRenderASSWithoutWatermarkIsUnchanged(t *testing.T) {
	cues := []SubtitleCue{{StartUS: 0, EndUS: 1_000_000, Text: "一句"}}
	ass, err := RenderASS(cues, ASSOptions{PlayResX: 1920, PlayResY: 1080, FontSize: 48})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := string(ass)
	if strings.Contains(out, "Watermark") {
		t.Fatalf("未配置水印却写出了 Watermark 样式：\n%s", out)
	}
}

// 方案 A：SRT/VTT 不能继续一条 cue 覆盖整段 50 秒。逐行 cue 的行首/行尾必须
// 复用时间轴里的 Chars，而不是重新估算。
func TestSplitSubtitleCuesUsesCharBoundaries(t *testing.T) {
	text := "第一行内容测试内容。第二行内容测试内容。第三行内容测试内容。第四行内容测试内容。"
	runes := []rune(text)
	chars := make([]CharCue, len(runes))
	for i, r := range runes {
		start := int64(200_000 + i*100_000)
		chars[i] = CharCue{StartUS: start, EndUS: start + 100_000, Char: string(r)}
	}
	cue := SubtitleCue{
		SlideID: "s1", SegmentID: "seg-1",
		StartUS: chars[0].StartUS, EndUS: chars[len(chars)-1].EndUS,
		Text: text, Chars: chars,
	}
	lines := SplitSubtitleCues([]SubtitleCue{cue})
	if len(lines) < 2 {
		t.Fatalf("lines = %d, want at least 2", len(lines))
	}
	var joined strings.Builder
	var previousEnd int64
	for i, line := range lines {
		if strings.TrimSpace(line.Text) == "" {
			t.Fatalf("line %d has empty text", i)
		}
		if len(line.Chars) != len([]rune(line.Text)) {
			t.Fatalf("line %d chars = %d, text runes = %d", i, len(line.Chars), len([]rune(line.Text)))
		}
		if line.StartUS < previousEnd || line.EndUS <= line.StartUS {
			t.Fatalf("line %d times = %d..%d, previous end %d", i, line.StartUS, line.EndUS, previousEnd)
		}
		previousEnd = line.EndUS
		joined.WriteString(line.Text)
	}
	if joined.String() != text {
		t.Fatalf("joined = %q, want %q", joined.String(), text)
	}
	if lines[0].StartUS != chars[0].StartUS || lines[len(lines)-1].EndUS != chars[len(chars)-1].EndUS {
		t.Fatalf("line boundaries lost: first=%d last=%d", lines[0].StartUS, lines[len(lines)-1].EndUS)
	}
	if _, err := RenderSRT(lines); err != nil {
		t.Fatalf("RenderSRT line cues: %v", err)
	}
}

// 没有 Chars 的旧时间轴仍要可用：按行均分，不能重叠，也不能把最后一行吃掉。
func TestSplitSubtitleCuesFallsBackToEvenDistribution(t *testing.T) {
	cue := SubtitleCue{
		SlideID: "s1", SegmentID: "seg-1",
		StartUS: 0, EndUS: 6_000_000,
		Text: "这是一句没有字符时间戳的字幕。这是一句没有字符时间戳的字幕。这是一句没有字符时间戳的字幕。",
	}
	lines := SplitSubtitleCues([]SubtitleCue{cue})
	if len(lines) < 2 {
		t.Fatalf("lines = %d, want at least 2", len(lines))
	}
	if lines[0].StartUS != 0 || lines[len(lines)-1].EndUS != cue.EndUS {
		t.Fatalf("distributed range = %d..%d, want 0..%d", lines[0].StartUS, lines[len(lines)-1].EndUS, cue.EndUS)
	}
	var previousEnd int64
	for i, line := range lines {
		if line.StartUS < previousEnd || line.EndUS <= line.StartUS {
			t.Fatalf("line %d times = %d..%d, previous end %d", i, line.StartUS, line.EndUS, previousEnd)
		}
		previousEnd = line.EndUS
	}
	if _, err := RenderWebVTT(lines); err != nil {
		t.Fatalf("RenderWebVTT line cues: %v", err)
	}
}
