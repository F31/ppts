package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations"
	"github.com/F31/ppts/internal/integrations/objectstore"
	sliderenderer "github.com/F31/ppts/internal/integrations/render"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/render"
	"github.com/F31/ppts/internal/share"
)

// ExportSnapshot fixes all inputs for an export job.
// 快照必须完整记录影响产物的全部输入：除时间轴/页面图/编码参数外，
// 亦包含 burnSubtitles 与 includeNotes，避免仅因这些选项不同却得到相同 snapshotHash。
//
// 字段顺序即序列化顺序（Go 按声明顺序输出 struct 字段），而 snapshotHash 是对序列化
// 字节取摘要。硬约束只有一条：**新字段在默认零值时必须不出现在 JSON 里**
// （omitempty，且零值确实表示"没有这个选项"）。少一个 omitempty，历史产物的键会
// 一次性全部漂移；至于字段摆在哪儿其实不影响——被省略的字段不进字节（实测过：
// 把 nil 指针字段插到中间，字节不变；只去掉 omitempty，立刻就变）。
type ExportSnapshot struct {
	Format        artifact.Format `json:"format"`
	TimelineKey   string          `json:"timelineKey"`
	PagePNGKeys   []string        `json:"pagePngKeys,omitempty"`
	Width         int             `json:"width,omitempty"`
	Height        int             `json:"height,omitempty"`
	FPS           int             `json:"fps,omitempty"`
	BurnSubtitles bool            `json:"burnSubtitles,omitempty"`
	IncludeNotes  bool            `json:"includeNotes,omitempty"`
	// Animation 固定动画通道的输入；nil = 走静态路径（默认）。
	// 必须是 omitempty 的指针：开关关闭时这一整段不出现在 JSON 里，
	// 序列化结果与引入动画之前逐字节一致（见 snapshotHashOf 与回归用例）。
	Animation *AnimationOptions `json:"animation,omitempty"`
	// TextCopy 决定 HTML 成品里的讲稿文本是否可复制（ADR-029 ⑥）。
	// 用 omitempty 保持旧快照逐字节不变：关闭时这个键不出现，
	// 存量作业的 snapshot_hash 不会因为新增字段而全部变化。
	TextCopy bool `json:"textCopy,omitempty"`
	// Watermark 是成片水印文案（ADR-029 ②）；空 = 不加。
	//
	// 进快照而不是"导出时再读策略"：水印改变了产物内容，必须改变产物身份。
	// 否则租户升级去掉水印后，用同一幂等键重放会命中旧的去重记录，
	// 用户拿到的还是带水印的那份，且没有任何环节报错。
	Watermark string `json:"watermark,omitempty"`
}

// AnimationOptions 固定「这片视频依据哪些动画规格渲染」。
//
// 没有 preset / intensity：它们是规格自身的字段（animation.Revision.PresetID /
// Intensity），已在 Specs 指向的版本里，重复记录会让同一份输入有两种写法，
// 两种写法又算出两个 snapshotHash。
type AnimationOptions struct {
	// Specs 是逐页规格身份，顺序与 PagePNGKeys 对齐。
	//
	// 为什么要进哈希：spec 在导出排队期间被改，产物就不同；若键不变，
	// 步骤去重会把新任务并进旧产物，用户拿到的是旧动画。
	// 它是**校验**而不是查询键：动画存储按 (页, 语言, 源版本) 取当前版本，
	// 取回后比对 Revision，不符说明规格已变（见第 2 组接线）。
	Specs []AnimationSpecRef `json:"specs,omitempty"`
}

// AnimationSpecRef 是一页动画规格的身份。
type AnimationSpecRef struct {
	SlideID string `json:"slideId"`
	// Language 空 = 默认语言（zh-CN），与 animation.Key.Normalize 一致。
	Language string `json:"language,omitempty"`
	// SourceRevisionNo 0 = legacy，与讲稿同规则。
	SourceRevisionNo int   `json:"sourceRevisionNo,omitempty"`
	Revision         int64 `json:"revision"`
}

// SnapshotBytes 返回快照的规范序列化字节。
//
// 三个地方要用到同一份字节：Connect 入口与 REST 入口把它写进任务的 input_snapshot，
// 导出步骤反序列化后再算步骤键。三处各写一遍 json.Marshal 迟早会漂——比如某天有人
// 只在 app 侧做了规整，入口存下的载荷就和步骤键对不上，表现为「幂等重试创建出第二个任务」。
//
// 规整规则：动画选项为零值（无规格）时按 nil 处理。这样「开关关闭」与「开关打开但
// 一页规格都没有」得到同一个键——两者都不渲染动画，产物同源，分成两个键只会让同一份
// 静态视频在成品库里出现两次。
func SnapshotBytes(snapshot ExportSnapshot) ([]byte, error) {
	if snapshot.Animation != nil && len(snapshot.Animation.Specs) == 0 {
		snapshot.Animation = nil
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("export snapshot is not serializable: %w", err)
	}
	return b, nil
}

// snapshotHashOf 是快照的内容键：步骤键 `export:<format>:<hash>` 的载荷。
func snapshotHashOf(snapshot ExportSnapshot) (string, error) {
	b, err := SnapshotBytes(snapshot)
	if err != nil {
		return "", err
	}
	return hashBytes(b), nil
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
	// logger 用于记录可降级的打包告警（缺页图、转码失败、混音轨拼接失败）；nil 时静默。
	logger *slog.Logger
	// renderer 是动画渲染服务客户端（nil = 未启用动画，导出保持纯静态路径）。
	// 它与 encoder 是两条互不替代的通道：encoder 出静态版，renderer 出逐页动画片段。
	renderer *render.Client
	// slideRenderer 是本地 LibreOffice 渲染器（nil = 未启用，导出保持 150 DPI 模糊特写路径）。
	// 仅用于带 Crop 的镜头页做 300 DPI 重渲，使特写清晰；其余页仍用 150 DPI。
	// 与 renderer（动画服务客户端）无关——一个出清晰静态裁切，一个出逐页动画片段。
	slideRenderer sliderenderer.SlideRenderer
	// animationTimeout 是整片动画渲染的等待上限（0 = 不限，由作业上下文控制）。
	animationTimeout time.Duration
	// animationPages 组装逐页动画输入（底图路径、元素几何、规格）。nil = 无法组装：
	// 底图投递（tasks 2.0）与元素几何来源（tasks 2.0b）都还没有落地，
	// 此时导出保持纯静态路径，且不探测渲染服务。
	animationPages AnimationRequestBuilder
	// usage 是动画渲染分钟的结算账本（M4 §7.5）。nil = 不结算（本地/单测），
	// 额度不是导出路径的必需依赖。
	usage UsageSettler
	// pageImageEncoder 压页图为 WebP（ADR-034）。nil = 保持 JPEG q82。
	// 与 encoder 无关：那条是音频/视频，这条只影响成品包里的 pages/ 条目。
	pageImageEncoder share.PageImageEncoder
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

// WithPageImageEncoder 注入页图编码器（WebP，ADR-034）；传 nil 表示保持 JPEG q82。
//
// 与 WithSubtitleFont 一样是"能力注入"而非配置：机器上有没有 ffmpeg 决定了它
// 能不能压，而"没有"不是错误——页图退回 JPEG，包照样能播，只是更大。
func (h *ExportHandler) WithPageImageEncoder(enc share.PageImageEncoder) *ExportHandler {
	h.pageImageEncoder = enc
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

// WithSlideRenderer 注入本地 LibreOffice 渲染器（graceful）。
//
// 仅用于带 Crop 的镜头页做 300 DPI 重渲，使特写清晰。未注入（LibreOffice 不可用）
// 时保持 nil，导出退回 150 DPI 模糊特写路径——镜头是增强层，缺失清晰特写不拖累整包。
func (h *ExportHandler) WithSlideRenderer(r sliderenderer.SlideRenderer) *ExportHandler {
	h.slideRenderer = r
	return h
}

// WithMaxDuration 设置成品时长上限；0 表示不限。
func (h *ExportHandler) WithMaxDuration(d time.Duration) *ExportHandler {
	h.maxDuration = d
	return h
}

// WithLogger 注入日志器（用于 HTML 成品包的可降级告警）。
func (h *ExportHandler) WithLogger(logger *slog.Logger) *ExportHandler {
	h.logger = logger
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
	snapshotHash, err := snapshotHashOf(snapshot)
	if err != nil {
		return fmt.Errorf("export job: %w", err)
	}
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
	// 产物数据来源：小文件（srt/vtt/pptx）读入内存；zip/mp4 走临时文件流式化，避免峰值内存随
	// 音频总量/编码产物线性增长。两种来源在 produce 内归一成 exportOutput，发布阶段只有一条路径。
	out, err := h.produce(ctx, job, snapshot, bundle, snapshotHash)
	if err != nil {
		return failStep(err)
	}
	defer out.Close()
	a, err := h.storeArtifact(ctx, job, snapshot, snapshotHash, durationMS, sourceRevisionNo, sourceDisplayName, out)
	if err != nil {
		return failStep(err)
	}
	step.State, step.ResultRef = pipeline.StepSuccess, a.ID
	// 最终步骤随任务终态原子提交（outbox，G3-5），避免"步骤成功但任务未终态"的崩溃窗口。
	pipeline.SetCommitStep(ctx, step)
	return nil
}

// exportOutput 是「待发布的成品内容」，把内存产物与临时文件产物归一。
//
// 归一之前，Handle 里有两段几乎逐行重复的「写对象 + 建成品行」——文件分支一份、内存分支
// 一份，连注释都是复制的。那种重复不会立刻出错，但下一次给成品行加字段时只要漏改一处，
// 就会出现「mp4 有来源版本、srt 没有」这种只在部分格式上成立的字段，且没有任何断言会红。
type exportOutput struct {
	reader      io.Reader
	size        int64
	contentHash string
	contentType string
	ext         string
	// file 非 nil 表示产物落在临时文件上，发布完成后需要关闭并删除。
	file *os.File
}

// Close 释放临时文件产物（内存产物是空操作）。
func (o *exportOutput) Close() {
	if o.file == nil {
		return
	}
	name := o.file.Name()
	_ = o.file.Close()
	_ = os.Remove(name)
}

// bytesExportOutput 把内存产物包成 exportOutput（srt/vtt/pptx）。
func bytesExportOutput(data []byte, contentType, ext string) exportOutput {
	return exportOutput{
		reader: bytes.NewReader(data), size: int64(len(data)),
		contentHash: hashBytes(data), contentType: contentType, ext: ext,
	}
}

// fileExportOutput 把临时文件产物包成 exportOutput：回卷 → 哈希/计长 → 再回卷，供流式上传。
// 任何一步失败都会关掉并删除临时文件——调用方拿不到 output 时不会再有 defer 兜底。
func fileExportOutput(f *os.File, contentType, ext string) (exportOutput, error) {
	discard := func() {
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		discard()
		return exportOutput{}, err
	}
	contentHash, size, err := hashReader(f)
	if err != nil {
		discard()
		return exportOutput{}, fmt.Errorf("export job: hash artifact: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		discard()
		return exportOutput{}, err
	}
	return exportOutput{reader: f, size: size, contentHash: contentHash, contentType: contentType, ext: ext, file: f}, nil
}

// produce 按格式产出成品内容；这里只做「快照格式 → 具体打包器」的分发，
// 各格式的实现见 export_format_*.go。
func (h *ExportHandler) produce(ctx context.Context, job *pipeline.Job, snapshot ExportSnapshot,
	bundle *TimelineAsset, snapshotHash string) (exportOutput, error) {
	switch snapshot.Format {
	case artifact.FormatSRT:
		data, err := h.readTenantObject(ctx, job.TenantID, bundle.SRTKey)
		if err != nil {
			return exportOutput{}, err
		}
		return bytesExportOutput(data, "application/x-subrip", "srt"), nil
	case artifact.FormatVTT:
		data, err := h.readTenantObject(ctx, job.TenantID, bundle.VTTKey)
		if err != nil {
			return exportOutput{}, err
		}
		return bytesExportOutput(data, "text/vtt; charset=utf-8", "vtt"), nil
	case artifact.FormatMP4:
		f, err := h.renderMP4(ctx, job, snapshot, bundle)
		if err != nil {
			return exportOutput{}, err
		}
		return fileExportOutput(f, "video/mp4", "mp4")
	case artifact.FormatWebProject:
		f, err := h.packWebProject(ctx, job.TenantID, bundle)
		if err != nil {
			return exportOutput{}, err
		}
		return fileExportOutput(f, "application/zip", "zip")
	case artifact.FormatHTMLBundle:
		// 成品包（M6-1）：与 web_project 并列、互不影响。web_project 保持"数据包"语义，
		// 因为已有下游按"zip 内含 timeline.json"解析，原地改内容属于隐性破坏。
		f, err := h.packHTMLBundle(ctx, job, snapshot, bundle, snapshotHash)
		if err != nil {
			return exportOutput{}, err
		}
		return fileExportOutput(f, "application/zip", "zip")
	case artifact.FormatHTMLSingle:
		// 单文件成品（ADR-029 ⑤）：与成品包同一份组装，只是内联成一个文件。
		// Content-Type 用 text/html 而不是 application/octet-stream：用户的浏览器
		// 点开链接就能看，不必先存盘再拖进浏览器。
		f, err := h.packHTMLSingle(ctx, job, snapshot, bundle, snapshotHash)
		if err != nil {
			return exportOutput{}, err
		}
		return fileExportOutput(f, "text/html; charset=utf-8", "html")
	case artifact.FormatAudioPPTX:
		data, err := h.renderAudioPPTX(ctx, job, bundle)
		if err != nil {
			return exportOutput{}, err
		}
		return bytesExportOutput(data, "application/vnd.openxmlformats-officedocument.presentationml.presentation", "pptx"), nil
	default:
		return exportOutput{}, fmt.Errorf("export job: unsupported format %q", snapshot.Format)
	}
}

// storeArtifact 把成品内容发布到对象存储并落成品行。
//
// 两种数据来源（内存 / 临时文件）走同一段代码，字段一次性写全：成品行里的 timeline_key、
// 来源版本号与展示名是"成品库按 PPT 名称 + 版本展示"的依据，任何格式漏写都只会在那个
// 格式的成品上表现为空白，不会有错误浮出来。
func (h *ExportHandler) storeArtifact(ctx context.Context, job *pipeline.Job, snapshot ExportSnapshot,
	snapshotHash string, durationMS int64, sourceRevisionNo int, sourceDisplayName string, out exportOutput) (*artifact.Artifact, error) {
	key := objectstore.ObjectKey{
		TenantID: job.TenantID, ProjectID: job.ProjectID, Revision: "artifact",
		AssetType: "artifact", AssetID: out.contentHash, Ext: out.ext,
	}
	if err := h.objects.Put(ctx, key, out.reader, objectstore.ObjectMeta{
		ContentType: out.contentType, ContentHash: out.contentHash, Size: out.size,
	}); err != nil {
		return nil, fmt.Errorf("export job: publish artifact: %w", err)
	}
	a, err := h.artifacts.Create(ctx, job.TenantID, artifact.NewArtifact{
		ProjectID: job.ProjectID, SnapshotHash: snapshotHash, Format: snapshot.Format,
		ObjectKey: key.String(), ContentHash: out.contentHash, SizeBytes: out.size,
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
		return nil, err
	}
	return a, nil
}

func (h *ExportHandler) warnExport(msg string, job *pipeline.Job, err error) {
	if h.logger != nil {
		h.logger.Warn(msg, "job", job.ID, "error", err)
	}
}

// tenantObjectReader 把对象存储适配成 share.ResourceReader。
//
// 沿用导出链路既有的租户约束：key 必须属于本租户（EnsureTenant），
// 否则跨租户 key 会通过"读自己的对象"变成数据泄露通道。
type tenantObjectReader struct {
	handler  *ExportHandler
	tenantID string
}

func (r tenantObjectReader) OpenPagePNG(ctx context.Context, key string) (io.ReadCloser, error) {
	return r.open(ctx, key)
}

func (r tenantObjectReader) OpenObject(ctx context.Context, key string) (io.ReadCloser, error) {
	return r.open(ctx, key)
}

func (r tenantObjectReader) open(ctx context.Context, rawKey string) (io.ReadCloser, error) {
	key, err := objectstore.Parse(rawKey)
	if err != nil {
		return nil, err
	}
	if err := key.EnsureTenant(r.tenantID); err != nil {
		return nil, err
	}
	reader, _, err := r.handler.objects.Get(ctx, key)
	return reader, err
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
