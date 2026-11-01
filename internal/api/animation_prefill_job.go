package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
)

// 本文件是「讲稿预填」的异步入口（M1b）。
//
// 分两条路，而不是一条：
//   - structured / layout / both 三档：规则毫秒级出结果，POST 里直接算完返回（200）；
//   - script 档：规则实测 0.667、模型 0.833，值得问一次模型，而模型要十几秒，
//     所以受理时先返回**规则结果 + jobId**（202），编辑器立刻有可编辑的底稿，
//     模型结果好了再用 GET 换上去。
//
// 这个形态还顺手解决了两件事：模型失败/超时/额度不足时编辑器不会空着（它手里已有规则结果），
// 以及「预填偷偷烧额度」——任务侧每次都预占并结算 token（见 app.AnimationPrefillHandler）。

// prefillJobs 是异步预填需要的最小任务能力面：建任务 + 按 id 查状态。
//
// 不直接要 JobStore（虽然它满足）：这里只用到两个方法，接口窄一点，
// 测试里就不必为了预填去实现取消、重试、列表那五个用不到的方法。
type prefillJobs interface {
	Create(ctx context.Context, tenantID, projectID, kind, idemKey, snapshot string, runAt time.Time) (*pipeline.Job, error)
	Get(ctx context.Context, id, tenantID string) (*pipeline.Job, error)
}

// prefillResponse 是两条预填入口的统一响应。
//
// Status 的取值都对应**编辑器要做的下一件事**，不是内部枚举：
//
//	succeeded  有结果了（可能就是这次算的），直接按 spec 渲染
//	pending    先用这次给的规则 spec，等 model 结果（轮询同一个 jobId）
//	stale      内容变了，这次结果对不上当前页面，重新 POST
//	failed     模型没成功，手里的规则 spec 就是最终结果
type prefillResponse struct {
	Status string            `json:"status"`
	JobID  string            `json:"jobId,omitempty"`
	Reason string            `json:"reason,omitempty"`
	Spec   *animationSpecDTO `json:"spec,omitempty"`
}

// prefillSnapshot 把「当前这一页长什么样」钉成一份快照。
//
// 元素侧重解析一遍（而不是复用 pc.Elements）：pc.Elements 是 animdefault 的锚点投影
// （给校验用），字段与绑定器要的不是一套；绑定器要文本与容器，锚点里没有文本。
// 页尺寸传 0 与既有预填路径同一个近似（见 bind.Run 之前的注释）。
func prefillSnapshot(page *project.Page, pc animation.PageContext, key animation.Key, presetID string) bind.Snapshot {
	snap := bind.Snapshot{
		SlideID:          key.SlideID,
		Language:         key.Language,
		SourceRevisionNo: key.SourceRevisionNo,
		PresetID:         strings.TrimSpace(presetID),
		Elements:         app.PageBindElements(page, 0, 0),
		Segments:         make([]bind.Segment, 0, len(pc.Segments)),
	}
	for _, seg := range pc.Segments {
		snap.Segments = append(snap.Segments, bind.Segment{ID: seg.ID, Text: seg.Text})
	}
	return snap
}

// prefillDTO 把绑定器的分档与通道记录翻译成前端契约。
// report 为 nil 时只说「做了预填」，不编造绑定条数。
func prefillDTO(res bind.RoutedResult, report *animdefault.Report) *animationPrefillDTO {
	out := &animationPrefillDTO{
		Applied:       true,
		Band:          string(res.Band),
		BandReason:    res.BandReason,
		Signals:       res.Signals,
		Route:         string(res.Route),
		ModelFallback: res.Model.Fallback,
	}
	if report != nil {
		out.Bound = report.Bound
		out.Static = report.Static
	}
	return out
}

// defaultSpecFor 用一组绑定生成默认规格。同步路径与异步路径共用，避免两处各算一遍
// 「预填之后这一页该长什么样」——那正是分档口径最容易走偏的地方。
func (h *AnimationHandler) defaultSpecFor(page *project.Page, pc animation.PageContext, key animation.Key, presetID string, bindings []bind.Binding) (animationSpecDTO, animdefault.Report, error) {
	rev, report, err := animdefault.DefaultSpecReport(page, animdefault.DefaultOptions{
		SlideID: key.SlideID, Language: key.Language, DurationMS: pc.DurationMS,
		SegmentIDs: pc.SegmentIDs, PresetID: presetID,
		Bindings: bindings,
	})
	if err != nil {
		return animationSpecDTO{}, report, err
	}
	rev.TenantID = key.TenantID
	rev.ProjectID = key.ProjectID
	rev.SourceRevisionNo = key.SourceRevisionNo
	return animationDTO(rev, false), report, nil
}

// prefillPost 受理一次预填。路由需要能力开启（与 PUT 同一道闸）。
func (h *AnimationHandler) prefillPost(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthenticated"})
		return
	}
	// 预填会改产物形态（把「按顺序淡入」换成「讲到哪条出现哪条」），且可能产生一次模型调用。
	// 与写规格同一道能力闸：开关关闭时返回 404，不暴露半成品能力。
	if !h.gate.Resolve(r.Context(), principal.TenantID).Enabled {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "not_found"})
		return
	}
	key, ok := animationKeyFromRequest(w, r, principal.TenantID)
	if !ok {
		return
	}
	if h.pages == nil || h.jobs == nil || h.objects == nil {
		// 三者缺一都跑不成：没有页解析就不知道绑什么，没有任务就排不了模型，
		// 没有对象存储就写不回结果。宁可明确说不可用，也不要退化成「每次都同步跑模型」。
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
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code":    "duration_unknown",
			"message": "该页还没有配音，无法生成默认动画；请先生成配音，或手动创建动画",
		})
		return
	}

	presetID := r.URL.Query().Get("preset")
	snap := prefillSnapshot(page, pc, key, presetID)
	hash := snap.ContentHash()

	// ① 同内容已算过 → 直接给结果。这就是「内容指纹即缓存键」的收益：
	// 反复打开同一个编辑器、同一个页面重试，都不会再问一次模型。
	// 读失败按「没有」处理：它只意味着这次可能多花一次模型调用，
	// 而不是「结果不对」；真写不回去会在任务侧暴露。
	if cached, hit, _ := app.LoadPrefillResult(r.Context(), h.objects, key.TenantID, key.ProjectID, key.SourceRevisionNo, hash); hit {
		dto, report, derr := h.defaultSpecFor(page, pc, key, presetID, cached.Bindings)
		if derr != nil {
			writeAnimationError(w, derr)
			return
		}
		dto.Prefill = prefillDTOFromResult(cached, &report)
		writeJSON(w, http.StatusOK, prefillResponse{Status: "succeeded", JobID: cached.JobID, Spec: &dto})
		return
	}

	// ② 规则先算一份。它同时充当三个角色：分档判定、script 档的即时底稿、
	// 以及模型失败时的最终结果——三者用的是同一次计算，不会互相矛盾。
	base := bind.RunRouted(r.Context(), key.SlideID, snap.Elements, snap.Segments, bind.RoutedOptions{})
	dto, report, derr := h.defaultSpecFor(page, pc, key, presetID, base.Bindings)
	if derr != nil {
		writeAnimationError(w, derr)
		return
	}

	if len(snap.Segments) == 0 {
		// 没有分段就没有 withSegment 可锚：说清「没做」，而不是给一份看起来做了的结果。
		dto.Prefill = &animationPrefillDTO{Applied: false, Reason: "no_segments"}
		writeJSON(w, http.StatusOK, prefillResponse{Status: "succeeded", Spec: &dto})
		return
	}

	// 基线是用「不带模型」的规则跑出来的，所以它的 Fallback 必然是 no_model。
	// 那不是这一页的结论，只是这次调用的参数——写进响应会让每一页都显示
	// 「还没有配置模型」，包括那些本来就不该问模型的结构化页。清掉。
	base.Model.Fallback = ""
	dto.Prefill = prefillDTO(base, &report)
	if bind.RouteOf(base.Band, base.BandReason) != bind.RouteModel {
		// 规则档不问模型：structured 上规则 0.972 vs 模型 0.772，layout/both 上
		// 两边都救不回来（0.533 vs 0.353）。问一次只是多等十几秒。
		writeJSON(w, http.StatusOK, prefillResponse{Status: "succeeded", Spec: &dto})
		return
	}

	snapBytes, merr := json.Marshal(snap)
	if merr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "internal"})
		return
	}
	// 幂等键带 slideID + 内容指纹：同一页同一版内容重复点开只会有一个任务。
	// 只带指纹不够——两页内容恰好相同（模板页）时会撞到同一把键。
	idem := "prefill:" + key.SlideID + ":" + hash
	job, jerr := h.jobs.Create(r.Context(), principal.TenantID, key.ProjectID,
		string(pipeline.KindAnimationPrefill), idem, string(snapBytes), time.Time{})
	if jerr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "internal"})
		return
	}
	if job.InputSnapshot != string(snapBytes) {
		// 同一把幂等键却对应不同输入：只可能是哈希碰撞或键被复用，不能假装成功。
		writeJSON(w, http.StatusConflict, map[string]any{"code": "idempotency_conflict"})
		return
	}
	// 幂等键命中的是一个**已终结**的任务：要么它当初失败了，要么它成功了但结果对象已经不在
	// （缓存读取在前面刚刚落空才会走到这里）。两种情况下沿用这把键都是死路——
	// 用户每次重开编辑器都拿到同一个 jobId，永远等不到新结果。换一把键重开。
	if job.State.Terminal() {
		retryKey := idem + ":retry:" + strconv.FormatInt(time.Now().UnixNano(), 10)
		if fresh, rerr := h.jobs.Create(r.Context(), principal.TenantID, key.ProjectID,
			string(pipeline.KindAnimationPrefill), retryKey, string(snapBytes), time.Time{}); rerr == nil && fresh.InputSnapshot == string(snapBytes) {
			job = fresh
		}
	}
	if job.State.Terminal() {
		// 重开也没能排上（例如存储仍不可用）：不要报 pending——那会永远等不到结果。
		writeJSON(w, http.StatusOK, prefillResponse{Status: "failed", JobID: job.ID, Reason: jobErrorCode(job)})
		return
	}
	writeJSON(w, http.StatusAccepted, prefillResponse{Status: "pending", JobID: job.ID, Spec: &dto})
}

// prefillGet 查询一次异步预填的结果。
func (h *AnimationHandler) prefillGet(w http.ResponseWriter, r *http.Request) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthenticated"})
		return
	}
	key, ok := animationKeyFromRequest(w, r, principal.TenantID)
	if !ok {
		return
	}
	jobID := strings.TrimSpace(r.URL.Query().Get("jobId"))
	if jobID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_job_id"})
		return
	}
	if h.pages == nil || h.jobs == nil || h.objects == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_unavailable"})
		return
	}
	job, jerr := h.jobs.Get(r.Context(), jobID, principal.TenantID)
	if jerr != nil {
		if errors.Is(jerr, pipeline.ErrJobNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{"code": "not_found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "internal"})
		return
	}
	// 任务必须属于这条路径上的项目与种类：拿到别人的 jobId 也读不出东西。
	if job == nil || job.Kind != pipeline.KindAnimationPrefill || job.ProjectID != key.ProjectID {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "not_found"})
		return
	}
	if !job.State.Terminal() {
		writeJSON(w, http.StatusAccepted, prefillResponse{Status: "pending", JobID: job.ID})
		return
	}
	if job.State != pipeline.StateSucceeded {
		// 失败与取消对编辑器的含义相同：手里那份规则结果就是最终结果。
		// 但两者对运维的含义不同，所以原样透出 accepted 状态而不是一律说 failed。
		status := "failed"
		if job.State == pipeline.StateCanceled {
			status = "canceled"
		}
		writeJSON(w, http.StatusOK, prefillResponse{Status: status, JobID: job.ID, Reason: jobErrorCode(job)})
		return
	}

	var snap bind.Snapshot
	if uerr := json.Unmarshal([]byte(job.InputSnapshot), &snap); uerr != nil {
		writeJSON(w, http.StatusOK, prefillResponse{Status: "failed", JobID: job.ID, Reason: "bad_snapshot"})
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
	// 任务跑完之前页面可能被改过（改了讲稿、重新解析）。此时结果算的是**另一版内容**，
	// 硬套上去就是错绑，所以先比内容指纹，不一致直接让调用方重新 POST。
	if current := prefillSnapshot(page, pc, key, snap.PresetID); current.ContentHash() != snap.ContentHash() {
		writeJSON(w, http.StatusOK, prefillResponse{Status: "stale", JobID: job.ID})
		return
	}
	res, hit, lerr := app.LoadPrefillResult(r.Context(), h.objects, key.TenantID, key.ProjectID, key.SourceRevisionNo, snap.ContentHash())
	if lerr != nil {
		writeJSON(w, http.StatusOK, prefillResponse{Status: "failed", JobID: job.ID, Reason: "result_read_error"})
		return
	}
	if !hit {
		// 任务成功但结果不在：对象过期/被清理，或写入后又被替换。说清原因，
		// 不要拿规则结果冒充模型结果。
		writeJSON(w, http.StatusOK, prefillResponse{Status: "failed", JobID: job.ID, Reason: "result_missing"})
		return
	}
	dto, report, derr := h.defaultSpecFor(page, pc, key, snap.PresetID, res.Bindings)
	if derr != nil {
		writeAnimationError(w, derr)
		return
	}
	dto.Prefill = prefillDTOFromResult(res, &report)
	writeJSON(w, http.StatusOK, prefillResponse{Status: "succeeded", JobID: job.ID, Spec: &dto})
}

// prefillDTOFromResult 把落盘的结果翻译成前端契约。
func prefillDTOFromResult(res *app.PrefillResult, report *animdefault.Report) *animationPrefillDTO {
	out := &animationPrefillDTO{
		Applied:       true,
		Band:          res.Band,
		BandReason:    res.BandReason,
		Signals:       res.Signals,
		Route:         res.Route,
		ModelFallback: res.Model.Fallback,
	}
	if report != nil {
		out.Bound = report.Bound
		out.Static = report.Static
	}
	return out
}

// jobErrorCode 取任务的稳定错误码；没有结构化错误时给空串（前端据此显示通用文案）。
func jobErrorCode(job *pipeline.Job) string {
	if job == nil || job.LastError == nil {
		return ""
	}
	return job.LastError.Code
}
