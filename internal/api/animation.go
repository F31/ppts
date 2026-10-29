package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/animation/animstore"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/tenant"
)

// maxAnimationBodyBytes 限制请求体：正常规格 ≤256KB（V12），留一倍余量给 DTO 包装。
const maxAnimationBodyBytes = 512 << 10

// AnimationPageResolver 提供保存动画规格所需的页级上下文（元素锚点、讲稿分段、页时长）。
//
// 校验必须发生在服务端（设计方案 §5.4），而校验需要"这一页现在长什么样"。
// 把它抽成接口，而不是让 handler 直接读存储：一是有助于测试，二是 M2 接入时间轴后
// 只需要换一个实现（页时长从 0=未知 变成真实值），handler 不必改。
type AnimationPageResolver interface {
	Resolve(ctx context.Context, tenantID, userID, projectID string, sourceRevisionNo int, slideID, language string) (*project.Page, animation.PageContext, error)
}

// animationResolver 从对象存储里的 extracted document.json 还原页与元素锚点，
// 并从最近一次成功的配音时间轴取页时长。
//
// 页时长的来源只有时间轴（EndUS-StartUS）——它是"这一页到底播多久"的唯一事实，
// 讲稿字数估算、原始 PPT 的 advTm 都不是。取不到（还没配音）时给 0，
// 0 的语义是"暂不判上界"：保存照常进行（V3 跳过），渲染前再用真实时长校验一次。
//
// 之前这里恒返回 0，导致 `?default=true`（按预设生成一份默认规格）永远走
// duration_unknown 分支——逐页编辑器一打开就没有可编辑的起点。接上时间轴后
// 它才真的可用，而且用户改强度/锚点时校验依据的是真实页长。
type animationResolver struct {
	projects project.ProjectStore
	objects  objectstore.ObjectStore
	scripts  narration.Store
	jobs     JobCreator
}

func newAnimationResolver(projects project.ProjectStore, objects objectstore.ObjectStore, scripts narration.Store, jobs JobCreator) *animationResolver {
	return &animationResolver{projects: projects, objects: objects, scripts: scripts, jobs: jobs}
}

func (r *animationResolver) Resolve(ctx context.Context, tenantID, userID, projectID string, sourceRevisionNo int, slideID, language string) (*project.Page, animation.PageContext, error) {
	var empty animation.PageContext
	if r.projects == nil || r.objects == nil {
		return nil, empty, animation.ErrNotFound
	}
	revNo := sourceRevisionNo
	if revNo <= 0 {
		proj, err := r.projects.GetProject(ctx, tenantID, userID, projectID)
		if err != nil {
			return nil, empty, animation.ErrNotFound
		}
		revNo = proj.CurrentRevision
	}
	docs, err := loadProjectDocuments(ctx, r.projects, r.objects, tenantID, projectID, revNo, revNo)
	if err != nil {
		return nil, empty, err
	}
	doc := docs[revNo]
	if doc == nil {
		return nil, empty, animation.ErrNotFound
	}
	var page *project.Page
	for _, pg := range doc.Pages {
		if pg != nil && pg.SlideID == slideID {
			page = pg
			break
		}
	}
	if page == nil {
		return nil, empty, animation.ErrNotFound
	}
	pc := animation.PageContext{DurationMS: r.pageDurationMS(ctx, tenantID, projectID, slideID), Elements: animdefault.PageAnchors(page)}
	if r.scripts != nil {
		// 讲稿缺失不是错误：还没有讲稿时就没有 withSegment 可锚，规格里的相对锚点会被 V7 拒绝，
		// 这正是需要告诉用户的事实（"先写讲稿再绑分段"），而不是掩盖成 500。
		if script, err := r.scripts.Get(ctx, tenantID, projectID, revNo, slideID, language); err == nil && script != nil {
			for _, seg := range script.Segments {
				if seg != nil && seg.SegmentID != "" {
					pc.SegmentIDs = append(pc.SegmentIDs, seg.SegmentID)
				}
			}
		}
	}
	return page, pc, nil
}

// pageDurationMS 从最近一次成功的配音时间轴取该页时长；取不到返回 0。
//
// 任何一步失败都只返回 0，不返回 error：动画规格的保存不该因为"时间轴读不到"
// 而失败——那会把一次存储抖动变成"用户改不动动效"。0 的语义本就表示未知，
// 上层据此跳过上界校验并标记 needsDurationRecheck。
func (r *animationResolver) pageDurationMS(ctx context.Context, tenantID, projectID, slideID string) int64 {
	if r.jobs == nil || r.objects == nil {
		return 0
	}
	job, err := r.jobs.LatestSucceededJob(ctx, tenantID, projectID, string(pipeline.KindNarration))
	if err != nil || job == nil {
		return 0
	}
	ref, err := r.jobs.StepResultRef(tenant.WithContext(ctx, tenantID), job.ID, "timeline")
	if err != nil || ref == "" {
		return 0
	}
	bundle, _, err := loadBundle(ctx, r.objects, tenantID, ref)
	if err != nil || bundle == nil || bundle.Timeline == nil {
		return 0
	}
	for _, cue := range bundle.Timeline.Slides {
		if cue.SlideID != slideID {
			continue
		}
		if d := (cue.EndUS - cue.StartUS) / 1000; d > 0 {
			return d
		}
		return 0
	}
	return 0
}

// AnimationHandler 提供单页动画规格的读写（REST，与 /api/context-rules 同一约定）。
//
// 为什么是 REST 而不是 ConnectRPC：这一组是资源式 CRUD（一页一份规格），与
// /api/context-rules 同形；业务 RPC 面继续由 Connect 承载。接口前缀用 /api/ 与既有
// REST 端点保持一致，避免出现第三种前缀。
type AnimationHandler struct {
	store   animstore.Store
	pages   AnimationPageResolver
	enabled bool
}

// NewAnimationHandler 创建 handler。store 为 nil 时不注册任何路由（等价未上线）。
func NewAnimationHandler(store animstore.Store, pages AnimationPageResolver, enabled bool) *AnimationHandler {
	return &AnimationHandler{store: store, pages: pages, enabled: enabled}
}

// Register 挂载路由。读路径始终可用（便于灰度前验证契约），写路径受 enabled 控制。
func (h *AnimationHandler) Register(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
	if h == nil || h.store == nil {
		return
	}
	mux.Handle("GET /api/projects/{projectId}/slides/{slideId}/animation", auth(http.HandlerFunc(h.get)))
	// 逐页编辑器需要的页级上下文（可挂元素、分段、页时长、预设）。读路径，不受写入开关影响：
	// 开关关闭时编辑器据此把"配置动效"整块说明清楚，而不是先渲染出一个空面板。
	mux.Handle("GET /api/projects/{projectId}/slides/{slideId}/animation/anchors", auth(http.HandlerFunc(h.anchors)))
	mux.Handle("PUT /api/projects/{projectId}/slides/{slideId}/animation", auth(http.HandlerFunc(h.put)))
}

type animationSpecDTO struct {
	SchemaVersion    string             `json:"schemaVersion"`
	SlideID          string             `json:"slideId"`
	Language         string             `json:"language"`
	SourceRevisionNo int                `json:"sourceRevisionNo"`
	Revision         int64              `json:"revision"`
	Status           animation.Status   `json:"status"`
	PresetID         string             `json:"presetId"`
	Intensity        float64            `json:"intensity"`
	Tracks           []*animation.Track `json:"tracks"`
	// NeedsDurationRecheck 为 true 表示保存时页时长未知（尚无配音），
	// 渲染前必须用真实时长重新校验（设计方案 §5.4 V3 的编辑期例外）。
	NeedsDurationRecheck bool `json:"needsDurationRecheck,omitempty"`
}

func animationDTO(rev *animation.Revision, needsRecheck bool) animationSpecDTO {
	return animationSpecDTO{
		SchemaVersion:        animation.SchemaVersion,
		SlideID:              rev.SlideID,
		Language:             rev.Language,
		SourceRevisionNo:     rev.SourceRevisionNo,
		Revision:             rev.Revision,
		Status:               rev.Status,
		PresetID:             rev.PresetID,
		Intensity:            rev.Intensity,
		Tracks:               rev.Tracks,
		NeedsDurationRecheck: needsRecheck,
	}
}

type animationPutRequest struct {
	ExpectedRevision *int64             `json:"expectedRevision"`
	PresetID         string             `json:"presetId"`
	Intensity        float64            `json:"intensity"`
	Tracks           []*animation.Track `json:"tracks"`
}

func (h *AnimationHandler) get(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthenticated"})
		return
	}
	key, ok := animationKeyFromRequest(w, r, principal.TenantID)
	if !ok {
		return
	}
	rev, err := h.store.Get(r.Context(), key)
	if err == nil {
		writeJSON(w, http.StatusOK, animationDTO(rev, false))
		return
	}
	if !errors.Is(err, animation.ErrNotFound) {
		writeAnimationError(w, err)
		return
	}
	if r.URL.Query().Get("default") != "true" {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "not_found"})
		return
	}
	if h.pages == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_unavailable"})
		return
	}
	page, pc, rerr := h.pages.Resolve(r.Context(), principal.TenantID, principal.UserID, key.ProjectID, key.SourceRevisionNo, key.SlideID, key.Language)
	if rerr != nil {
		writeAnimationError(w, rerr)
		return
	}
	if page == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "page_not_found"})
		return
	}
	if pc.DurationMS <= 0 {
		// 默认规格要按页时长压时间轴；没有时长就不猜，直接告诉用户先配音。
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code":    "duration_unknown",
			"message": "该页还没有配音，无法生成默认动画；请先生成配音，或手动创建动画",
		})
		return
	}
	rev, derr := animdefault.DefaultSpec(page, animdefault.DefaultOptions{
		SlideID: key.SlideID, Language: key.Language, DurationMS: pc.DurationMS,
		SegmentIDs: pc.SegmentIDs, PresetID: r.URL.Query().Get("preset"),
	})
	if derr != nil {
		writeAnimationError(w, derr)
		return
	}
	rev.TenantID = key.TenantID
	rev.ProjectID = key.ProjectID
	rev.SourceRevisionNo = key.SourceRevisionNo
	writeJSON(w, http.StatusOK, animationDTO(rev, false))
}

func (h *AnimationHandler) put(w http.ResponseWriter, r *http.Request) {
	if !h.enabled {
		// 开关关闭时返回 404 而不是 403：不暴露能力存在，避免外部探测与半成品文案。
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "not_found"})
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthenticated"})
		return
	}
	key, ok := animationKeyFromRequest(w, r, principal.TenantID)
	if !ok {
		return
	}
	if h.pages == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_unavailable"})
		return
	}

	var req animationPutRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAnimationBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_body", "message": err.Error()})
		return
	}
	if req.ExpectedRevision == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code": "missing_expected_revision", "message": "expectedRevision 必填（新建传 0）",
		})
		return
	}

	_, pc, rerr := h.pages.Resolve(r.Context(), principal.TenantID, principal.UserID, key.ProjectID, key.SourceRevisionNo, key.SlideID, key.Language)
	if rerr != nil {
		writeAnimationError(w, rerr)
		return
	}

	intensity := req.Intensity
	if intensity == 0 {
		// 省略 intensity 视为默认值 1.0；显式传 0 同样落到这里，而 0 本身是非法值，
		// 让它变成默认值比报"必须 ≥0.5"更符合"没填就用默认"的直觉。
		intensity = 1.0
	}
	rev := &animation.Revision{
		TenantID:         key.TenantID,
		ProjectID:        key.ProjectID,
		SlideID:          key.SlideID,
		Language:         key.Language,
		SourceRevisionNo: key.SourceRevisionNo,
		Status:           animation.StatusDraft,
		PresetID:         req.PresetID,
		Intensity:        intensity,
		Tracks:           req.Tracks,
	}
	if err := rev.Validate(pc); err != nil {
		writeAnimationError(w, err)
		return
	}
	saved, err := h.store.Put(r.Context(), rev, *req.ExpectedRevision)
	if err != nil {
		writeAnimationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, animationDTO(saved, pc.DurationMS <= 0))
}

// animationAnchorDTO 是编辑器可挂动效的一个元素。
//
// 带 text 的理由：面板里列"第 2 条要点"比列形状 ID 有用得多，
// 而形状 ID 在页面上不可见，用户没有任何办法把它和具体那行字对上。
type animationAnchorDTO struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Text        string `json:"text,omitempty"`
	Index       int    `json:"index"`
	BulletIndex int    `json:"bulletIndex,omitempty"`
}

// animationSegmentDTO 是讲稿分段的引用（只给 id 与序号，正文前端已有）。
type animationSegmentDTO struct {
	SegmentID string `json:"segmentId"`
	Index     int    `json:"index"`
}

// animationPresetDTO 是预设的展示信息。强度缺省值随预设走，前端据此初始化滑块。
type animationPresetDTO struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	Description string  `json:"description"`
	Intensity   float64 `json:"intensity"`
}

type animationAnchorsDTO struct {
	SlideID    string                `json:"slideId"`
	Language   string                `json:"language"`
	DurationMs int64                 `json:"durationMs"`
	Elements   []animationAnchorDTO  `json:"elements"`
	Segments   []animationSegmentDTO `json:"segments"`
	Presets    []animationPresetDTO  `json:"presets"`
}

// anchors 返回逐页编辑器所需的页级上下文。
//
// 为什么不并进 GET /animation（规格本体）：规格可能还不存在（未配置过动效的页 404），
// 而锚点在"还没配置"时恰恰最需要。把两件事放在一个响应里，就得给 404 加语义，
// 或者让调用方从错误体里读数据——两条都不好读。
func (h *AnimationHandler) anchors(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthenticated"})
		return
	}
	key, ok := animationKeyFromRequest(w, r, principal.TenantID)
	if !ok {
		return
	}
	if h.pages == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_unavailable"})
		return
	}
	page, pc, err := h.pages.Resolve(r.Context(), principal.TenantID, principal.UserID, key.ProjectID, key.SourceRevisionNo, key.SlideID, key.Language)
	if err != nil {
		writeAnimationError(w, err)
		return
	}
	if page == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "page_not_found"})
		return
	}

	texts := make(map[string]string)
	var walk func([]*project.Shape)
	walk = func(shapes []*project.Shape) {
		for _, sh := range shapes {
			if sh == nil || sh.ID == "" {
				continue
			}
			if t := strings.TrimSpace(sh.Text); t != "" {
				texts[sh.ID] = clipAnchorText(t)
			}
			if len(sh.Children) > 0 {
				walk(sh.Children)
			}
		}
	}
	walk(page.Shapes)

	// 用 animdefault.Anchors：与"按预设生成默认规格"同一套分类与顺序。
	// 用 PageAnchors 的话每个文字元素都是 shape，面板只能显示"形状 1/2/3"，
	// 而用户改的是"第 2 条要点"——两套编号必然错位。
	resp := animationAnchorsDTO{
		SlideID: key.SlideID, Language: key.Language, DurationMs: pc.DurationMS,
		Elements: make([]animationAnchorDTO, 0, len(pc.Elements)),
		Segments: make([]animationSegmentDTO, 0, len(pc.SegmentIDs)),
		Presets:  presetDTOs(),
	}
	anchors := animdefault.Anchors(page)
	bullets := 0
	for i, a := range anchors {
		bullet := 0
		if a.Kind == animation.KindBullet {
			bullets++
			bullet = bullets
		}
		resp.Elements = append(resp.Elements, animationAnchorDTO{
			ID: a.ID, Kind: a.Kind, Text: texts[a.ID], Index: i + 1, BulletIndex: bullet,
		})
	}
	for i, id := range pc.SegmentIDs {
		resp.Segments = append(resp.Segments, animationSegmentDTO{SegmentID: id, Index: i + 1})
	}
	writeJSON(w, http.StatusOK, resp)
}

func presetDTOs() []animationPresetDTO {
	presets := animation.Catalog().Presets()
	out := make([]animationPresetDTO, 0, len(presets))
	for _, p := range presets {
		out = append(out, animationPresetDTO{ID: p.ID, Label: p.Label, Description: p.Description, Intensity: p.Intensity})
	}
	return out
}

// clipAnchorText 截断过长文本：面板只用来让用户认出"是哪一条"，
// 整段正文会把侧栏撑成一张讲稿页。
func clipAnchorText(s string) string {
	const maxRunes = 40
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}

func animationKeyFromRequest(w http.ResponseWriter, r *http.Request, tenantID string) (animation.Key, bool) {
	projectID := strings.TrimSpace(r.PathValue("projectId"))
	slideID := strings.TrimSpace(r.PathValue("slideId"))
	if projectID == "" || slideID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_path"})
		return animation.Key{}, false
	}
	sourceRevisionNo := 0
	if raw := r.URL.Query().Get("sourceRevisionNo"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_source_revision"})
			return animation.Key{}, false
		}
		sourceRevisionNo = n
	}
	language := r.URL.Query().Get("language")
	if language == "" {
		language = "zh-CN"
	}
	return animation.Key{
		TenantID: tenantID, ProjectID: projectID, SlideID: slideID,
		Language: language, SourceRevisionNo: sourceRevisionNo,
	}, true
}

// writeAnimationError 把领域错误映射成 HTTP 语义。规则编号（V1–V12）原样返回，
// 前端按编号映射文案，不复制一套校验逻辑（设计方案 §5.4）。
func writeAnimationError(w http.ResponseWriter, err error) {
	var invalidKey *animation.ValidationError
	if errors.As(err, &invalidKey) {
		status := invalidKey.Status
		if status == 0 {
			status = http.StatusBadRequest
		}
		payload := map[string]any{
			"code":    "validation_failed",
			"rule":    invalidKey.Rule,
			"message": invalidKey.Message,
		}
		if invalidKey.Field != "" {
			payload["field"] = invalidKey.Field
		}
		if invalidKey.Hint != "" {
			payload["hint"] = invalidKey.Hint
		}
		writeJSON(w, status, payload)
		return
	}
	var conflict *animation.ErrConflict
	switch {
	case errors.Is(err, animation.ErrLocked):
		writeJSON(w, http.StatusConflict, map[string]any{"code": "locked", "message": "动画规格已锁定，需先解锁"})
	case errors.As(err, &conflict):
		payload := map[string]any{"code": "conflict", "message": "版本冲突，请拉取最新内容后重试"}
		if conflict.Latest != nil {
			payload["latest"] = animationDTO(conflict.Latest, false)
		}
		writeJSON(w, http.StatusConflict, payload)
	case errors.Is(err, animation.ErrStatusTransition):
		writeJSON(w, http.StatusConflict, map[string]any{"code": "invalid_status_transition", "message": err.Error()})
	case errors.Is(err, animation.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "not_found"})
	case errors.Is(err, animation.ErrInvalidKey):
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_key", "message": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "internal", "message": fmt.Sprintf("%v", err)})
	}
}
