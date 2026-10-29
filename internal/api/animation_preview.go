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
	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/animation/animstore"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/design"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/project"
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
	// previewBudget 是"提交→拿到字节"的总预算，也是超时提示的阈值。
	previewBudget = 3 * time.Second
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

// WithBudget 覆盖预览预算（测试用）。
func (p *AnimationPreviewer) WithBudget(d time.Duration) *AnimationPreviewer {
	if p != nil && d > 0 {
		p.budget = d
	}
	return p
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
		Elements:       previewElements(page),
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
	// 图层数 > 0 说明这次预览含元素级动效，而导出侧目前会跳过它们（tasks 2.0b）。
	// 前端口径：看到这个头就把差别写在预览旁边，而不是让用户导出一版静态片再来问。
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

// previewElements 把页内元素转成渲染几何。
//
// kind 用 animdefault.Anchors（与预设生成同一套标题/要点分类），Bounds 从形状树按 id 回填：
// 分类只走一条路径，几何也只有一个来源，两处各遍历一遍迟早会出现"第 2 条要点"在两个列表里不是同一条。
func previewElements(page *project.Page) []render.Element {
	bounds := make(map[string]*project.Box)
	var walk func([]*project.Shape)
	walk = func(shapes []*project.Shape) {
		for _, sh := range shapes {
			if sh == nil || sh.ID == "" {
				continue
			}
			if sh.Bounds != nil {
				bounds[sh.ID] = sh.Bounds
			}
			if len(sh.Children) > 0 {
				walk(sh.Children)
			}
		}
	}
	walk(page.Shapes)

	anchors := animdefault.Anchors(page)
	out := make([]render.Element, 0, len(anchors))
	for _, a := range anchors {
		box := bounds[a.ID]
		if box == nil {
			continue
		}
		out = append(out, render.Element{
			ID:   a.ID,
			Kind: a.Kind,
			Bounds: &render.EMUBox{
				X: box.X, Y: box.Y, Width: box.Width, Height: box.Height,
			},
		})
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
// 与导出的 AppAnimationPageAssembler 同一规则：负偏移归零（它只可能来自错位的时间轴）。
func segmentOffsetsOf(slide media.SlideCue) map[string]int64 {
	if len(slide.Segments) == 0 {
		return nil
	}
	offsets := make(map[string]int64, len(slide.Segments))
	for _, seg := range slide.Segments {
		if seg.SegmentID == "" {
			continue
		}
		off := (seg.StartUS - slide.StartUS) / 1000
		if off < 0 {
			off = 0
		}
		offsets[seg.SegmentID] = off
	}
	if len(offsets) == 0 {
		return nil
	}
	return offsets
}
