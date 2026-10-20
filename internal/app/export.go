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
	"time"

	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations"
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
	// narrationWriter 写带音频 PPTX（nil = 该格式不可用，导出时显式失败）。
	narrationWriter integrations.NarrationWriter
	// sourceRevisions 取源版本 PPTX 对象键（nil = 无法定位源文件，带音频 PPTX 显式失败）。
	sourceRevisions SourceRevisionLookup
	// maxDuration 是成品时长上限（0 = 不限）。超限在导出步骤失败，不产出半成品。
	maxDuration time.Duration
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

// WithNarrationWriter 注入带音频 PPTX 写适配器。
func (h *ExportHandler) WithNarrationWriter(w integrations.NarrationWriter) *ExportHandler {
	h.narrationWriter = w
	return h
}

// WithSourceRevisions 注入源版本查询能力（带音频 PPTX 需要原始源文件）。
func (h *ExportHandler) WithSourceRevisions(s SourceRevisionLookup) *ExportHandler {
	h.sourceRevisions = s
	return h
}

// WithMaxDuration 设置成品时长上限；0 表示不限。
func (h *ExportHandler) WithMaxDuration(d time.Duration) *ExportHandler {
	h.maxDuration = d
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
	// 成品时长 = 绑定时间轴的实际时长（迁移 0027）。四种格式同源：MP4 的画面长度由各页
	// PageDurationsMS 决定，而它正是从这里的时间轴推出的，故与时间轴时长一致。
	durationMS := int64(0)
	if bundle.Timeline.DurationUS > 0 {
		durationMS = bundle.Timeline.DurationUS / 1000
	}
	if h.maxDuration > 0 && bundle.Timeline.DurationUS > h.maxDuration.Microseconds() {
		return failStep(fmt.Errorf("export job: timeline duration %s exceeds limit %s",
			time.Duration(bundle.Timeline.DurationUS)*time.Microsecond, h.maxDuration))
	}
	// 产物数据来源：小文件（srt/vtt）读入内存；zip/mp4 走临时文件流式化，避免峰值内存随
	// 音频总量/编码产物线性增长。file != nil 时 data 为 nil，由下方流式上传。
	var data []byte
	var file *os.File
	var contentType, ext string
	switch snapshot.Format {
	case artifact.FormatSRT:
		data, err = h.readTenantObject(ctx, job.TenantID, bundle.SRTKey)
		contentType, ext = "application/x-subrip", "srt"
	case artifact.FormatVTT:
		data, err = h.readTenantObject(ctx, job.TenantID, bundle.VTTKey)
		contentType, ext = "text/vtt; charset=utf-8", "vtt"
	case artifact.FormatMP4:
		file, err = h.renderMP4(ctx, job, snapshot, bundle)
		contentType, ext = "video/mp4", "mp4"
	case artifact.FormatWebProject:
		file, err = h.packWebProject(ctx, job.TenantID, bundle)
		contentType, ext = "application/zip", "zip"
	case artifact.FormatAudioPPTX:
		data, err = h.renderAudioPPTX(ctx, job, bundle)
		contentType, ext = "application/vnd.openxmlformats-officedocument.presentationml.presentation", "pptx"
	default:
		err = fmt.Errorf("export job: unsupported format %q", snapshot.Format)
	}
	if err != nil {
		return failStep(err)
	}
	// 统一流式上传：文件产物先回卷再哈希/统计，随后 io.Copy 进对象存储。
	if file != nil {
		defer file.Close()
		defer os.Remove(file.Name())
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return failStep(err)
		}
		contentHash, size, herr := hashReader(file)
		if herr != nil {
			return failStep(fmt.Errorf("export job: hash artifact: %w", herr))
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return failStep(err)
		}
		key := objectstore.ObjectKey{TenantID: job.TenantID, ProjectID: job.ProjectID, Revision: "artifact", AssetType: "artifact", AssetID: contentHash, Ext: ext}
		if err := h.objects.Put(ctx, key, file, objectstore.ObjectMeta{ContentType: contentType, ContentHash: contentHash, Size: size}); err != nil {
			return failStep(fmt.Errorf("export job: publish artifact: %w", err))
		}
		_ = data
		data = nil
		a, aerr := h.artifacts.Create(ctx, job.TenantID, artifact.NewArtifact{
			ProjectID: job.ProjectID, SnapshotHash: snapshotHash, Format: snapshot.Format,
			ObjectKey: key.String(), ContentHash: contentHash, SizeBytes: size,
			DurationMS: durationMS,
			// 记录本成品绑定的时间轴（迁移 0040）：成品库内嵌预览据此构建播放清单，
			// 保证预览内容与下载文件同源（而不是"项目最新讲解"的另一个版本）。
			TimelineKey: snapshot.TimelineKey,
			// 冗余来源信息（源版本号 + 展示名）：成品库按"PPT 名称 + 版本"精确展示，
			// 见 internal/media/timeline.go 与 internal/app/narration.go。
			SourceRevisionNo:  sourceRevisionNo,
			SourceDisplayName: sourceDisplayName,
		})
		if aerr != nil {
			return failStep(aerr)
		}
		step.State, step.ResultRef = pipeline.StepSuccess, a.ID
		// 最终步骤随任务终态原子提交（outbox，G3-5），避免"步骤成功但任务未终态"的崩溃窗口。
		pipeline.SetCommitStep(ctx, step)
		return nil
	}
	contentHash := hashBytes(data)
	key := objectstore.ObjectKey{TenantID: job.TenantID, ProjectID: job.ProjectID, Revision: "artifact", AssetType: "artifact", AssetID: contentHash, Ext: ext}
	if err := h.objects.Put(ctx, key, bytes.NewReader(data), objectstore.ObjectMeta{ContentType: contentType, ContentHash: contentHash, Size: int64(len(data))}); err != nil {
		return failStep(fmt.Errorf("export job: publish artifact: %w", err))
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

// renderAudioPPTX 在源 PPTX 副本上按页嵌入音频并设置自动切页。
// 每页音频由时间轴在该页区间内的片段拼成完整 WAV（含静默间隔），与 MP4/网页播放同源。
func (h *ExportHandler) renderAudioPPTX(ctx context.Context, job *pipeline.Job, bundle *TimelineAsset) ([]byte, error) {
	if h.narrationWriter == nil {
		return nil, errors.New("export job: narrated pptx writer is not configured")
	}
	if h.sourceRevisions == nil {
		return nil, errors.New("export job: source revisions are not available")
	}
	revisionNo := bundle.Timeline.SourceRevisionNo
	if revisionNo <= 0 {
		return nil, errors.New("export job: timeline has no source revision; regenerate narration before exporting narrated pptx")
	}
	rev, err := h.sourceRevisions.GetSourceRevision(ctx, job.TenantID, job.ProjectID, revisionNo)
	if err != nil {
		return nil, fmt.Errorf("export job: load source revision %d: %w", revisionNo, err)
	}
	if rev == nil || rev.ObjectKey == "" {
		return nil, fmt.Errorf("export job: source revision %d has no source object", revisionNo)
	}
	src, err := h.readTenantObject(ctx, job.TenantID, rev.ObjectKey)
	if err != nil {
		return nil, fmt.Errorf("export job: read source pptx: %w", err)
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
	plan := integrations.NarrationPlan{Slides: make([]integrations.SlideNarration, 0, len(bundle.Timeline.Slides))}
	for i, slide := range bundle.Timeline.Slides {
		durationUS := slide.EndUS - slide.StartUS
		if durationUS <= 0 {
			return nil, fmt.Errorf("export job: slide %d has no duration", i)
		}
		trackKey := slide.SlideID
		if trackKey == "" {
			trackKey = fmt.Sprintf("slide-%d", i)
		}
		entry := integrations.SlideNarration{
			Index: i, TrackKey: "narration:" + trackKey,
			Advance: time.Duration(durationUS) * time.Microsecond,
		}
		if len(slide.Segments) > 0 {
			segments := make([]media.SegmentCue, 0, len(slide.Segments))
			for _, segment := range slide.Segments {
				shifted := segment
				shifted.StartUS -= slide.StartUS
				shifted.EndUS -= slide.StartUS
				segments = append(segments, shifted)
			}
			pageTimeline := &media.Timeline{
				DurationUS: durationUS,
				Slides: []media.SlideCue{{
					SlideID: slide.SlideID, StartUS: 0, EndUS: durationUS, Segments: segments,
				}},
			}
			wav, err := media.AssembleTimelineWAV(pageTimeline, clips)
			if err != nil {
				return nil, fmt.Errorf("export job: assemble slide %d audio: %w", i, err)
			}
			entry.Audio, entry.MIME = wav, "audio/wav"
		}
		plan.Slides = append(plan.Slides, entry)
	}
	report, err := h.narrationWriter.Apply(ctx, bytes.NewReader(src), int64(len(src)), plan)
	if err != nil {
		return nil, fmt.Errorf("export job: write narrated pptx: %w", err)
	}
	if len(report.Unsupported) > 0 {
		return nil, fmt.Errorf("export job: narrated pptx writer reported unsupported features: %s", strings.Join(report.Unsupported, ", "))
	}
	if len(report.Output) == 0 {
		return nil, errors.New("export job: narrated pptx writer produced no output")
	}
	return report.Output, nil
}

// packWebProject 打包可离线播放的 Web 讲解工程：timeline.json + 字幕 + 各音频片段。
//
// 流式实现：zip 写入临时文件，音频片段逐个 io.Copy 进 zip entry（绝不整体载入内存），
// 返回 *os.File 供调用方流式上传后关闭。大 deck 的音频总量可能数十 MB，整体进 bytes.Buffer
// 会让 worker 峰值内存随音频总量线性增长。
func (h *ExportHandler) packWebProject(ctx context.Context, tenantID string, bundle *TimelineAsset) (*os.File, error) {
	tmp, err := os.CreateTemp("", "ppts-webproject-*.zip")
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}
	zw := zip.NewWriter(tmp)
	addBytes := func(name string, data []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	timelineJSON, err := json.Marshal(bundle.Timeline)
	if err != nil {
		cleanup()
		return nil, err
	}
	srt, err := h.readTenantObject(ctx, tenantID, bundle.SRTKey)
	if err != nil {
		cleanup()
		return nil, err
	}
	vtt, err := h.readTenantObject(ctx, tenantID, bundle.VTTKey)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := addBytes("timeline.json", timelineJSON); err != nil {
		cleanup()
		return nil, err
	}
	if err := addBytes("subtitles.srt", srt); err != nil {
		cleanup()
		return nil, err
	}
	if err := addBytes("subtitles.vtt", vtt); err != nil {
		cleanup()
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, slide := range bundle.Timeline.Slides {
		for _, segment := range slide.Segments {
			if _, ok := seen[segment.AudioKey]; ok {
				continue
			}
			seen[segment.AudioKey] = struct{}{}
			// 音频逐个流式写入 zip：先建 entry，再从对象存储流式拷贝，不整段驻留内存。
			name := "audio/" + segment.AudioKey[strings.LastIndex(segment.AudioKey, "/")+1:]
			w, err := zw.Create(name)
			if err != nil {
				cleanup()
				return nil, err
			}
			key, perr := objectstore.Parse(segment.AudioKey)
			if perr != nil {
				cleanup()
				return nil, perr
			}
			if perr := key.EnsureTenant(tenantID); perr != nil {
				cleanup()
				return nil, perr
			}
			r, _, gerr := h.objects.Get(ctx, key)
			if gerr != nil {
				cleanup()
				return nil, gerr
			}
			_, cerr := io.Copy(w, r)
			r.Close()
			if cerr != nil {
				cleanup()
				return nil, cerr
			}
		}
	}
	if err := zw.Close(); err != nil {
		cleanup()
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, err
	}
	return tmp, nil
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
	// 编码产物本就写临时文件；直接返回 *os.File 由调用方流式上传，避免 os.ReadFile 再整份读入内存。
	tmp := filepath.Join(os.TempDir(), "ppts-export-"+job.ID+".mp4")
	if _, err := h.encoder.Encode(ctx, media.MP4EncodeOptions{
		OutPath: tmp, FPS: snapshot.FPS, Width: snapshot.Width, Height: snapshot.Height,
		PagePNGs: pagePNGs, PageDurationsMS: pageDurations, AudioPath: audioTmpName,
		BurnSubtitles: snapshot.BurnSubtitles, SubtitleASS: subtitleASS,
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
