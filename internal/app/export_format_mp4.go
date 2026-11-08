package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/pipeline"
)

func (h *ExportHandler) renderMP4(ctx context.Context, job *pipeline.Job, snapshot ExportSnapshot, bundle *TimelineAsset) (*os.File, error) {
	if h.encoder == nil {
		return nil, errors.New("export job: mp4 encoder is not configured")
	}
	if len(snapshot.PagePNGKeys) > len(bundle.Timeline.Slides) {
		return nil, errors.New("export job: page png count exceeds timeline")
	}
	// 页面图为可选增强：按 slide 位置对齐，缺图（空键）或单页读取失败 → 沿用上一页画面，
	// 首屏缺失/全缺 → 用深色占位图。这样隐藏页、渲染缺失、渲染器不可用都不会拦死 MP4 导出。
	pagePNGs := make([][]byte, 0, len(bundle.Timeline.Slides))
	var prev []byte
	for i := range bundle.Timeline.Slides {
		var rawKey string
		if i < len(snapshot.PagePNGKeys) {
			rawKey = snapshot.PagePNGKeys[i]
		}
		if rawKey == "" {
			if prev == nil {
				prev = blankPagePNG(snapshot.Width, snapshot.Height)
			}
			pagePNGs = append(pagePNGs, prev)
			continue
		}
		data, err := h.readTenantObject(ctx, job.TenantID, rawKey)
		if err != nil {
			// A26：单页对象读不到是「能力缺失」而非「内容错误」，降级为沿用上一页而非整单失败。
			if prev == nil {
				prev = blankPagePNG(snapshot.Width, snapshot.Height)
			}
			pagePNGs = append(pagePNGs, prev)
			continue
		}
		prev = data
		pagePNGs = append(pagePNGs, data)
	}
	clips := map[string][]byte{}
	for _, slide := range bundle.Timeline.Slides {
		for _, segment := range slide.Segments {
			if _, ok := clips[segment.AudioKey]; ok {
				continue
			}
			data, err := h.readTenantObject(ctx, job.TenantID, segment.AudioKey)
			if err != nil {
				return nil, err
			}
			clips[segment.AudioKey] = data
		}
	}
	// 拼接 WAV 流式写入临时文件（WriteTimelineWAV 按时间轴位置 seek 落盘），
	// 不再整份生成 []byte；编码器经 AudioPath 直接复用该文件，避免二次读回内存。
	audioTmp, err := os.CreateTemp("", "ppts-audio-*.wav")
	if err != nil {
		return nil, err
	}
	audioTmpName := audioTmp.Name()
	defer os.Remove(audioTmpName)
	defer audioTmp.Close()
	if err := media.WriteTimelineWAV(audioTmp, bundle.Timeline, clips); err != nil {
		return nil, err
	}
	if err := audioTmp.Sync(); err != nil {
		return nil, err
	}
	pageDurations := make([]int64, 0, len(bundle.Timeline.Slides))
	for _, slide := range bundle.Timeline.Slides {
		pageDurations = append(pageDurations, (slide.EndUS-slide.StartUS)/1000)
	}
	// 字幕烧录（设计方案 V1_6 §338）：由时间轴 cue（含字符级时间戳）渲染 ASS ——
	// 与下载的 SRT/VTT 同源内容，但按播放器同规则逐行轮换 + 已朗读高亮，而不是一次铺满整段。
	subtitleASS, err := h.subtitleASSForBurning(snapshot, bundle)
	if err != nil {
		return nil, err
	}
	// 动画通道（ADR-21）：开关打开、快照里有逐页规格、且能组装出输入，三者同时成立才探测。
	// 任一不成立都直接走下面的静态编码 —— 静态路径不因这条分支改变任何行为。
	if av := h.animatedVideo(ctx, job, snapshot, bundle, pagePNGs, pageDurations, audioTmpName, subtitleASS); av != nil {
		h.settleAnimationMinutes(ctx, job, pageDurations, av)
		h.logAnimationPages(av, len(pagePNGs))
		return av.File, nil
	}
	// 声明了动画却退回静态：结算 0 分钟，把受理时占住的额度放掉。
	// 不做这一步，一次渲染失败就会把额度锁到账期结束（用户看到的是"没额度了"，实际一次都没渲）。
	if snapshot.Animation != nil {
		h.settleAnimationMinutes(ctx, job, pageDurations, nil)
	}
	// 编码产物本就写临时文件；直接返回 *os.File 由调用方流式上传，避免 os.ReadFile 再整份读入内存。
	tmp := filepath.Join(os.TempDir(), "ppts-export-"+job.ID+".mp4")
	if _, err := h.encoder.Encode(ctx, media.MP4EncodeOptions{
		OutPath: tmp, FPS: snapshot.FPS, Width: snapshot.Width, Height: snapshot.Height,
		PagePNGs: pagePNGs, PageDurationsMS: pageDurations, AudioPath: audioTmpName,
		// 判据是"有没有要烧的素材"而不是"勾没勾字幕"：只有水印时 ASS 非空，
		// 也必须开滤镜，否则免费层最有价值的那条路径（导出成片）恰好没有水印。
		BurnSubtitles: len(subtitleASS) > 0, SubtitleASS: subtitleASS,
		SubtitleFontName: h.subtitleFontName,
	}); err != nil {
		return nil, classifyEncodeError(err)
	}
	f, err := os.Open(tmp)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// classifyEncodeError 把 MP4 编码错误映射为 worker 可识别的错误：
//   - 字幕能力缺失 → 带明确原因的永久错误（A26）；
//   - 校验类失败（ffprobe/抽帧）→ 多为瞬时（ffprobe 偶发段错误、读取共享音频缓存时撞上
//     并发写入），转 *pipeline.RetryError 由 worker 退避重跑整个编码（受 MaxAttempts 上限
//     约束），避免一次偶发校验失败就把已成功编码的导出判为永久失败；
//   - 其余保持原样（编码失败等）。
func classifyEncodeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, media.ErrSubtitlesUnavailable) {
		return fmt.Errorf("export job: burning subtitles is unavailable on this server: %w", err)
	}
	var ve *media.VerifyError
	if errors.As(err, &ve) {
		return &pipeline.RetryError{Err: err}
	}
	return err
}

// subtitleASSForBurning 取烧录用字幕（ASS：逐行轮换 + 已朗读位置高亮）。未勾选烧录时返回 nil。
// 以时间轴 cue（含字符级时间戳）为唯一来源，内容与下载的 .srt/.vtt 同源，只是呈现方式不同。
//
// 水印（ADR-029 ②）也走这里：它是一条额外 Dialogue，与字幕同一个 ASS、同一个滤镜。
// 因此"只有水印、没有字幕"时仍要产出 ASS——返回非空就代表"这一段素材需要烧"，调用方
// 据此决定是否开字幕滤镜，不必再单独判断水印。
func (h *ExportHandler) subtitleASSForBurning(snapshot ExportSnapshot, bundle *TimelineAsset) ([]byte, error) {
	watermark := strings.TrimSpace(snapshot.Watermark)
	if !snapshot.BurnSubtitles && watermark == "" {
		return nil, nil
	}
	if bundle.Timeline == nil {
		return nil, errors.New("export job: timeline is empty")
	}
	hasCues := len(bundle.Timeline.Subtitles) > 0
	if snapshot.BurnSubtitles && !hasCues {
		return nil, errors.New("export job: timeline has no subtitles to burn")
	}
	// 字号按输出高度缩放（1080p 约 48px）；缺省 52。
	fontSize := 52
	if snapshot.Height > 0 {
		fontSize = snapshot.Height * 48 / 1080
		if fontSize < 16 {
			fontSize = 16
		}
	}
	ass, err := media.RenderASS(bundle.Timeline.Subtitles, media.ASSOptions{
		FontName: h.subtitleFontName, PlayResX: snapshot.Width, PlayResY: snapshot.Height, FontSize: fontSize,
		Watermark: watermark, DurationUS: timelineDurationUS(bundle.Timeline),
	})
	if err != nil {
		return nil, fmt.Errorf("export job: render burned subtitles: %w", err)
	}
	return ass, nil
}

// timelineDurationUS 取时间轴总时长（微秒）。
//
// DurationUS 为 0 的历史时间轴按页累加：水印要覆盖到片尾，靠一个 0 会让它整片不出现，
// 而合成分支的页时长本来就是逐页给的，累加值与成片长度一致。
func timelineDurationUS(timeline *media.Timeline) int64 {
	if timeline == nil {
		return 0
	}
	if timeline.DurationUS > 0 {
		return timeline.DurationUS
	}
	var total int64
	for _, slide := range timeline.Slides {
		if slide.EndUS > slide.StartUS {
			total += slide.EndUS - slide.StartUS
		}
	}
	return total
}

// blankPagePNG 生成一张深色占位页面图（与播放器暗色主题一致），用于缺图页 / 首屏缺失 /
// 渲染器不可用的降级场景：导出 MP4 时这些位置以静态占位画面呈现，而非整单失败。
// 仅用标准库，无外部依赖。
func blankPagePNG(w, h int) []byte {
	if w <= 0 {
		w = 1920
	}
	if h <= 0 {
		h = 1080
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	c := color.RGBA{R: 24, G: 26, B: 32, A: 255}
	// 整幅填充：直接写像素缓冲，避免逐像素 Set 的反射开销。
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i] = c.R
		img.Pix[i+1] = c.G
		img.Pix[i+2] = c.B
		img.Pix[i+3] = c.A
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}
