package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png" // 只为拿页图尺寸；解码整张图在预览里没必要
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
)

// 单页预览（tasks 3.3）。
//
// 契约只有一句：**只提交这一页的 IR**（1 页），≤3s 内给结果，超时给明确错误。
// 这条边界不是性能小心思，而是成本边界：全片预览意味着一次编辑动作渲一遍全片，
// 用户改第 3 条要点的锚点也要等整片——按 Q1 的决策首版不做。
//
// 三个刻意的取舍：
//
//  1. **预览用的是已保存的规格**，不是请求体里的草案。草案要预览就得把整份规格
//     复述一遍（校验、身份、语言都对不上时该信谁），而保存本来只是一次 PUT。
//     好处是预览与导出看的是同一份数据，用户不会遇到"预览对、导出不对"。
//  2. **预览分辨率低于导出**（960 宽 / 24fps vs 1080p / 30fps）。预览要的是
//     "第 4 段开始时这行字出现了吗"，不是像素级校对；按导出参数渲一页在纯 CPU
//     渲染器上轻易吃掉 3s 预算，那样每次编辑都要等超时提示。
//  3. **超时即取消**。不取消的话渲染服务会继续把这一页渲完，占着一个工作槽；
//     用户连点五次预览就等于排了五个必然被丢掉的任务。

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
	// previewCanvasWidth 是预览画布宽（高度按页图比例推）。
	previewCanvasWidth = 960
	previewFPS         = 24
	// previewMaxImageBytes 限制读入的页图大小：一张 1080p PNG 通常 <4MB，
	// 32MB 只用来挡住"把这个端点当文件读取器"的用法。
	previewMaxImageBytes = 32 << 20
)

// AnimationPreviewer 提供单页动画预览。
//
// 依赖缺一就不注册路由（与动画规格端点同一约定）：预览不是可选增强，
// 它要么完整可用，要么不存在——半可用的预览会让用户以为"动效没生效"。
type AnimationPreviewer struct {
	store    animstore.Store
	objects  objectstore.ObjectStore
	jobs     JobCreator
	assets   app.AssetPublisher
	renderer *render.Client
	// pages 提供页文档：预览需要元素的 EMU 框才能把元素级轨道编译成图层。
	pages AnimationPageResolver
	// budget 可覆盖（测试用短预算），0 用 previewBudget。
	budget time.Duration
}

// NewAnimationPreviewer 构造预览器。任一依赖为 nil 时返回 nil，调用方据此不注册路由。
func NewAnimationPreviewer(store animstore.Store, objects objectstore.ObjectStore, jobs JobCreator,
	assets app.AssetPublisher, renderer *render.Client, pages AnimationPageResolver) *AnimationPreviewer {
	if store == nil || objects == nil || jobs == nil || assets == nil || renderer == nil || pages == nil {
		return nil
	}
	return &AnimationPreviewer{
		store: store, objects: objects, jobs: jobs, assets: assets, renderer: renderer, pages: pages,
	}
}

// WithBudget 覆盖预览预算（测试用短预算，或部署方按硬件调整）。
func (p *AnimationPreviewer) WithBudget(d time.Duration) *AnimationPreviewer {
	if p != nil && d > 0 {
		p.budget = d
	}
	return p
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
}

type previewRequest struct {
	TimelineKey string `json:"timelineKey"`
}

func (p *AnimationPreviewer) preview(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthenticated"})
		return
	}
	key, ok := animationKeyFromRequest(w, r, principal.TenantID)
	if !ok {
		return
	}
	var req previewRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
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

	// 规格是预览的输入，不是可选项：没有规格就没有"预览什么"。
	rev, err := p.store.Get(r.Context(), key.Normalize())
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
	png, err := p.readObject(r.Context(), principal.TenantID, pngKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "internal", "message": err.Error()})
		return
	}
	rel, err := p.assets.Publish(r.Context(), "image/png", png)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "asset_publish_failed", "message": err.Error()})
		return
	}

	canvas := previewCanvas(png)
	compiler, err := render.NewCompiler(render.Options{
		Canvas: canvas,
		FPS:    previewFPS,
		// 与导出路径同一个底色：页与页之间的交叉底色在单页里不可见，
		// 但计划里必须给一个值（Validate 拒绝空串）。
		Background: "#FFFFFF",
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "internal", "message": err.Error()})
		return
	}
	plan, err := compiler.Compile([]render.PageInput{{
		Index:          0,
		SlideID:        key.SlideID,
		DurationMs:     durationMS,
		BaseImage:      rel,
		Elements:       app.PageElements(page),
		Revision:       rev,
		SegmentOffsets: segmentOffsetsOf(*slide),
	}})
	if err != nil {
		// 编译失败是"这份规格/这页数据有问题"，不是服务故障：
		// 4xx 让前端把原因原样显示，用户才知道该改什么。
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code": "compile_failed", "message": err.Error(),
		})
		return
	}

	budget := p.budget
	if budget <= 0 {
		budget = previewBudget
	}
	// 预算覆盖提交 + 等待 + 下载：只卡等待的话，慢一次提交就能让接口超过 3s。
	ctx, cancel := context.WithTimeout(r.Context(), budget)
	defer cancel()

	jobID, err := p.renderer.Submit(ctx, plan)
	if err != nil {
		p.failBudget(ctx, w, "renderer_unavailable", err)
		return
	}
	status, err := p.renderer.Wait(ctx, jobID, 100*time.Millisecond, budget)
	if err != nil {
		// 超时与网络错误的处理不同：超时必须取消任务（否则渲染服务继续占槽），
		// 而且要给"稍后重试"的明确提示，不是空转。
		p.cancel(jobID)
		p.failBudget(ctx, w, "preview_timeout", err)
		return
	}
	if status.State != render.StateSucceeded {
		msg := status.Error
		if msg == "" {
			msg = string(status.State)
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"code": "preview_failed", "message": msg})
		return
	}

	var buf bytes.Buffer
	if _, err := p.renderer.DownloadArtifact(ctx, jobID, render.SegmentName(0), &buf); err != nil {
		p.cancel(jobID)
		p.failBudget(ctx, w, "preview_failed", err)
		return
	}
	if buf.Len() == 0 {
		writeJSON(w, http.StatusBadGateway, map[string]any{"code": "preview_empty", "message": "渲染服务返回了空片段"})
		return
	}

	h := w.Header()
	h.Set("Content-Type", "video/mp4")
	h.Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
	// 预览是"此刻这份规格"的结果，缓存它等于让用户看到旧动效。
	h.Set("Cache-Control", "no-store")
	if status.PlanHash != "" {
		h.Set("X-Render-Plan-Hash", status.PlanHash)
	}
	// 图层数是"这一页有几个元素在动"的唯一可核对的数字：几何读不到时会静默退化成
	// 只有整页动效，而画面上看起来就是"动效没生效"。前端据此说明预览含几个元素动效。
	// 导出侧 2.0b 起读同一份解析产物，两边图层数应当一致（画布分辨率/帧率不同）。
	h.Set("X-Preview-Layers", fmt.Sprintf("%d", len(plan.Pages[0].Layers)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// failBudget 区分"预算用尽"与"其它失败"。
//
// ctx 已过期时不能报 502 renderer_unavailable——那会让用户去查服务，
// 而真实原因是这一页渲得太慢。两种原因的下一步动作完全不同。
func (p *AnimationPreviewer) failBudget(ctx context.Context, w http.ResponseWriter, code string, err error) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{
			"code":    "preview_timeout",
			"message": "预览超时：这一页渲得太慢，稍后重试或直接导出查看",
		})
		return
	}
	status := http.StatusBadGateway
	if code == "renderer_unavailable" {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, map[string]any{"code": code, "message": err.Error()})
}

func (p *AnimationPreviewer) cancel(jobID string) {
	// 用后台上下文：请求上下文已经取消，拿它去发取消请求等于不取消。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.renderer.Cancel(ctx, jobID)
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

// previewCanvas 按页图比例定画布。用页图比例而不是固定 16:9：
// 4:3 的页塞进 16:9 画布会被拉伸，预览看起来"字歪了"，用户会以为动效做坏了。
func previewCanvas(png []byte) design.Canvas {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(png))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return design.Canvas{Width: previewCanvasWidth, Height: previewCanvasWidth * 9 / 16}
	}
	height := int(math.Round(float64(previewCanvasWidth) * float64(cfg.Height) / float64(cfg.Width)))
	if height%2 != 0 {
		height++ // H.264 yuv420p 要求偶数
	}
	if height < 240 {
		height = 240
	}
	if height > 1920 {
		height = 1920
	}
	return design.Canvas{Width: previewCanvasWidth, Height: height}
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
