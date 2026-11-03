package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/media"
)

// 这组用例守的是一条容易退化成"只交付一半"的分支：
// 水印与字幕共用同一个 ASS、同一个滤镜，所以判据必须是"有没有要烧的素材"，
// 而不是"用户勾没勾字幕"。三条判据各自对应一个真实入口组合。
func TestSubtitleASSForBurning(t *testing.T) {
	withCues := &TimelineAsset{Timeline: &media.Timeline{
		DurationUS: 8_000_000,
		Subtitles: []media.SubtitleCue{{
			SlideID: "s1", SegmentID: "seg1",
			StartUS: 0, EndUS: 3_000_000, Text: "第一句",
		}},
	}}
	noCues := &TimelineAsset{Timeline: &media.Timeline{DurationUS: 8_000_000}}
	h := &ExportHandler{}

	t.Run("只勾字幕", func(t *testing.T) {
		ass, err := h.subtitleASSForBurning(ExportSnapshot{BurnSubtitles: true, Width: 1920, Height: 1080}, withCues)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(ass) == 0 {
			t.Fatal("勾了字幕却没产出 ASS")
		}
		body := string(ass)
		if !strings.Contains(body, "Style: Default,") {
			t.Error("缺少字幕样式段")
		}
		// 没有水印时不该多写一条 Style/Dialogue——否则等于给所有人加了一层透明字幕。
		if strings.Contains(body, "Style: Watermark,") || strings.Contains(body, ",Watermark,,") {
			t.Error("未配置水印却写入了水印层")
		}
	})

	t.Run("只有水印", func(t *testing.T) {
		// 这是免费层最关键的路径：用户没勾字幕，但必须出带水印的成片。
		ass, err := h.subtitleASSForBurning(ExportSnapshot{
			BurnSubtitles: false, Width: 1280, Height: 720, Watermark: "由智讲PPT生成",
		}, noCues)
		if err != nil {
			t.Fatalf("只有水印时报错会让免费层导出直接失败: %v", err)
		}
		body := string(ass)
		if !strings.Contains(body, "Style: Watermark,") {
			t.Error("只有水印时未写入水印样式段——调用方会因 ASS 为空而不开滤镜，成片没水印")
		}
		if !strings.Contains(body, ",Watermark,,") {
			t.Error("缺少水印 Dialogue")
		}
		if !strings.Contains(body, "由智讲PPT生成") {
			t.Error("水印文案未进 ASS")
		}
		// 样式表里的 Default 段是常驻的（libass 要求），这里只该没有字幕事件。
		if strings.Contains(body, ",Default,,") {
			t.Error("没有字幕 cue 却写了字幕 Dialogue")
		}
	})

	t.Run("两者都无", func(t *testing.T) {
		ass, err := h.subtitleASSForBurning(ExportSnapshot{Width: 1280, Height: 720}, withCues)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ass != nil {
			t.Fatalf("什么都没有却产出 ASS，会让编码器白开一次滤镜: %q", string(ass))
		}
	})

	t.Run("只有空白水印", func(t *testing.T) {
		// 能力解析时 TrimSpace 过，但快照可能来自历史任务或直接构造；空白串等同于没配。
		ass, err := h.subtitleASSForBurning(ExportSnapshot{Width: 1280, Height: 720, Watermark: "   "}, withCues)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ass != nil {
			t.Fatalf("空白水印不该产出 ASS: %q", string(ass))
		}
	})

	t.Run("勾了字幕但时间轴没字幕", func(t *testing.T) {
		// 这条要报错而不是静默出无字幕成片：用户明确勾了，静默降级等于骗人。
		if _, err := h.subtitleASSForBurning(ExportSnapshot{
			BurnSubtitles: true, Width: 1280, Height: 720,
		}, noCues); err == nil {
			t.Fatal("勾了字幕却没有 cue，应报错")
		}
	})

	t.Run("水印排在字幕之后", func(t *testing.T) {
		// libass 按 Dialogue 顺序叠层：水印写在前面会被后面逐行刷新的字幕压住。
		ass, err := h.subtitleASSForBurning(ExportSnapshot{
			BurnSubtitles: true, Width: 1920, Height: 1080, Watermark: "由智讲PPT生成",
		}, withCues)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		body := string(ass)
		lastSubtitle := bytes.LastIndex([]byte(body), []byte(",Default,,"))
		wm := bytes.Index([]byte(body), []byte(",Watermark,,"))
		if wm < 0 || lastSubtitle < 0 {
			t.Fatalf("缺少字幕或水印 Dialogue（subtitle=%d watermark=%d）", lastSubtitle, wm)
		}
		if wm < lastSubtitle {
			t.Error("水印 Dialogue 排在字幕之前，会被字幕层叠压住")
		}
	})
}
