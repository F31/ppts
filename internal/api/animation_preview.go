package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animstore"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/design"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/render"
	"github.com/F31/ppts/internal/share"
)

// 单页预览（tasks 3.3）。
//
// 契约只有一句：**只编译这一页的 IR**，把和 HTML 成片同一份场景交给浏览器播放。
// 不提交渲染服务：全片预览意味着一次编辑动作渲一遍全片，用户改第 3 条要点的锚点
// 也要等整片——按 Q1 的决策首版不做。导出成片仍走渲染服务，与这条路径无关。
//
// 两个刻意的取舍：
//
//  1. **预览可以带上未保存的草案**。请求体里有 spec 时，用这份规格编译场景，不落库。
//     导出仍只读已保存的规格，所以「舞台上看到的」和「成片」只在用户还没保存时不同。
//     没有 spec 时仍读已保存的规格。草案走和 PUT 同一套 Validate。
//  2. **画布跟页图比例走，高度对齐成片基准 1080**。位移幅度按画布高 / 1080 缩放，
//     高度对齐后与 1080p 成片是同一套幅度；宽度按页图比例推，4:3 不会被拉进 16:9。
//     帧率 30，与分享播放器的采样步长一致。

const (
	// previewBudget 是"提交→拿到字节"的默认总预算，也是超时提示的阈值。
	//
	// 3s 是产品默认值（这一份契约写在 Q1 决策里），但它必须可配：纯 CPU 渲染器上
	// 一页 1080p30 的渲染耗时随机器差异极大，本机验收环境单页实测约 4s，硬编码的
	// 3s 会让这个端点**恒定 504**——一个永远拿不到结果的按钮比没有按钮更糟。
	// 部署方按自己的硬件把 PPTS_ANIMATION_PREVIEW_BUDGET 调大即可。
	previewBudget = 3 * time.Second
	// PreviewBudgetMin 是下限：比这更短的预算连提交请求都发不完，只会制造假超时。
	PreviewBudgetMin = 1 * time.Second
	// PreviewBudgetMax 是上限：前端把 HTTP 兜底超时设成 90s，服务端预算必须明显小于它，
	// 否则用户看到的是"网络断了"（本地超时）而不是"这一页渲得太慢"（服务端 504）。
	//
	// 60s 不是拍的：纯 CPU 渲染器上预览吞吐低于实时（实测约 0.8x），
	// 一页 30s 的讲解就要渲近 40s。这个上限的真实含义是"拒绝比整片还慢的等待"，
	// 而不是"3s 的体验"。真要 3s 级预览，得改成只渲改变点附近的一小段窗口——
	// 那是产品决策，不在本轮。
	PreviewBudgetMax = 60 * time.Second
	// previewCanvasHeight 与成片基准同高（animation.BaselineHeight = 1080）。
	// 宽度按页图比例推，并夹在 [previewCanvasMinWidth, previewCanvasMaxWidth]。
	previewCanvasHeight   = 1080
	previewCanvasMinWidth = 240
	previewCanvasMaxWidth = 3840
	// previewFPS 与导出缺省帧率一致：分享播放器按 1000/fps 采样，帧率不同画面就会分叉。
	previewFPS = 30
	// previewMaxImageBytes 限制读入的页图大小：一张 1080p PNG 通常 <4MB，
	// 32MB 只用来挡住"把这个端点当文件读取器"的用法。
	previewMaxImageBytes = 32 << 20
)

// 预览能力的原因码。前端按码给可行动文案，运维按码查配置项。
//
// 这些值同时出现在 GET .../preview/capability 与 POST 失败时的 503 信封里，
// 两边必须一致：页面提前置灰的理由和真的点了之后看到的理由不能是两句话。
const (
	PreviewReasonOK              = "ok"
	PreviewReasonDisabled        = "animation_disabled"
	PreviewReasonRendererMissing = "renderer_not_configured"
	PreviewReasonAssetDirMissing = "assets_not_configured"
	PreviewReasonRendererInvalid = "renderer_invalid"
	PreviewReasonAssetDirInvalid = "assets_invalid"
)

// AnimationPreviewer 提供单页动画预览。
//
// 依赖缺一就构造「未启用」降级实例（不再是不注册路由）：预览不是可选增强，
// 它要么完整可用，要么**明确地**不可用——静默 404 会让用户以为"动效坏了"，
// 而前端只能显示一句没有原因的"预览失败"，反复重试一个永远不可能成功的按钮。
type AnimationPreviewer struct {
	store   animstore.Store
	objects objectstore.ObjectStore
	jobs    JobCreator
	// pages 提供页文档：预览需要元素的 EMU 框才能把元素级轨道编译成图层。
	pages AnimationPageResolver
	// budget 可覆盖（测试用短预算），0 用 previewBudget。
	budget time.Duration
	// unavailableReason 非空时所有预览请求返回 503 preview_unavailable。
	// 此时其余依赖可以为 nil，Register 依然挂载路由。
	unavailableReason string
}

// NewAnimationPreviewer 构造预览器。
//
// 浏览器播放不提交渲染服务，也不把页图投递到资产目录，所以 assets 与 renderer
// 可以为 nil（参数保留是为了调用点不用改签名）。store、objects、jobs、pages
// 缺一则返回 nil，调用方据此换上明确说明原因的降级实例。
func NewAnimationPreviewer(store animstore.Store, objects objectstore.ObjectStore, jobs JobCreator,
	_ app.AssetPublisher, _ *render.Client, pages AnimationPageResolver) *AnimationPreviewer {
	if store == nil || objects == nil || jobs == nil || pages == nil {
		return nil
	}
	return &AnimationPreviewer{
		store: store, objects: objects, jobs: jobs, pages: pages,
	}
}

// NewAnimationPreviewerUnavailable 构造「这个部署没开预览」的降级处理器。
//
// 仍然注册路由是本轮修复的核心：POST 返回 503 JSON 而不是落到 SPA 兜底/404 纯文本，
// GET capability 让页面在点之前就知道该不该置灰。"接口不存在"不是有效答案。
func NewAnimationPreviewerUnavailable(reason string, budget time.Duration) *AnimationPreviewer {
	if strings.TrimSpace(reason) == "" {
		reason = PreviewReasonRendererMissing
	}
	return &AnimationPreviewer{unavailableReason: reason, budget: budget}
}

// WithBudget 覆盖预览预算（测试用短预算，或部署方按硬件调整）。
func (p *AnimationPreviewer) WithBudget(d time.Duration) *AnimationPreviewer {
	if p != nil && d > 0 {
		p.budget = d
	}
	return p
}

// effectiveBudget 是能力响应里给出的预算：必须与真实提交用的是同一个数，
// 否则前端按 3s 提示，服务端其实给了 20s，用户会被"太慢"的提示误导。
func (p *AnimationPreviewer) effectiveBudget() time.Duration {
	if p == nil || p.budget <= 0 {
		return previewBudget
	}
	return p.budget
}

// ClampPreviewBudget 把配置来的预算收进 [PreviewBudgetMin, PreviewBudgetMax]。
//
// 夹紧放在读取配置的边界上，而不是 setter 里：预算不是"越大越好"的旋钮，
// 它得和前端 90s 的兜底超时保持先后关系，否则用户看到的是"网络断了"而不是
// "这一页渲得太慢"。测试用 WithBudget 可给更短的值，不受这里约束。
func ClampPreviewBudget(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d < PreviewBudgetMin {
		return PreviewBudgetMin
	}
	if d > PreviewBudgetMax {
		return PreviewBudgetMax
	}
	return d
}

func (p *AnimationPreviewer) Register(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
	if p == nil {
		return
	}
	mux.Handle("POST /api/projects/{projectId}/slides/{slideId}/animation/preview",
		auth(http.HandlerFunc(p.preview)))
	// 能力探测始终注册：回答"这一刻该不该让用户点预览"不需要渲染服务，
	// 未启用时它给出具体缺哪个配置，而不是让页面去猜一个 404。
	mux.Handle("GET /api/projects/{projectId}/slides/{slideId}/animation/preview/capability",
		auth(http.HandlerFunc(p.capability)))
}

// previewCapability 是单页预览的部署级能力快照。
//
// Configured 指"依赖齐全、路由可用"，**不是**"渲染服务此刻可达"：
// 服务临时挂掉由 POST 的 502 renderer_unavailable 回答，两者的修复动作不同
// （前者找运维配环境，后者稍后重试），合成一个 bool 会把用户指错方向。
type previewCapability struct {
	Configured bool   `json:"configured"`
	Reason     string `json:"reason"`
	BudgetMs   int64  `json:"budgetMs"`
	Message    string `json:"message,omitempty"`
}

func (p *AnimationPreviewer) capability(w http.ResponseWriter, r *http.Request) {
	if _, ok := PrincipalFromContext(r.Context()); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthenticated"})
		return
	}
	reason := p.unavailableReason
	if reason == "" {
		reason = PreviewReasonOK
	}
	resp := previewCapability{
		Configured: reason == PreviewReasonOK,
		Reason:     reason,
		BudgetMs:   p.effectiveBudget().Milliseconds(),
	}
	if !resp.Configured {
		resp.Message = previewUnavailableMessage(reason)
	}
	writeJSON(w, http.StatusOK, resp)
}

// previewUnavailableMessage 给运维/日志一句能直接照着改的话。
// 前端不直接展示这句中文（会做本地化），但兜底展示时也必须说清缺哪个配置项。
func previewUnavailableMessage(reason string) string {
	switch reason {
	case PreviewReasonDisabled:
		return "单页预览未启用：当前部署关闭了动画功能（PPTS_ANIMATION_ENABLED）"
	case PreviewReasonRendererMissing:
		return "单页预览未启用：缺少渲染服务地址（PPTS_RENDERER_URL）"
	case PreviewReasonAssetDirMissing:
		return "单页预览未启用：缺少页图资产目录（PPTS_ANIMATION_ASSET_DIR）"
	case PreviewReasonRendererInvalid:
		return "单页预览未启用：渲染服务地址无效（PPTS_RENDERER_URL）"
	case PreviewReasonAssetDirInvalid:
		return "单页预览未启用：页图资产目录不可用（PPTS_ANIMATION_ASSET_DIR）"
	default:
		return "单页预览未启用：部署依赖不完整（" + reason + "）"
	}
}

type previewRequest struct {
	TimelineKey string               `json:"timelineKey"`
	Spec        *animationPutRequest `json:"spec,omitempty"`
}

// previewRevision 在草案和已保存规格之间选一份。草案不落库，导出仍读已保存的那份。
func previewRevision(ctx context.Context, store animstore.Store, key animation.Key, pc animation.PageContext, spec *animationPutRequest) (*animation.Revision, error) {
	if spec == nil {
		return store.Get(ctx, key.Normalize())
	}
	intensity := spec.Intensity
	if intensity == 0 {
		intensity = 1
	}
	rev := &animation.Revision{
		TenantID:         key.TenantID,
		ProjectID:        key.ProjectID,
		SlideID:          key.SlideID,
		Language:         key.Language,
		SourceRevisionNo: key.SourceRevisionNo,
		Status:           animation.StatusDraft,
		PresetID:         spec.PresetID,
		Intensity:        intensity,
		MotionMode:       spec.MotionMode,
		Tracks:           spec.Tracks,
	}
	if err := rev.Validate(pc); err != nil {
		return nil, err
	}
	return rev, nil
}

func (p *AnimationPreviewer) preview(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthenticated"})
		return
	}
	// 依赖不全时不能落到 404：那会被前端当成"接口不存在"这个 bug 反复重试。
	// 503 + reason 才是可行动的答案（配置好了会自己变成 200）。
	if p.unavailableReason != "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    "preview_unavailable",
			"reason":  p.unavailableReason,
			"message": previewUnavailableMessage(p.unavailableReason),
		})
		return
	}
	key, ok := animationKeyFromRequest(w, r, principal.TenantID)
	if !ok {
		return
	}
	var req previewRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAnimationBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_body", "message": err.Error()})
		return
	}
	timelineKey := strings.TrimSpace(req.TimelineKey)
	if timelineKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code": "missing_timeline", "message": "timelineKey 必填：预览的页时长与分段偏移都来自时间轴",
		})
		return
	}

	bundle, _, err := loadBundle(r.Context(), p.objects, principal.TenantID, timelineKey)
	if err != nil || bundle == nil || bundle.Timeline == nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code": "timeline_unavailable", "message": "时间轴读不到，无法确定这一页的时长",
		})
		return
	}
	var slide *media.SlideCue
	for i := range bundle.Timeline.Slides {
		if bundle.Timeline.Slides[i].SlideID == key.SlideID {
			slide = &bundle.Timeline.Slides[i]
			break
		}
	}
	if slide == nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code": "slide_not_in_timeline", "message": "该页不在这条时间轴上",
		})
		return
	}
	// 页文档同时给出页时长与元素的 EMU 框：时长以 resolver 为准（与保存校验同源），
	// 几何只有它有——导出侧因为拿不到（tasks 2.0b）会跳过元素级轨道，
	// 预览拿到的是"规格真正想表达的那一版"。这个差别必须让用户看得见（见响应头）。
	page, pc, err := p.pages.Resolve(r.Context(), principal.TenantID, principal.UserID,
		key.ProjectID, key.SourceRevisionNo, key.SlideID, key.Language)
	if err != nil || page == nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code": "page_unavailable", "message": "读不到这一页的内容",
		})
		return
	}
	durationMS := pc.DurationMS
	if durationMS <= 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code": "duration_unknown", "message": "该页还没有配音，无法预览动效",
		})
		return
	}
	rev, err := previewRevision(r.Context(), p.store, key, pc, req.Spec)
	if err != nil {
		if errors.Is(err, animation.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"code": "no_animation", "message": "该页还没有动画规格",
			})
			return
		}
		writeAnimationError(w, err)
		return
	}

	pngKey, err := p.pageImageKey(r.Context(), principal.TenantID, key.ProjectID, timelineKey, key.SlideID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "internal", "message": err.Error()})
		return
	}
	if pngKey == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code": "page_image_missing", "message": "该页没有渲染图，无法合成预览",
		})
		return
	}
	pagePNG, err := p.readObject(r.Context(), principal.TenantID, pngKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "internal", "message": err.Error()})
		return
	}
	base, err := png.Decode(bytes.NewReader(pagePNG))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code": "page_image_invalid", "message": "该页渲染图不是可读的 PNG",
		})
		return
	}
	base = render.SnapRasterSlack(base)

	canvas := previewCanvasSize(base.Bounds().Dx(), base.Bounds().Dy())
	// 焦点与元素几何来自同一份页文档、同一个源版本，只是这里不需要对象读：
	// page 就是 Resolve 读出来的那一页。
	focus, focusDiags := app.PageFocusSpans(page, *slide, rev, durationMS, canvas, pc.SlideHeightEMU)
	focusSkipped, focusReason := len(focusDiags), ""
	if len(focusDiags) > 0 {
		focusReason = focusDiags[0].Code
	}
	// 画布宽被夹到 [240,3840] 后可能不再保持页图比例（正常 16:9/4:3 永远触发不到）。
	// 此时 EMU→像素的单一系数会把框整体放歪：宁可这一页不指，也不能指错位置——
	// 遮罩错位在画面上就是「孔开在旁边」，而所有接口都返回 200。
	if len(focus) > 0 && !canvasKeepsAspectSize(canvas, base.Bounds().Dx(), base.Bounds().Dy()) {
		focusSkipped += len(focus)
		focus, focusReason = nil, "canvas_aspect"
	}
	elements := app.PageElements(page)
	var beatSheet *animation.BeatSheet
	var denyIDs map[string]struct{}
	if animation.InferMotionMode(rev) != animation.MotionModeEntrance {
		got := app.PageBeatSheet(
			app.PageBindElements(page, pc.SlideWidthEMU, pc.SlideHeightEMU),
			previewCompileIDs(elements),
			*slide,
			bundle.Timeline.Subtitles,
			durationMS,
		)
		denyIDs = got.DenyIDs
		if got.Sheet.Valid() {
			s := got.Sheet
			beatSheet = &s
			focus = nil
		}
	}
	compiler, err := render.NewCompiler(render.Options{
		Canvas: canvas,
		FPS:    previewFPS,
		// 与导出路径同一个底色：页与页之间的交叉底色在单页里不可见，
		// 但计划里必须给一个值（Validate 拒绝空串）。
		Background:     "#FFFFFF",
		SlideWidthEMU:  pc.SlideWidthEMU,
		SlideHeightEMU: pc.SlideHeightEMU,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "internal", "message": err.Error()})
		return
	}
	// BaseImage 只为通过 IR 的路径校验。浏览器播放读的是下面编码的 data URL，
	// 不把页图投递给渲染服务，也不提交渲染任务。
	plan, err := compiler.Compile([]render.PageInput{{
		Index:          0,
		SlideID:        key.SlideID,
		DurationMs:     durationMS,
		BaseImage:      "page.png",
		Elements:       elements,
		Revision:       rev,
		SegmentOffsets: segmentOffsetsOf(*slide),
		Focus:          focus,
		BeatSheet:      beatSheet,
		GazeDenyIDs:    denyIDs,
	}})
	if err != nil {
		// 编译失败是"这份规格/这页数据有问题"，不是服务故障：
		// 4xx 让前端把原因原样显示，用户才知道该改什么。
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code": "compile_failed", "message": err.Error(),
		})
		return
	}
	if len(plan.Pages) != 1 {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code": "internal", "message": "preview compiled an unexpected page count",
		})
		return
	}

	scene := buildPreviewScene(base, pagePNG, plan, focusSkipped, focusReason)
	h := w.Header()
	// 预览是"此刻这份规格"的结果，缓存它等于让用户看到旧动效。
	h.Set("Cache-Control", "no-store")
	// 计数同时放进响应头：焦点说明以前只读这些头。正文里的同名字段是浏览器播放的来源。
	h.Set("X-Preview-Layers", fmt.Sprintf("%d", scene.LayerCount))
	h.Set("X-Preview-Focus", fmt.Sprintf("%d", scene.FocusCount))
	if focusSkipped > 0 {
		h.Set("X-Preview-Focus-Skipped", fmt.Sprintf("%d", focusSkipped))
		if focusReason != "" {
			h.Set("X-Preview-Focus-Skip-Reason", focusReason)
		}
	}
	writeJSON(w, http.StatusOK, scene)
}

func previewCompileIDs(elements []render.Element) map[string]struct{} {
	if len(elements) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(elements))
	for _, e := range elements {
		if e.ID != "" {
			out[e.ID] = struct{}{}
		}
	}
	return out
}

// pageImageKey 取该页在对象存储里的 PNG 键（按时间轴页序对齐）。
func (p *AnimationPreviewer) pageImageKey(ctx context.Context, tenantID, projectID, timelineKey, slideID string) (string, error) {
	keys, err := resolvePagePngKeys(ctx, p.jobs, p.objects, tenantID, projectID, timelineKey)
	if err != nil {
		return "", err
	}
	bundle, _, err := loadBundle(ctx, p.objects, tenantID, timelineKey)
	if err != nil || bundle == nil || bundle.Timeline == nil {
		return "", nil
	}
	for i, slide := range bundle.Timeline.Slides {
		if slide.SlideID != slideID {
			continue
		}
		if i < len(keys) {
			return keys[i], nil
		}
		return "", nil
	}
	return "", nil
}

func (p *AnimationPreviewer) readObject(ctx context.Context, tenantID, rawKey string) ([]byte, error) {
	key, err := objectstore.Parse(rawKey)
	if err != nil {
		return nil, err
	}
	if err := key.EnsureTenant(tenantID); err != nil {
		return nil, err
	}
	rc, _, err := p.objects.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, previewMaxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > previewMaxImageBytes {
		return nil, errors.New("animation: page image exceeds preview limit")
	}
	return data, nil
}

// previewCanvas 按页图比例定画布，高度固定为成片基准 1080。
//
// 用页图比例而不是固定 16:9：4:3 的页塞进 16:9 画布会被拉伸，预览看起来"字歪了"。
// 高度对齐 1080 是因为位移幅度按画布高 / 1080 缩放，只有高度相同，预览和 1080p 成片
// 的元素才移动同样的距离。宽度取偶数，极端比例夹进 [240,3840]，夹完比例就断了，
// 调用方会因此丢掉焦点而不是把孔开歪。
func previewCanvas(png []byte) design.Canvas {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(png))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return design.Canvas{Width: 1920, Height: previewCanvasHeight}
	}
	return previewCanvasSize(cfg.Width, cfg.Height)
}

func previewCanvasSize(srcW, srcH int) design.Canvas {
	if srcW <= 0 || srcH <= 0 {
		return design.Canvas{Width: 1920, Height: previewCanvasHeight}
	}
	width := int(math.Round(float64(previewCanvasHeight) * float64(srcW) / float64(srcH)))
	if width%2 != 0 {
		width++
	}
	if width < previewCanvasMinWidth {
		width = previewCanvasMinWidth
	}
	if width > previewCanvasMaxWidth {
		width = previewCanvasMaxWidth
	}
	return design.Canvas{Width: width, Height: previewCanvasHeight}
}

// canvasKeepsAspect 判断预览画布是否仍保持页图比例。
//
// 只在「这一页真有焦点」时才值得一问（调用方先判 len(focus) > 0）：它要再解一次
// PNG 头。2px 容差用来吃掉两处取整——previewCanvas 会把奇数宽度 +1，
// 以及 1080 高未必能整除原始比例。
func canvasKeepsAspect(canvas design.Canvas, png []byte) bool {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(png))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		// 解不出页图尺寸时不能说"保持了比例"：焦点框的位置无法核对。
		return false
	}
	return canvasKeepsAspectSize(canvas, cfg.Width, cfg.Height)
}

func canvasKeepsAspectSize(canvas design.Canvas, srcW, srcH int) bool {
	if srcW <= 0 || srcH <= 0 {
		return false
	}
	want := float64(canvas.Height) * float64(srcW) / float64(srcH)
	return math.Abs(float64(canvas.Width)-want) <= 2
}

// previewScene 是浏览器播放这一页需要的全部数据。
//
// 字段与分享清单的一页对齐（image / plate / layers / focus / shots），
// 播放器因此可以复用同一份 evaluateLayer、shotAt 和遮罩采样。
// 图片是 data URL：预览不落盘，也不依赖渲染服务的工作目录。
type previewScene struct {
	FPS             int                     `json:"fps"`
	Width           int                     `json:"width"`
	Height          int                     `json:"height"`
	DurationMs      int64                   `json:"durationMs"`
	Image           string                  `json:"image"`
	Plate           bool                    `json:"plate"`
	Layers          []share.SlideLayer      `json:"layers"`
	Focus           []share.SlideFocus      `json:"focus"`
	Annotations     []share.SlideAnnotation `json:"annotations,omitempty"`
	LookCrop        bool                    `json:"lookCrop,omitempty"`
	LookCropPadPx   int                     `json:"lookCropPadPx,omitempty"`
	ClearRects      []share.SlideBox        `json:"clearRects,omitempty"`
	Shots           []share.SlideShot       `json:"shots"`
	LayerCount      int                     `json:"layerCount"`
	FocusCount      int                     `json:"focusCount"`
	FocusSkipped    int                     `json:"focusSkipped"`
	FocusSkipReason string                  `json:"focusSkipReason,omitempty"`
	EntranceSkipped bool                    `json:"entranceSkipped,omitempty"`
}

// buildPreviewScene 把编译结果变成浏览器能播的场景。
//
// 底板和图层位图与 HTML 导出走同一对函数（BuildPlate / BuildLayerAssets）：
// 只在整页图上做 CSS 位移会在原位留下残影。挖空失败时退回整页图、不带图层，
// 画面至少是对的。镜头裁切失败则整页镜头都不给，与导出的降级相同。
func buildPreviewScene(base image.Image, pagePNG []byte, plan *render.Timeline, focusSkipped int, focusReason string) previewScene {
	page := plan.Pages[0]
	stage := pagePNG
	scene := previewScene{
		FPS:             plan.FPS,
		Width:           plan.Width,
		Height:          plan.Height,
		DurationMs:      page.DurationMs,
		Layers:          []share.SlideLayer{},
		Focus:           previewFocus(page.Focus),
		Annotations:     previewAnnotations(page.Annotations),
		LookCrop:        page.LookCrop,
		LookCropPadPx:   page.LookCropPadPx,
		ClearRects:      previewClearRects(page.ClearRects),
		Shots:           []share.SlideShot{},
		FocusCount:      len(page.Focus),
		FocusSkipped:    focusSkipped,
		FocusSkipReason: focusReason,
	}
	if previewHasAnimatedLayer(page.Layers) {
		base = render.CropSlack(base, plan.Width, plan.Height)
		plateImg, _ := render.BuildPlate(base, page.Layers, plan.Width, plan.Height)
		assets := render.BuildLayerAssets(base, page.Layers, plan.Width, plan.Height)
		if plateImg != nil && len(assets) > 0 {
			if raw, err := encodePreviewPNG(plateImg); err == nil {
				if layers, ok := previewLayerScenes(assets, page.Layers); ok {
					stage = raw
					scene.Plate = true
					scene.Layers = layers
				}
			}
		}
	}
	scene.Image = pngDataURL(stage)
	scene.LayerCount = len(scene.Layers)
	scene.Shots = previewShotScenes(base, page, plan.Width, plan.Height, stage)
	for _, w := range plan.Warnings {
		if w.Code == render.DiagEntranceSkipped {
			scene.EntranceSkipped = true
			break
		}
	}
	return scene
}

func previewHasAnimatedLayer(layers []render.Layer) bool {
	// 与导出 / Remotion 暂存同一条「入场元素层」判据：注视页不挖洞。
	return render.HasEntranceElementLayer(layers)
}

func previewLayerScenes(assets []render.LayerAsset, layers []render.Layer) ([]share.SlideLayer, bool) {
	out := make([]share.SlideLayer, 0, len(assets))
	for _, asset := range assets {
		if asset.Index < 0 || asset.Index >= len(layers) || asset.Image == nil {
			return nil, false
		}
		raw, err := encodePreviewPNG(asset.Image)
		if err != nil {
			return nil, false
		}
		layer := layers[asset.Index]
		box := layer.Box
		if asset.Box.W > 0 && asset.Box.H > 0 {
			box = asset.Box
		}
		out = append(out, share.SlideLayer{
			ElementID: layer.ElementID,
			Kind:      layer.Kind,
			Box:       share.SlideBox{X: box.X, Y: box.Y, W: box.W, H: box.H},
			Image:     pngDataURL(raw),
			Origin:    layer.Origin,
			Clips:     previewClips(layer.Clips),
		})
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func previewClips(clips []render.Motion) []share.Clip {
	if len(clips) == 0 {
		return nil
	}
	out := make([]share.Clip, 0, len(clips))
	for _, clip := range clips {
		out = append(out, share.Clip{
			ClipID:               clip.ClipID,
			Effect:               clip.Effect,
			StartMs:              clip.StartMs,
			DurationMs:           clip.DurationMs,
			Easing:               clip.Easing,
			Props:                clip.Props,
			IsContinuation:       clip.IsContinuation,
			ContinuationOffsetMs: clip.ContinuationOffsetMs,
			ElementID:            clip.ElementID,
			Carry:                clip.Carry,
		})
	}
	return out
}

func previewClearRects(in []render.Box) []share.SlideBox {
	if len(in) == 0 {
		return nil
	}
	out := make([]share.SlideBox, 0, len(in))
	for _, b := range in {
		if b.W <= 0 || b.H <= 0 {
			continue
		}
		out = append(out, share.SlideBox{X: b.X, Y: b.Y, W: b.W, H: b.H})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func previewFocus(in []render.FocusSpan) []share.SlideFocus {
	if len(in) == 0 {
		return []share.SlideFocus{}
	}
	out := make([]share.SlideFocus, 0, len(in))
	for _, span := range in {
		item := share.SlideFocus{
			StartMs: span.StartMs, ElementID: span.ElementID,
			EndMs: span.EndMs, FadeMs: span.FadeMs, VeilAlpha: span.VeilAlpha,
		}
		if span.Rect != nil {
			item.Rect = &share.SlideBox{X: span.Rect.X, Y: span.Rect.Y, W: span.Rect.W, H: span.Rect.H}
		}
		out = append(out, item)
	}
	return out
}

func previewAnnotations(in []render.Annotation) []share.SlideAnnotation {
	if len(in) == 0 {
		return nil
	}
	out := make([]share.SlideAnnotation, 0, len(in))
	for _, a := range in {
		item := share.SlideAnnotation{
			Kind: a.Kind, StartMs: a.StartMs, EndMs: a.EndMs, FadeMs: a.FadeMs,
			ElementID: a.ElementID, Shape: a.Shape, Text: a.Text, SegmentID: a.SegmentID,
		}
		if a.Rect != nil {
			item.Rect = &share.SlideBox{X: a.Rect.X, Y: a.Rect.Y, W: a.Rect.W, H: a.Rect.H}
		}
		out = append(out, item)
	}
	return out
}

// previewShotScenes 产出页内镜头。首镜复用舞台图（有分层时是底板），其余镜头从原页裁切。
// 任一步失败就整页不给镜头：半套镜头会在切换点闪一下，比没有镜头更难看。
func previewShotScenes(base image.Image, page render.Page, canvasW, canvasH int, stagePNG []byte) []share.SlideShot {
	if len(page.Shots) == 0 {
		return []share.SlideShot{}
	}
	out := make([]share.SlideShot, 0, len(page.Shots))
	for i, shot := range page.Shots {
		item := share.SlideShot{
			Index:       shot.Index,
			StartMs:     shot.StartMs,
			DurationMs:  shot.DurationMs,
			CrossFadeMs: shot.CrossFadeMs,
			Camera:      previewCamera(shot.Camera),
		}
		if i == 0 {
			item.Image = pngDataURL(stagePNG)
			out = append(out, item)
			continue
		}
		if shot.Crop == nil || base == nil {
			return []share.SlideShot{}
		}
		cropped, err := render.CropShot(base, *shot.Crop, canvasW, canvasH)
		if err != nil {
			return []share.SlideShot{}
		}
		raw, err := encodePreviewPNG(cropped)
		if err != nil {
			return []share.SlideShot{}
		}
		item.Image = pngDataURL(raw)
		item.Crop = &share.SlideBox{X: shot.Crop.X, Y: shot.Crop.Y, W: shot.Crop.W, H: shot.Crop.H}
		out = append(out, item)
	}
	return out
}

func previewCamera(kfs []render.CameraKeyframe) []share.CameraKeyframe {
	if len(kfs) == 0 {
		return nil
	}
	out := make([]share.CameraKeyframe, len(kfs))
	for i, kf := range kfs {
		out[i] = share.CameraKeyframe{AtMs: kf.AtMs, X: kf.X, Y: kf.Y, Scale: kf.Scale}
	}
	return out
}

func encodePreviewPNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func pngDataURL(raw []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
}

// segmentOffsetsOf 把分段绝对时间换算成相对本页起点的毫秒偏移。
// 与导出的 AppAnimationPageAssembler 同一规则：负偏移归零（它只可能来自错位的时间轴），
// 键用页内 id（时间轴给的是「页:段」全局键，规格里存的是页内 id）。
func segmentOffsetsOf(slide media.SlideCue) map[string]int64 {
	if len(slide.Segments) == 0 {
		return nil
	}
	offsets := make(map[string]int64, len(slide.Segments))
	for _, seg := range slide.Segments {
		local := media.UnscopedSegmentID(slide.SlideID, seg.SegmentID)
		if local == "" {
			continue
		}
		off := (seg.StartUS - slide.StartUS) / 1000
		if off < 0 {
			off = 0
		}
		offsets[local] = off
	}
	if len(offsets) == 0 {
		return nil
	}
	return offsets
}
