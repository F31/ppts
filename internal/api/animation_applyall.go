package api

// 「一键动效」的服务端落点：整份 deck 一次套用同一预设。
//
// 为什么单独一个端点，而不是让前端循环调 GET ?default=true&preset=x + PUT：
//   1. N 页就是 2N 次请求，中途失败会留下「前 10 页换了风格、后 13 页没换」的半成品，
//      而且前端没有事务能力把它回滚；
//   2. 「跳过人手改过的页」是存储侧判据（TriggerSource=manual / approved / locked），
//      让前端判断等于把业务规则复制一份，必然走偏；
//   3. 主流的整份套用（WPS 智能动画 / Canva Magic Animate）都是一次动作，
//      逐页精修是之后的例外操作，不该反过来成为必经路径。
//
// 生成口径刻意复用逐页端点：animdefault.DefaultSpecReport 出规格、bind.Run 做规则
// 预填、animation.Revision.Validate 校验、animstore.Put 落库。这里不重算任何动画参数，
// 因此「一键套用」与「打开某一页手动选同一预设」得到的规格完全一致。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/project"
)

const maxApplyAllBodyBytes = 8 << 10

// AnimationDeckResolver 提供整份 deck 的页序（真实放映顺序）。
//
// 只要页 ID 列表，不要 Page 对象：批量端点对每页仍走 AnimationPageResolver.Resolve，
// 保证「一键」与「逐页」拿到的元素分类、分段、页时长是同一份数据。
type AnimationDeckResolver interface {
	SlideIDs(ctx context.Context, tenantID, userID, projectID string, sourceRevisionNo int) ([]string, error)
}

// SlideIDs 从 extracted.json 读页序。animationResolver 已经持有 projects/objects，
// 直接实现这个窄接口，装配处不必再准备第二套依赖。
func (r *animationResolver) SlideIDs(ctx context.Context, tenantID, userID, projectID string, sourceRevisionNo int) ([]string, error) {
	if r == nil || r.projects == nil || r.objects == nil {
		return nil, animation.ErrNotFound
	}
	revNo := sourceRevisionNo
	if revNo <= 0 {
		proj, err := r.projects.GetProject(ctx, tenantID, userID, projectID)
		if err != nil {
			return nil, err
		}
		revNo = proj.CurrentRevision
	}
	docs, err := loadProjectDocuments(ctx, r.projects, r.objects, tenantID, projectID, revNo, revNo)
	if err != nil {
		return nil, err
	}
	doc := docs[revNo]
	if doc == nil {
		return nil, animation.ErrNotFound
	}
	pages := make([]*project.Page, 0, len(doc.Pages))
	for _, pg := range doc.Pages {
		if pg != nil && strings.TrimSpace(pg.SlideID) != "" {
			pages = append(pages, pg)
		}
	}
	sort.SliceStable(pages, func(i, j int) bool { return pages[i].Index < pages[j].Index })
	ids := make([]string, 0, len(pages))
	for _, pg := range pages {
		ids = append(ids, pg.SlideID)
	}
	return ids, nil
}

type applyAllRequest struct {
	PresetID  string `json:"presetId"`
	Overwrite bool   `json:"overwrite,omitempty"`
	// Prefill 默认开启：规则预填是同步的、不调模型，却能让动效跟着讲稿走
	// （「讲到哪条出现哪条」）。显式传 false 才退回纯顺序淡入。
	Prefill *bool `json:"prefill,omitempty"`
}

type applyAllSlideResult struct {
	SlideID  string `json:"slideId"`
	Status   string `json:"status"` // applied | skipped
	Reason   string `json:"reason,omitempty"`
	Revision int64  `json:"revision,omitempty"`
}

type applyAllResponse struct {
	PresetID string                `json:"presetId"`
	Total    int                   `json:"total"`
	Applied  int                   `json:"applied"`
	Skipped  int                   `json:"skipped"`
	Results  []applyAllSlideResult `json:"results"`
}

func (h *AnimationHandler) applyAll(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthenticated"})
		return
	}
	// 与单页 PUT 同一道能力闸：灰度未开时返回 404，不暴露半成品能力。
	if !h.gate.Resolve(r.Context(), principal.TenantID).Enabled {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "not_found"})
		return
	}
	projectID, revNo, language, ok := deckParamsFromRequest(w, r)
	if !ok {
		return
	}
	if h.pages == nil || h.deck == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_unavailable"})
		return
	}

	var req applyAllRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxApplyAllBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_body", "message": err.Error()})
		return
	}
	req.PresetID = strings.TrimSpace(req.PresetID)
	if req.PresetID == "" {
		req.PresetID = animdefault.DefaultPresetID
	}
	if _, ok := animation.Catalog().Preset(req.PresetID); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_preset", "message": "未知的动效预设"})
		return
	}
	prefill := true
	if req.Prefill != nil {
		prefill = *req.Prefill
	}

	slideIDs, err := h.deck.SlideIDs(r.Context(), principal.TenantID, principal.UserID, projectID, revNo)
	if err != nil {
		writeAnimationError(w, err)
		return
	}
	if len(slideIDs) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code": "no_slides", "message": "这份演示文稿还没有可应用动效的页面",
		})
		return
	}

	resp := applyAllResponse{
		PresetID: req.PresetID, Total: len(slideIDs),
		Results: make([]applyAllSlideResult, 0, len(slideIDs)),
	}
	for _, slideID := range slideIDs {
		key := animation.Key{
			TenantID: principal.TenantID, ProjectID: projectID, SlideID: slideID,
			Language: language, SourceRevisionNo: revNo,
		}
		res := h.applyOneSlide(r.Context(), principal.UserID, key, req.PresetID, req.Overwrite, prefill)
		resp.Results = append(resp.Results, res)
		if res.Status == "applied" {
			resp.Applied++
		} else {
			resp.Skipped++
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// applyOneSlide 处理单页，任何失败都只记成 skipped，不中断整份 deck。
//
// 这是「一键」最重要的语义：某一页没配音、解析不出来或校验不过，不该让另外 22 页
// 白跑一次；也不能整单 500 让用户不知道到底成了几页。逐页原因码让前端能列出
// 「跳过 3 页：2 页未配音、1 页已锁定」这种可行动的复核清单。
func (h *AnimationHandler) applyOneSlide(ctx context.Context, userID string, key animation.Key, presetID string, overwrite, prefill bool) applyAllSlideResult {
	out := applyAllSlideResult{SlideID: key.SlideID, Status: "skipped"}
	prev, err := h.store.Get(ctx, key)
	if err != nil && !errors.Is(err, animation.ErrNotFound) {
		out.Reason = "store_failed"
		return out
	}
	if errors.Is(err, animation.ErrNotFound) {
		prev = nil
	}
	if prev != nil {
		// locked 是审批终态，任何批量动作都不能越过它——哪怕 overwrite=true。
		if prev.Status == animation.StatusLocked {
			out.Reason = "locked"
			return out
		}
		if !overwrite {
			if prev.Status == animation.StatusApproved {
				out.Reason = "approved"
				return out
			}
			if hasManualAnimationEdits(prev) {
				out.Reason = "manual_edits"
				return out
			}
		}
	}

	page, pc, err := h.pages.Resolve(ctx, key.TenantID, userID, key.ProjectID, key.SourceRevisionNo, key.SlideID, key.Language)
	if err != nil || page == nil {
		out.Reason = "page_unavailable"
		return out
	}
	// 没有配音就没有页时长，默认规格无法压时间轴。逐页端点是 422，
	// 这里降级成「跳过这一页」，因为整份里有个别页没配音是常态，不是整单失败。
	if pc.DurationMS <= 0 {
		out.Reason = "duration_unknown"
		return out
	}

	var bindings []bind.Binding
	if prefill && len(pc.Segments) > 0 {
		segs := make([]bind.Segment, 0, len(pc.Segments))
		for _, seg := range pc.Segments {
			segs = append(segs, bind.Segment{ID: seg.ID, Text: seg.Text})
		}
		bindings = bind.Run(app.PageBindElements(page, 0, 0), segs, bind.Options{}).Bindings
	}
	dto, _, err := h.defaultSpecFor(page, pc, key, presetID, bindings)
	if err != nil {
		out.Reason = "spec_failed"
		return out
	}

	rev := &animation.Revision{
		TenantID: key.TenantID, ProjectID: key.ProjectID, SlideID: key.SlideID,
		Language: key.Language, SourceRevisionNo: key.SourceRevisionNo,
		Status: animation.StatusDraft, PresetID: presetID,
		Intensity: dto.Intensity, Tracks: dto.Tracks,
	}
	if err := rev.Validate(pc); err != nil {
		out.Reason = "validation_failed"
		return out
	}
	if prev != nil {
		animation.ReconcileTriggerSources(prev, rev)
	}
	expected := int64(0)
	if prev != nil {
		expected = prev.Revision
	}
	saved, err := h.store.Put(ctx, rev, expected)
	if err != nil {
		switch {
		case errors.Is(err, animation.ErrLocked):
			out.Reason = "locked"
		default:
			var conflict *animation.ErrConflict
			if errors.As(err, &conflict) {
				out.Reason = "conflict"
			} else {
				out.Reason = "store_failed"
			}
		}
		return out
	}
	out.Status = "applied"
	out.Revision = saved.Revision
	return out
}

// hasManualAnimationEdits 判断一份规格是否被人手接过。
//
// 判据与 animationPrefill.ts 的复核层一致：TriggerSource=manual 是「人改过 trigger」
// 的唯一标记（ReconcileTriggerSources 写入）。强度、预设这类整体参数不在此列——
// 一键套用的语义本就是「把这些整体参数重设为所选风格」。
func hasManualAnimationEdits(rev *animation.Revision) bool {
	if rev == nil {
		return false
	}
	for _, track := range rev.Tracks {
		if track == nil {
			continue
		}
		for _, clip := range track.Clips {
			if clip != nil && clip.TriggerSource == animation.TriggerSourceManual {
				return true
			}
		}
	}
	return false
}

func deckParamsFromRequest(w http.ResponseWriter, r *http.Request) (string, int, string, bool) {
	projectID := strings.TrimSpace(r.PathValue("projectId"))
	if projectID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_path"})
		return "", 0, "", false
	}
	revNo := 0
	if raw := r.URL.Query().Get("sourceRevisionNo"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_source_revision"})
			return "", 0, "", false
		}
		revNo = n
	}
	language := r.URL.Query().Get("language")
	if language == "" {
		language = "zh-CN"
	}
	return projectID, revNo, language, true
}
