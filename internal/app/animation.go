package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/observability"
	"github.com/F31/ppts/internal/render"
)

// 这一层的职责边界：
//
//	本文件只做「给定逐页规格与几何 → 拿到逐页片段」的编排（compile → submit → wait → 下载）。
//	它不读数据库、不读对象存储、不决定降级——那三件事分别属于调用方与 renderMP4。
//	保持这个边界，是为了让「渲染服务抖一下」与「用户数据有问题」在上层能分开处理（ADR-21）。

// AnimationPage 是一页的动画渲染输入。
type AnimationPage struct {
	SlideID    string
	DurationMs int64
	// BaseImage 是**渲染服务资产根**下的相对路径，不是对象存储键。
	// 两者之间怎么映射是部署问题（见 deploy/renderer/compose.yml 的 -assets-dir）。
	BaseImage string
	// Elements 是页内可挂元素的 EMU 几何，与规格里的 ElementID 对应。
	// 几何缺失的页不会报错：它的轨道会被编译成 anchor_missing 诊断并跳过，
	// 该页仍然产出一段静态画面。
	Elements []render.Element
	// Revision 为 nil 表示该页没有动画规格：照样渲一段静态画面。
	// 这是常态（用户只给部分页配了动效），不是降级。
	Revision *animation.Revision
	// Shots 是页内镜头切片（EMU 坐标）。与 Revision 同序来源于规格，可以独立于
	// 分层动画存在（"先全图、后特写" 不需要元素动效）。
	Shots []render.PageShotInput
	// SegmentOffsets 是分段 id → 相对本页起点的毫秒偏移，供 withSegment 触发器解析。
	SegmentOffsets map[string]int64
	// SegmentCharStarts 是分段 id → 每个 DisplayText 字符相对本页起点的毫秒。
	// 供 atWord 触发器解析；缺失时编译器退回分段起点并留 word_anchor_missing。
	SegmentCharStarts map[string][]int64
	// SegmentTexts 是分段 id → DisplayText，供表头/容器文本锚点定位。
	SegmentTexts map[string]string
	// Focus 是本页的焦点序列（画布像素矩形，或者图层 id）。
	//
	// 由讲稿的分段目标 + 解析产物的框算出（见 focus_spans.go），不是规格的一部分：
	// 它跟着"这段话在说什么"走，与转场/位移无关。
	Focus []render.FocusSpan
	// Diagnostics 是组装期就已经知道的诊断（焦点目标找不到等）。它们不在编译器的
	// 视野里，但和编译诊断同样不阻断渲染、同样要留痕——所以随页带进去，最后并进
	// plan.Warnings。
	Diagnostics []render.Diagnostic
	// BeatSheet 是编译期注视 IR。Valid 时编译器按拍表填 Focus，不再 equal-spread。
	BeatSheet *animation.BeatSheet
	// GazeDenyIDs 是页码/图注，回退 GazeFromSegmentTracks 也跳过。
	GazeDenyIDs map[string]struct{}
}

// AnimationRequest 是一次动画渲染的全部输入。
type AnimationRequest struct {
	Pages []AnimationPage
	// Width/Height 是输出画布，必须是偶数（H.264 yuv420p）。
	Width, Height int
	FPS           int
	// SlideWidthEMU/SlideHeightEMU 为 0 时由画布宽高比与标准页高推断，
	// 此时 EMU 几何的换算会带一条 geometry_inferred 诊断。
	//
	// 显式给值时若与画布宽高比不符，编译会返回 ErrAspectMismatch——
	// 这是刻意的：4:3 的页按 16:9 输出，元素框要么错位要么被拉伸，
	// 与其静默出一版歪的动画，不如让调用方降级成静态版（静态路径会拉伸整页，
	// 元素框跟着页面一起变形才是自洽的）。
	SlideWidthEMU  int64
	SlideHeightEMU int64
	Background     string
	Fonts          []render.Font
}

// AnimationRender 是编排结果。
type AnimationRender struct {
	// Segments 与 Pages 同序，元素是本地片段路径；空串表示该页没拿到片段。
	Segments []string
	// Missing 是没拿到片段的页索引（0 基）。调用方 MUST 把这些页换成静态段，
	// 而不是跳过——跳过会让成片比时间轴短，音频整体前移。
	Missing []int
	// PlanHash 是渲染服务为这份计划算的键，用于排查「同一份输入渲出两份产物」。
	PlanHash string
	// Diagnostics 是编译期诊断（anchor_missing 等）。它们不阻断渲染，但必须留痕。
	Diagnostics []render.Diagnostic
}

// AnimationPageInput 是组装动画输入时可用的材料。
type AnimationPageInput struct {
	Snapshot ExportSnapshot
	// TenantID/ProjectID 是规格的隔离维度。有了它们，组装器才能按 (租户, 项目, 页,
	// 语言, 源版本) 取回规格本体——快照里只有规格身份，没有内容。
	TenantID  string
	ProjectID string
	// Slides 是时间轴的逐页区间，页序即成片页序。
	Slides []media.SlideCue
	// PagePNGs 与 Slides 同序，是已读入内存的页面 PNG。
	// 把它们送进渲染服务的资产根由 AssetPublisher 负责（tasks 2.0）。
	PagePNGs [][]byte
	// BaseImagePaths 非空时直接用这些路径作为 IR 的 baseImage，不调用发布器。
	//
	// 供 HTML 成品包使用：它把页图打进包内，渲染服务的资产根对它没有意义。
	// 没有这一条，包导出会被迫先往一个用不上的目录写 20 张 PNG——既多一次对象往返，
	// 又让「HTML 包不需要渲染服务」这句话在代码里不成立。
	// 非空时必须与 Slides 同序等长。
	BaseImagePaths []string
	// Subtitles 是时间轴字幕（DisplayText + 可选字级钟）。拍表只用这一份，不用 SpokenText。
	Subtitles []media.SubtitleCue
}

// AnimationRequestBuilder 把材料组装成一次动画渲染的完整输入。
//
// 它是导出主路径与「动画数据来源」之间唯一的接缝：页图投递（tasks 2.0）与
// 元素几何来源（tasks 2.0b）都还没定，所以这两件事不进导出主路径，
// 而是由各自的适配器在这里补上。未注入 = 无法组装 = 导出保持纯静态路径。
type AnimationRequestBuilder func(ctx context.Context, in AnimationPageInput) (AnimationRequest, error)

// WithAnimationPages 注入动画输入组装器。不注入时导出走纯静态路径，
// 即使渲染客户端已配置也不会被探测——「开关关闭」必须与「没有这个功能」等价。
func (h *ExportHandler) WithAnimationPages(b AnimationRequestBuilder) *ExportHandler {
	h.animationPages = b
	return h
}

// WithAnimation 注入动画渲染客户端。client 为 nil 表示未启用（导出保持纯静态路径）。
// timeout 是整片等待上限；0 表示不设上限，由作业上下文控制。
func (h *ExportHandler) WithAnimation(client *render.Client, timeout time.Duration) *ExportHandler {
	h.renderer = client
	h.animationTimeout = timeout
	return h
}

// animationWaitTimeout 把配置里的等待上限当作地板，再按片长加 40 分钟开销。
// 24 分钟教学片在 2 核上 look 仍帧仍是大头；looks=once 把每页 N 次 medium 收成一次 ultrafast。
// 20 分钟硬顶会把注视通道降成静图。0 仍表示不设上限，交给作业上下文。
func animationWaitTimeout(base, content time.Duration) time.Duration {
	if base == 0 {
		return 0
	}
	const overhead = 40 * time.Minute
	const capWait = 2 * time.Hour
	need := content + overhead
	if need < base {
		need = base
	}
	if need > capWait {
		return capWait
	}
	return need
}

func compiledContentDuration(plan *render.Timeline) time.Duration {
	if plan == nil {
		return 0
	}
	var total int64
	for _, p := range plan.Pages {
		total += p.DurationMs
	}
	return time.Duration(total) * time.Millisecond
}

// WithRenderer 只注入渲染客户端，不打开动画通道。
//
// 为什么需要它：h.renderer 有两个用途——渲染动画片段，以及**裁成品包要内嵌的字体**。
// 后者与动画无关：一份不带任何动画的静态成品包，字幕同样需要内嵌字体才能在别人
// 机器上不出方框。若只有 WithAnimation 一条注入路径，动画开关（默认关）就会连带
// 关掉字体子集，而「开关关闭」只应该关掉动画这一件事。
func (h *ExportHandler) WithRenderer(client *render.Client) *ExportHandler {
	h.renderer = client
	return h
}

// RenderAnimation 编排一次动画渲染。workDir 应当**每次导出独占一个目录**：
// 片段按 `segment_NNN.mp4` 落盘，两个导出共用一个目录会互相覆盖，
// 而覆写发生在下载阶段，表现得像「这页放错了」。
//
// 三态语义（ADR-21）：这里只区分「拿到片段」与「没拿到」，不把错误分类成
// 「用户该重试」或「该报错」——那是静态编码路径 classifyEncodeError 的职责，
// 动画是增强层，它的任何失败都应止于降级。
func (h *ExportHandler) RenderAnimation(ctx context.Context, req AnimationRequest, workDir string) (*AnimationRender, error) {
	if h.renderer == nil {
		return nil, fmt.Errorf("%w: renderer is not configured", render.ErrAnimationUnavailable)
	}
	if len(req.Pages) == 0 {
		return nil, errors.New("animation: no pages to render")
	}
	doneCompile := observability.ExportStage(ctx, observability.ExportStageCompile)
	plan, err := h.compileAnimationPlan(req)
	doneCompile()
	if err != nil {
		return nil, err
	}
	h.observeCompiledPlan(plan)
	doneRender := observability.ExportStage(ctx, observability.ExportStageRender)
	jobID, err := h.renderer.Submit(ctx, plan)
	if err != nil {
		doneRender()
		return nil, fmt.Errorf("animation: submit: %w", err)
	}
	waitFor := animationWaitTimeout(h.animationTimeout, compiledContentDuration(plan))
	if h.logger != nil && waitFor != h.animationTimeout {
		h.logger.Info("animation wait timeout scaled with content",
			"base", h.animationTimeout, "content", compiledContentDuration(plan), "wait", waitFor)
	}
	st, err := h.renderer.Wait(ctx, jobID, 0, waitFor)
	if err != nil {
		doneRender()
		// 等待失败（超时/取消）必须让服务端尽快释放并发位：一次卡住的导出
		// 会把渲染槽占满，后面的导出连排队都进不去。成功路径不取消——
		// 对已成功任务发 DELETE 是纯噪声。
		_ = h.renderer.Cancel(context.WithoutCancel(ctx), jobID)
		return nil, fmt.Errorf("animation: wait %s: %w", jobID, err)
	}
	if st.State != render.StateSucceeded {
		doneRender()
		msg := st.Error
		if msg == "" {
			msg = "render job ended in state " + string(st.State)
		}
		return nil, fmt.Errorf("animation: render failed: %s", msg)
	}
	doneRender()
	if err := os.MkdirAll(workDir, 0o750); err != nil {
		return nil, fmt.Errorf("animation: prepare work dir: %w", err)
	}
	// 服务端把某页换成了静态画面（tasks 3.7）：片段仍然齐全、时长照旧，
	// 用户拿到的是「这页没有动画」而不是错误。这类降级只发生在渲染器内部，
	// 不记下来就没有任何地方能解释「为什么这页没动」。
	//
	// page_skipped 刻意不在这里记：那一页没有片段，下面的 Missing 循环会再记一条，
	// 两条日志说同一个问题只会让排查时找不到主线。
	for _, d := range st.DegradedPages {
		if d.Code != render.DegradationStill {
			continue
		}
		reason := d.Message
		if reason == "" {
			reason = "page render failed"
		}
		h.logAnimationDegrade(jobID, d.Page, reason)
	}
	present := make(map[string]struct{}, len(st.Artifacts))
	for _, a := range st.Artifacts {
		present[a] = struct{}{}
	}
	out := &AnimationRender{
		Segments:    make([]string, len(req.Pages)),
		PlanHash:    st.PlanHash,
		Diagnostics: plan.Warnings,
	}
	doneDownload := observability.ExportStage(ctx, observability.ExportStageDownload)
	h.downloadRenderSegments(ctx, jobID, workDir, present, out)
	doneDownload()
	return out, nil
}

type segmentDownload struct {
	path    string
	missing bool
	reason  string
}

func (h *ExportHandler) downloadRenderSegments(ctx context.Context, jobID, workDir string, present map[string]struct{}, out *AnimationRender) {
	n := len(out.Segments)
	results := make([]segmentDownload, n)
	sem := make(chan struct{}, exportObjectFetchLimit)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		name := render.SegmentName(i)
		if _, ok := present[name]; !ok {
			results[i] = segmentDownload{missing: true, reason: "segment missing from artifacts"}
			continue
		}
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				reason := "canceled"
				if err := ctx.Err(); err != nil {
					reason = err.Error()
				}
				results[i] = segmentDownload{missing: true, reason: reason}
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			dst := filepath.Join(workDir, name)
			got, err := h.downloadSegment(ctx, jobID, name, dst)
			if err != nil {
				results[i] = segmentDownload{missing: true, reason: err.Error()}
				return
			}
			if got == 0 {
				_ = os.Remove(dst)
				results[i] = segmentDownload{missing: true, reason: "empty segment"}
				return
			}
			results[i] = segmentDownload{path: dst}
		}(i, name)
	}
	wg.Wait()
	for i, r := range results {
		if r.missing {
			out.Missing = append(out.Missing, i)
			h.logAnimationDegrade(jobID, i, r.reason)
			continue
		}
		out.Segments[i] = r.path
	}
}

// downloadSegment 把一页片段写到本地文件。写失败时删掉半成品：
// 半个 mp4 留在目录里，下一轮拼接会把它当成有效分段。
func (h *ExportHandler) downloadSegment(ctx context.Context, jobID, name, dst string) (int64, error) {
	f, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	n, err := h.renderer.DownloadArtifact(ctx, jobID, name, f)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(dst)
		return 0, err
	}
	return n, nil
}

// logAnimationDegrade 记录降级。降级是静默的用户体验损失，日志是唯一能让
// 「用户说这页没动」变得可查的东西。logger 未注入时保持安静。
func (h *ExportHandler) logAnimationDegrade(jobID string, page int, reason string) {
	stage := degradeStage(reason)
	render.RecordDegrade(stage)
	if h.logger == nil {
		return
	}
	h.logger.Warn("animation segment degraded to still",
		"render_job", jobID, "page", page, "reason", reason, "stage", stage)
}

func degradeStage(reason string) string {
	switch reason {
	case "segment missing from artifacts":
		return "missing"
	case "empty segment":
		return "empty"
	default:
		return "still"
	}
}

func (h *ExportHandler) observeCompiledPlan(plan *render.Timeline) {
	if plan == nil {
		return
	}
	idxs := make([]int, len(plan.Pages))
	for i := range plan.Pages {
		idxs[i] = i
	}
	chrome, gaze, still := render.SplitRenderPages(plan, idxs)
	render.RecordGazePages(len(gaze))
	for _, i := range gaze {
		observability.BeatsLooks(len(plan.Pages[i].Focus))
	}
	if render.ChromeShareAlarm(len(chrome), len(gaze)) {
		render.RecordChromeShareAlarm()
		if h.logger != nil {
			h.logger.Warn("animation chrome share high",
				"chrome", len(chrome), "gaze", len(gaze), "still", len(still),
				"share", render.ChromeShare(len(chrome), len(gaze)))
		}
	}
	for _, d := range plan.Warnings {
		if d.Code == render.DiagEntranceSkipped {
			render.RecordEntranceSkipped()
		}
	}
	if h.logger == nil {
		return
	}
	for i, page := range plan.Pages {
		mode, looks, plate := render.DescribePageMotion(page)
		h.logger.Info("animation page compiled",
			"page", i, "motion_mode", mode, "looks", looks, "plate", plate)
		if n := len(page.Annotations); n > 1 {
			h.logger.Info("animation callouts",
				"page", i, "n", n,
				"end0", page.Annotations[0].EndMs,
				"endLast", page.Annotations[n-1].EndMs)
		}
	}
}
