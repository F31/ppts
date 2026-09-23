package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/pipeline"
)

// ExportSnapshot fixes all inputs for an export job.
// 快照必须完整记录影响产物的全部输入：除时间轴/页面图/编码参数外，
// 亦包含 burnSubtitles 与 includeNotes，避免仅因这些选项不同却得到相同 snapshotHash。
type ExportSnapshot struct {
	Format        artifact.Format `json:"format"`
	TimelineKey   string          `json:"timelineKey"`
	PagePNGKeys   []string        `json:"pagePngKeys,omitempty"`
	Width         int             `json:"width,omitempty"`
	Height        int             `json:"height,omitempty"`
	FPS           int             `json:"fps,omitempty"`
	BurnSubtitles bool            `json:"burnSubtitles,omitempty"`
	IncludeNotes  bool            `json:"includeNotes,omitempty"`
}

type ExportHandler struct {
	artifacts artifact.Store
	steps     interface {
		MarkStep(context.Context, pipeline.JobStep) error
	}
	objects objectstore.ObjectStore
	encoder *media.MP4Encoder
	// subtitleFontName 字幕烧录锁定的字体族名；空 = 由系统默认字体决定（见 media.subtitleStage）。
	subtitleFontName string
}

func NewExportHandler(artifacts artifact.Store, steps interface {
	MarkStep(context.Context, pipeline.JobStep) error
}, objects objectstore.ObjectStore, encoder *media.MP4Encoder) *ExportHandler {
	return &ExportHandler{artifacts: artifacts, steps: steps, objects: objects, encoder: encoder}
}

// WithSubtitleFont 设定字幕烧录锁定的字体族名（默认空 = 系统默认）。
// 用于与部署镜像安装的字体保持一致，保证中文渲染在各环境一致可复现（V1_6 §338）。
func (h *ExportHandler) WithSubtitleFont(name string) *ExportHandler {
	h.subtitleFontName = name
	return h
}

func (h *ExportHandler) Handle(ctx context.Context, job *pipeline.Job) error {
	if job == nil || job.Kind != pipeline.KindExport {
		return errors.New("export job: unexpected job kind")
	}
	var snapshot ExportSnapshot
	if err := json.Unmarshal([]byte(job.InputSnapshot), &snapshot); err != nil {
		return fmt.Errorf("export job: invalid input snapshot: %w", err)
	}
	if job.TenantID == "" || job.ProjectID == "" || snapshot.TimelineKey == "" || snapshot.Format == "" {
		return errors.New("export job: incomplete input snapshot")
	}
	snapshotBytes, _ := json.Marshal(snapshot)
	snapshotHash := hashBytes(snapshotBytes)
	step := pipeline.JobStep{
		JobID: job.ID, TenantID: job.TenantID, StepType: "export",
		StepKey: "export:" + string(snapshot.Format) + ":" + snapshotHash,
	}
	if err := h.steps.MarkStep(ctx, pipeline.JobStep{JobID: step.JobID, TenantID: step.TenantID, StepType: step.StepType, StepKey: step.StepKey, State: pipeline.StepPending}); err != nil {
		return err
	}
	failStep := func(err error) error {
		step.State = pipeline.StepFailed
		_ = h.steps.MarkStep(ctx, step)
		return err
	}
	bundle, err := h.loadTimelineBundle(ctx, job.TenantID, snapshot.TimelineKey)
	if err != nil {
		return failStep(err)
	}
	// 读取时间轴携带的来源（源版本号 + 展示名），落库到成品行供成品库按"PPT 名称 + 版本"展示。
	// 来源由 narration 在生成时间轴时写入（见 internal/app/narration.go）；成品行与 source_revisions 无外键，
	// 故在此冗余落库，避免运行时再查。历史时间轴无来源信息时两值均为零值。
	sourceRevisionNo := 0
	sourceDisplayName := ""
	if bundle.Timeline != nil {
		sourceRevisionNo = bundle.Timeline.SourceRevisionNo
		sourceDisplayName = bundle.Timeline.SourceDisplayName
	}
	var data []byte
	var contentType, ext string
	switch snapshot.Format {
	case artifact.FormatSRT:
		data, err = h.readTenantObject(ctx, job.TenantID, bundle.SRTKey)
		contentType, ext = "application/x-subrip", "srt"
	case artifact.FormatVTT:
		data, err = h.readTenantObject(ctx, job.TenantID, bundle.VTTKey)
		contentType, ext = "text/vtt; charset=utf-8", "vtt"
	case artifact.FormatMP4:
		data, err = h.renderMP4(ctx, job, snapshot, bundle)
		contentType, ext = "video/mp4", "mp4"
	case artifact.FormatWebProject:
		data, err = h.packWebProject(ctx, job.TenantID, bundle)
		contentType, ext = "application/zip", "zip"
	default:
		err = fmt.Errorf("export job: unsupported format %q", snapshot.Format)
	}
	if err != nil {
		return failStep(err)
	}
	contentHash := hashBytes(data)
	key := objectstore.ObjectKey{TenantID: job.TenantID, ProjectID: job.ProjectID, Revision: "artifact", AssetType: "artifact", AssetID: contentHash, Ext: ext}
	if err := h.objects.Put(ctx, key, bytes.NewReader(data), objectstore.ObjectMeta{ContentType: contentType, ContentHash: contentHash, Size: int64(len(data))}); err != nil {
		return failStep(fmt.Errorf("export job: publish artifact: %w", err))
	}
	// 成品时长 = 绑定时间轴的实际时长（迁移 0027）。四种格式同源：MP4 的画面长度由各页
	// PageDurationsMS 决定，而它正是从这里的时间轴推出的，故与时间轴时长一致。
	durationMS := int64(0)
	if bundle.Timeline.DurationUS > 0 {
		durationMS = bundle.Timeline.DurationUS / 1000
	}
	a, err := h.artifacts.Create(ctx, job.TenantID, artifact.NewArtifact{
		ProjectID: job.ProjectID, SnapshotHash: snapshotHash, Format: snapshot.Format,
		ObjectKey: key.String(), ContentHash: contentHash, SizeBytes: int64(len(data)),
		DurationMS: durationMS,
		// 记录本成品绑定的时间轴（迁移 0040）：成品库内嵌预览据此构建播放清单，
		// 保证预览内容与下载文件同源（而不是"项目最新讲解"的另一个版本）。
		TimelineKey: snapshot.TimelineKey,
		// 冗余来源信息（源版本号 + 展示名）：成品库按"PPT 名称 + 版本"精确展示，
		// 见 internal/media/timeline.go 与 internal/app/narration.go。
		SourceRevisionNo:  sourceRevisionNo,
		SourceDisplayName: sourceDisplayName,
	})
	if err != nil {
		return failStep(err)
	}
	step.State, step.ResultRef = pipeline.StepSuccess, a.ID
	// 最终步骤随任务终态原子提交（outbox，G3-5），避免"步骤成功但任务未终态"的崩溃窗口。
	pipeline.SetCommitStep(ctx, step)
	return nil
}

// packWebProject 打包可离线播放的 Web 讲解工程：timeline.json + 字幕 + 各音频片段。
func (h *ExportHandler) packWebProject(ctx context.Context, tenantID string, bundle *TimelineAsset) ([]byte, error) {
	timelineJSON, err := json.Marshal(bundle.Timeline)
	if err != nil {
		return nil, err
	}
	srt, err := h.readTenantObject(ctx, tenantID, bundle.SRTKey)
	if err != nil {
		return nil, err
	}
	vtt, err := h.readTenantObject(ctx, tenantID, bundle.VTTKey)
	if err != nil {
		return nil, err
	}
	type audioClip struct {
		name string
		data []byte
	}
	clips := make([]audioClip, 0, len(bundle.Timeline.Slides))
	seen := map[string]struct{}{}
	for _, slide := range bundle.Timeline.Slides {
		for _, segment := range slide.Segments {
			if _, ok := seen[segment.AudioKey]; ok {
				continue
			}
			seen[segment.AudioKey] = struct{}{}
			data, err := h.readTenantObject(ctx, tenantID, segment.AudioKey)
			if err != nil {
				return nil, err
			}
			name := "audio/" + segment.AudioKey[strings.LastIndex(segment.AudioKey, "/")+1:]
			clips = append(clips, audioClip{name: name, data: data})
		}
	}
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	add := func(name string, data []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	if err := add("timeline.json", timelineJSON); err != nil {
		return nil, err
	}
	if err := add("subtitles.srt", srt); err != nil {
		return nil, err
	}
	if err := add("subtitles.vtt", vtt); err != nil {
		return nil, err
	}
	for _, clip := range clips {
		if err := add(clip.name, clip.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (h *ExportHandler) loadTimelineBundle(ctx context.Context, tenantID, key string) (*TimelineAsset, error) {
	data, err := h.readTenantObject(ctx, tenantID, key)
	if err != nil {
		return nil, err
	}
	var bundle TimelineAsset
	if err := json.Unmarshal(data, &bundle); err != nil || bundle.Timeline == nil || bundle.SRTKey == "" || bundle.VTTKey == "" {
		return nil, errors.New("export job: invalid timeline bundle")
	}
	return &bundle, nil
}

func (h *ExportHandler) readTenantObject(ctx context.Context, tenantID, rawKey string) ([]byte, error) {
	key, err := objectstore.Parse(rawKey)
	if err != nil {
		return nil, err
	}
	if err := key.EnsureTenant(tenantID); err != nil {
		return nil, err
	}
	r, _, err := h.objects.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func (h *ExportHandler) renderMP4(ctx context.Context, job *pipeline.Job, snapshot ExportSnapshot, bundle *TimelineAsset) ([]byte, error) {
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
	audio, err := media.AssembleTimelineWAV(bundle.Timeline, clips)
	if err != nil {
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
	tmp := filepath.Join(os.TempDir(), "ppts-export-"+job.ID+".mp4")
	defer os.Remove(tmp)
	if _, err := h.encoder.Encode(ctx, media.MP4EncodeOptions{
		OutPath: tmp, FPS: snapshot.FPS, Width: snapshot.Width, Height: snapshot.Height,
		PagePNGs: pagePNGs, PageDurationsMS: pageDurations, AudioWAV: audio,
		BurnSubtitles: snapshot.BurnSubtitles, SubtitleASS: subtitleASS,
		SubtitleFontName: h.subtitleFontName,
	}); err != nil {
		return nil, classifyEncodeError(err)
	}
	return os.ReadFile(tmp)
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
func (h *ExportHandler) subtitleASSForBurning(snapshot ExportSnapshot, bundle *TimelineAsset) ([]byte, error) {
	if !snapshot.BurnSubtitles {
		return nil, nil
	}
	if bundle.Timeline == nil || len(bundle.Timeline.Subtitles) == 0 {
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
	})
	if err != nil {
		return nil, fmt.Errorf("export job: render burned subtitles: %w", err)
	}
	return ass, nil
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
