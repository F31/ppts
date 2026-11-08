package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animstore"
	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/project"
)

// 批量套用（设计 §7.6「页列表多选 → 套用此页动效」）。
//
// 「套用」套的是**风格**（presetId + intensity），不是把源页的轨道字节复制到别的页。
// 这不是简化，是硬约束：Track.ElementID 是页内形状 ID，V2 明确要求它必须存在于
// 当前页；Clip.trigger 里的 withSegment 也绑的是本页的分段 ID。把第 3 页的轨道
// 原样写到第 12 页，只可能得到 11 份 validation_failed，或者一份指向不存在元素的
// 假规格。真实 22 页数据上第一次跑就全军覆没，单测却全绿——因为夹具给每页用了
// 同一批 shape ID。这个盲区由 TestApplyBatchRegeneratesPerPageAnchors 钉住。
//
// 因此每页走与「一键套用 / 单页打开时选同一预设」完全相同的生成链：
// bind.Run 预填 → animdefault.DefaultSpecReport → Revision.Validate。
// 源页只贡献选了哪个预设和强度；目标页用自己的元素、自己的分段重算轨道。
//
// 与 apply-all 的区别不是"少选几页"：apply-all 是整份 deck、逐页尽力而为，
// 单页失败降级成 skipped 继续跑；这里对用户勾选的一批页，语义必须是
// "全成或全不成"——否则用户看到的是「5 页里有 2 页还是旧动效」，
// 而逐页编辑器并不会提示哪两页没跟上。
//
// 两段式：
//   1. 预检所有目标页（页存在、未被锁定、页时长已知、重新生成的规格能过 V1–V12）。
//      任何一页不合格就整单 422 并列出逐页原因，**一页都不写**。
//   2. 全部合格才交给 Store.ApplyBatch 在单事务里写入；事务里若发生并发冲突，
//      数据库整体回滚，不需要也不允许补偿式"把写过的页改回去"。
//
// 响应带 previous 快照，前端据此提供一次撤销；快照由服务端在写入前读取，
// 不信任前端自己保存的"之前是什么"。

const maxApplyBatchBodyBytes = 1 << 20

const (
	maxApplyBatchSlides = 200
	batchReasonLocked   = "locked"
	batchReasonApproved = "approved"
	batchReasonManual   = "manual_edits"
	batchReasonNoPage   = "page_unavailable"
	batchReasonNoDur    = "duration_unknown"
	batchReasonValidate = "validation_failed"
	batchReasonSpec     = "spec_failed"
	batchReasonRejected = "batch_rejected"
	batchReasonStore    = "store_failed"
)

type applyBatchRequest struct {
	SourceSlideID string   `json:"sourceSlideId"`
	SlideIDs      []string `json:"slideIds"`
	Overwrite     bool     `json:"overwrite,omitempty"`
}

// batchSlideSnapshot 是一页套用前的规格，用于一次性撤销。
// Existed=false 表示这页原本没有规格，撤销时必须删行而不是写空草稿。
type batchSlideSnapshot struct {
	SlideID   string             `json:"slideId"`
	Existed   bool               `json:"existed"`
	Revision  int64              `json:"revision"`
	PresetID  string             `json:"presetId,omitempty"`
	Intensity float64            `json:"intensity,omitempty"`
	Tracks    []*animation.Track `json:"tracks,omitempty"`
}

type applyBatchResponse struct {
	SourceSlideID string                `json:"sourceSlideId"`
	Total         int                   `json:"total"`
	Applied       int                   `json:"applied"`
	Results       []applyAllSlideResult `json:"results"`
	Previous      []batchSlideSnapshot  `json:"previous"`
}

type restoreBatchRequest struct {
	Snapshots []batchSlideSnapshot `json:"snapshots"`
}

func (h *AnimationHandler) applyBatch(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthenticated"})
		return
	}
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

	// 逐页 Resolve 会各自读一份 document.json 与时间轴；两份数据在一次请求里不变，
	// 挂上请求级缓存后整批只读一次（见 requestcache.go）。
	ctx := withRequestCache(r.Context())

	var req applyBatchRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxApplyBatchBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_body", "message": err.Error()})
		return
	}
	req.SourceSlideID = strings.TrimSpace(req.SourceSlideID)
	if req.SourceSlideID == "" || len(req.SlideIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_body", "message": "sourceSlideId 与 slideIds 均不能为空"})
		return
	}

	deckIDs, err := h.deck.SlideIDs(ctx, principal.TenantID, principal.UserID, projectID, revNo)
	if err != nil {
		writeAnimationError(w, err)
		return
	}
	inDeck := make(map[string]bool, len(deckIDs))
	for _, id := range deckIDs {
		inDeck[id] = true
	}
	if !inDeck[req.SourceSlideID] {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"code": "source_missing", "message": "源页不在当前版本里"})
		return
	}
	seen := make(map[string]bool, len(req.SlideIDs))
	targets := make([]string, 0, len(req.SlideIDs))
	for _, raw := range req.SlideIDs {
		id := strings.TrimSpace(raw)
		if id == req.SourceSlideID {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_body", "message": "源页不能同时作为目标页"})
			return
		}
		if !inDeck[id] {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"code": "unknown_slide", "message": "选中的页不在当前版本里: " + id})
			return
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		targets = append(targets, id)
	}
	if len(targets) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_body", "message": "没有可套用的目标页"})
		return
	}
	if len(targets) > maxApplyBatchSlides {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "too_many_slides", "message": fmt.Sprintf("一次最多套用 %d 页", maxApplyBatchSlides)})
		return
	}

	sourceKey := animation.Key{
		TenantID: principal.TenantID, ProjectID: projectID, SlideID: req.SourceSlideID,
		Language: language, SourceRevisionNo: revNo,
	}
	source, err := h.store.Get(r.Context(), sourceKey)
	if errors.Is(err, animation.ErrNotFound) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"code": "source_missing", "message": "源页还没有保存动效规格"})
		return
	}
	if err != nil {
		writeAnimationError(w, err)
		return
	}
	// 自定义规格（presetId 为空）没有可复制的"风格"；静默退回默认预设是骗人，
	// 直接让用户回源页选一个预设。
	if strings.TrimSpace(source.PresetID) == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code":    "source_missing",
			"message": "源页是自定义规格，没有可套用的动效风格；请先在源页选择一个预设",
		})
		return
	}
	// 源页的 Tracks / Shots 一律不带走：ElementID 与 Shots 的坐标都是页内的。

	type candidate struct {
		key      animation.Key
		rev      *animation.Revision
		expected int64
		previous batchSlideSnapshot
	}
	candidates := make([]candidate, 0, len(targets))
	results := make([]applyAllSlideResult, 0, len(targets))
	ineligible := 0
	for _, slideID := range targets {
		key := animation.Key{
			TenantID: principal.TenantID, ProjectID: projectID, SlideID: slideID,
			Language: language, SourceRevisionNo: revNo,
		}
		res := applyAllSlideResult{SlideID: slideID, Status: "skipped"}
		prev, err := h.store.Get(r.Context(), key)
		if err != nil && !errors.Is(err, animation.ErrNotFound) {
			res.Reason = batchReasonStore
			results = append(results, res)
			ineligible++
			continue
		}
		if errors.Is(err, animation.ErrNotFound) {
			prev = nil
		}
		snap := snapshotOf(slideID, prev)
		if reason := batchGuard(prev, req.Overwrite); reason != "" {
			res.Reason = reason
			results = append(results, res)
			ineligible++
			continue
		}
		page, pc, err := h.pages.Resolve(ctx, principal.TenantID, principal.UserID, projectID, revNo, slideID, language)
		if err != nil || page == nil {
			res.Reason = batchReasonNoPage
			results = append(results, res)
			ineligible++
			continue
		}
		if pc.DurationMS <= 0 {
			res.Reason = batchReasonNoDur
			results = append(results, res)
			ineligible++
			continue
		}
		// 按这一页自己的元素与讲稿生成同风格规格，而不是复用源页轨道。
		dto, _, err := h.defaultSpecFor(page, pc, key, source.PresetID, batchBindings(page, pc))
		if err != nil {
			res.Reason = batchReasonSpec
			results = append(results, res)
			ineligible++
			continue
		}
		rev := &animation.Revision{
			TenantID: key.TenantID, ProjectID: key.ProjectID, SlideID: key.SlideID,
			Language: key.Language, SourceRevisionNo: key.SourceRevisionNo,
			Status: animation.StatusDraft, PresetID: source.PresetID,
			Intensity: source.Intensity, Tracks: dto.Tracks,
		}
		if err := rev.Validate(pc); err != nil {
			res.Reason = batchReasonValidate
			results = append(results, res)
			ineligible++
			continue
		}
		expected := int64(0)
		if prev != nil {
			expected = prev.Revision
		}
		candidates = append(candidates, candidate{key: key, rev: rev, expected: expected, previous: snap})
		res.Status = "applied"
		results = append(results, res)
	}

	// 预检不合格就整单拒绝：绝不能出现"部分页套上了、部分页没套上"的静默中间态。
	// 同时不能让合格的页在一份「零写入」响应里仍标成 applied——那是字面上的假反馈。
	if ineligible > 0 {
		rejected := append([]applyAllSlideResult(nil), results...)
		for i := range rejected {
			if rejected[i].Status == "applied" {
				rejected[i].Status = "skipped"
				rejected[i].Reason = batchReasonRejected
			}
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code":    "batch_ineligible",
			"message": "有页面不满足套用条件，本次没有写入任何一页；请按原因调整选择后重试",
			"total":   len(targets),
			"results": rejected,
		})
		return
	}

	ops := make([]animstore.BatchOp, 0, len(candidates))
	previous := make([]batchSlideSnapshot, 0, len(candidates))
	for _, c := range candidates {
		ops = append(ops, animstore.BatchOp{Key: c.key, Rev: c.rev, Expected: c.expected})
		previous = append(previous, c.previous)
	}
	saved, err := h.store.ApplyBatch(r.Context(), ops)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"code":    "batch_conflict",
			"message": "写入时发生冲突，整批已回滚，没有页面被改动",
			"detail":  err.Error(),
		})
		return
	}
	for i, s := range saved {
		if s != nil {
			results[i].Revision = s.Revision
		}
	}
	writeJSON(w, http.StatusOK, applyBatchResponse{
		SourceSlideID: req.SourceSlideID, Total: len(targets), Applied: len(candidates),
		Results: results, Previous: previous,
	})
}

// restoreBatch 用 apply-batch 返回的快照一次性撤销。同样是单事务、全成或全不成；
// 快照内容仍要过 V1–V12，避免把客户端传来的任意字节写进规格表。
func (h *AnimationHandler) restoreBatch(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthenticated"})
		return
	}
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
	// 撤销同样逐页 Resolve，同样挂请求级缓存。
	ctx := withRequestCache(r.Context())

	var req restoreBatchRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxApplyBatchBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_body", "message": err.Error()})
		return
	}
	if len(req.Snapshots) == 0 || len(req.Snapshots) > maxApplyBatchSlides {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_body", "message": "快照数量不合法"})
		return
	}
	deckIDs, err := h.deck.SlideIDs(ctx, principal.TenantID, principal.UserID, projectID, revNo)
	if err != nil {
		writeAnimationError(w, err)
		return
	}
	inDeck := make(map[string]bool, len(deckIDs))
	for _, id := range deckIDs {
		inDeck[id] = true
	}
	seen := make(map[string]bool, len(req.Snapshots))
	ops := make([]animstore.BatchOp, 0, len(req.Snapshots))
	results := make([]applyAllSlideResult, 0, len(req.Snapshots))
	for _, snap := range req.Snapshots {
		id := strings.TrimSpace(snap.SlideID)
		res := applyAllSlideResult{SlideID: id, Status: "applied"}
		if !inDeck[id] || seen[id] {
			res.Status, res.Reason = "skipped", "invalid_snapshot"
			results = append(results, res)
			continue
		}
		seen[id] = true
		key := animation.Key{
			TenantID: principal.TenantID, ProjectID: projectID, SlideID: id,
			Language: language, SourceRevisionNo: revNo,
		}
		cur, err := h.store.Get(r.Context(), key)
		if err != nil && !errors.Is(err, animation.ErrNotFound) {
			res.Status, res.Reason = "skipped", batchReasonStore
			results = append(results, res)
			continue
		}
		expected := int64(0)
		if !errors.Is(err, animation.ErrNotFound) && cur != nil {
			expected = cur.Revision
		}
		if !snap.Existed {
			ops = append(ops, animstore.BatchOp{Key: key, Rev: nil, Expected: expected})
			results = append(results, res)
			continue
		}
		page, pc, err := h.pages.Resolve(ctx, principal.TenantID, principal.UserID, projectID, revNo, id, language)
		if err != nil || page == nil || pc.DurationMS <= 0 {
			res.Status, res.Reason = "skipped", batchReasonNoPage
			results = append(results, res)
			continue
		}
		tracks, err := cloneTracks(snap.Tracks)
		if err != nil {
			res.Status, res.Reason = "skipped", batchReasonStore
			results = append(results, res)
			continue
		}
		rev := &animation.Revision{
			TenantID: key.TenantID, ProjectID: key.ProjectID, SlideID: key.SlideID,
			Language: key.Language, SourceRevisionNo: key.SourceRevisionNo,
			Status: animation.StatusDraft, PresetID: snap.PresetID,
			Intensity: snap.Intensity, Tracks: tracks,
		}
		if err := rev.Validate(pc); err != nil {
			res.Status, res.Reason = "skipped", batchReasonValidate
			results = append(results, res)
			continue
		}
		ops = append(ops, animstore.BatchOp{Key: key, Rev: rev, Expected: expected})
		results = append(results, res)
	}
	for _, res := range results {
		if res.Status == "skipped" {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"code": "restore_ineligible", "message": "有快照无法恢复，本次没有改动任何一页", "results": results,
			})
			return
		}
	}
	if _, err := h.store.ApplyBatch(r.Context(), ops); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"code": "batch_conflict", "message": "恢复时发生冲突，整批已回滚", "detail": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": len(ops), "restored": len(ops), "results": results})
}

func batchGuard(prev *animation.Revision, overwrite bool) string {
	if prev == nil {
		return ""
	}
	if prev.Status == animation.StatusLocked {
		return batchReasonLocked // 审批终态，overwrite 也不能越过
	}
	if !overwrite {
		if prev.Status == animation.StatusApproved {
			return batchReasonApproved
		}
		if hasManualAnimationEdits(prev) {
			return batchReasonManual
		}
	}
	return ""
}

func snapshotOf(slideID string, prev *animation.Revision) batchSlideSnapshot {
	if prev == nil {
		return batchSlideSnapshot{SlideID: slideID, Existed: false}
	}
	tracks, err := cloneTracks(prev.Tracks)
	if err != nil {
		tracks = nil
	}
	return batchSlideSnapshot{
		SlideID: slideID, Existed: true, Revision: prev.Revision,
		PresetID: prev.PresetID, Intensity: prev.Intensity, Tracks: tracks,
	}
}

// batchBindings 与 applyOneSlide 的预填口径完全一致：同一页、同一份讲稿，
// 「批量套用」和「单页打开选同一预设」必须生成同一份规格，不能有两套编排。
func batchBindings(page *project.Page, pc animation.PageContext) []bind.Binding {
	if len(pc.Segments) == 0 {
		return nil
	}
	segs := make([]bind.Segment, 0, len(pc.Segments))
	for _, seg := range pc.Segments {
		segs = append(segs, bind.Segment{ID: seg.ID, Text: seg.Text})
	}
	return bind.Run(app.PageBindElements(page, 0, 0), segs, bind.Options{}).Bindings
}

// cloneTracks 深拷贝轨道：快照与恢复的每页必须持有独立的 Clip 指针，
// 否则共享指针会让一处改动串到另一页（最难查的一类串数据）。
func cloneTracks(tracks []*animation.Track) ([]*animation.Track, error) {
	if len(tracks) == 0 {
		return []*animation.Track{}, nil
	}
	raw, err := json.Marshal(tracks)
	if err != nil {
		return nil, fmt.Errorf("animation: marshal tracks: %w", err)
	}
	var out []*animation.Track
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("animation: unmarshal tracks: %w", err)
	}
	return out, nil
}
