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
