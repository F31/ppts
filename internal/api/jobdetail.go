package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/pipeline"
)

// B4-M6a：任务详情与列表的扩展信息（范围 / 受影响页 / 输入版本 / 执行步骤 / traceId）。
//
// 为什么走原生 HTTP 而不扩 proto：本环境 buf/protoc 不可用（见实施计划「架构约定」）。
//
// 现状核实（2026-09-16，逐文件核对，**更正实施计划旧结论「范围/阶段/受影响页服务端完全不存在」**）：
//   - traceId：`jobs.traceparent`（migrations/0018_job_traceparent.sql）已存在，
//     且已由 store 读出为 `pipeline.Job.TraceParent`（postgres.go 的 jobSelectColumns），只是未进 proto；
//   - 范围 / 受影响页 / 输入版本：`jobs.input_snapshot` 内的快照已是权威来源——
//     `app.ParseSnapshot.RevisionNo`、`app.ScriptDraftSnapshot.{SlideIDs,RevisionNo}`、
//     `app.NarrationSnapshot.{Slides,SegmentIDs}`、`app.ExportSnapshot.{Format,PagePNGKeys}`；
//   - 执行步骤：`job_steps` 表（migrations/0001_init.sql:74）已存在，此前只有写入路径（MarkStep）
//     与内部按 step_key 取 result_ref。
//
// 因此以上信息**全部可读、无需新增列或迁移 0026**（M6b 只保留真正没有数据的部分）。
//
// 权限：与 `JobService.Get/List` 同级（任何已认证成员 + 租户隔离），**不加角色门禁**。
// 任务中心本身属 project.read（viewer 可见），若此处要求 editor，会对 viewer 造出
// 「能进列表、点详情必 403」的假能力（A22）。

const (
	// maxJobSummaryIDs 限制 /jobs/summary 的 ids 数量，避免 IN 列表过长。
	maxJobSummaryIDs = 100
	// maxJobSteps 限制任务详情返回的步骤条数（逐页/逐段的配音任务步骤可能很多）；
	// 超限时保留**最近的** maxJobSteps 条，stepCounts 仍为全量计数。
	maxJobSteps = 500
	// maxScopePages 限制 affectedPages 数组长度；pageCount 仍是完整计数（两者不一致即表示已截断）。
	maxScopePages = 200
)

// jobScope 是从任务输入快照推导出的「范围」摘要。
// 采用推导而非新增列：快照已是权威来源，新增列会与之双写并引入一致性风险。
type jobScope struct {
	// Kind：project=全篇 | pages=指定页 | segments=指定分段 | export=导出 | unknown=未识别
	Kind          string   `json:"kind"`
	PageCount     int      `json:"pageCount"`
	AffectedPages []string `json:"affectedPages"`
	// InputRevision 是快照记录的输入版本（讲稿 RevisionNo / 每页 scriptRevision），0 表示快照未记录。
	InputRevision int64  `json:"inputRevision"`
	Format        string `json:"format,omitempty"` // kind=export 时的产物格式
}

// JobInspector 是任务扩展信息所需的读取能力（由 pipeline.PGStore 实现）。
// store 未实现时端点返回明确的 Unimplemented / stepsError，而不是让界面显示
// 「没有步骤」的空态（A26：失败必须可见且可解释）。
type JobInspector interface {
	ListSteps(ctx context.Context, tenantID, jobID string) ([]pipeline.JobStep, error)
	StepSummaries(ctx context.Context, tenantID string, jobIDs []string) (map[string]pipeline.JobStepSummary, error)
	GetMany(ctx context.Context, tenantID string, ids []string) ([]*pipeline.Job, error)
}

// registerJobDetailRoutes 挂载任务详情/摘要原生 HTTP 端点（B4-M6a）。
func registerJobDetailRoutes(mux *http.ServeMux, jobs JobStore, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /jobs/{jid}/detail", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicJobDetail(w, r, jobs)
	})))
	// 列表批量取「范围/阶段/步骤计数」：单次请求替代逐任务查询，避免 N+1。
	mux.Handle("GET /jobs/summary", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicJobsSummary(w, r, jobs)
	})))
}

// publicJobDetail 返回单个任务的扩展详情。
// 步骤读取失败时**仍返回已拿到的 traceId/范围**，并以 stepsError 明确标注步骤不可用——
// 部分可用好过整体失败，但绝不能让失败伪装成「没有步骤」。
func publicJobDetail(w http.ResponseWriter, r *http.Request, jobs JobStore) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	jobID := strings.TrimSpace(r.PathValue("jid"))
	if jobID == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("job id is required")))
		return
	}
	job, err := jobs.Get(r.Context(), jobID, principal.TenantID)
	if err != nil {
		writeConnectError(w, jobError(err))
		return
	}
	body := map[string]any{
		"jobId":   job.ID,
		"kind":    string(job.Kind),
		"traceId": job.TraceParent,
		"scope":   jobScopeFromSnapshot(string(job.Kind), job.InputSnapshot),
	}
	inspector, ok := jobs.(JobInspector)
	if !ok {
		// store 未提供步骤读取能力：明确标注不可用，不伪装成「没有步骤」（A26）。
		fillUnavailableSteps(body, "unsupported")
		writeJSON(w, http.StatusOK, body)
		return
	}
	steps, err := inspector.ListSteps(r.Context(), principal.TenantID, job.ID)
	if err != nil {
		fillUnavailableSteps(body, "load_failed")
		writeJSON(w, http.StatusOK, body)
		return
	}
	items := make([]map[string]any, 0, len(steps))
	for _, st := range steps {
		items = append(items, map[string]any{
			"stepType":      st.StepType,
			"state":         string(st.State),
			"updatedAtUnix": st.UpdatedAt.Unix(),
			// result_ref 是内部对象键，只回「是否存在结果」，不把内部存储键透给前端。
			"hasResult": st.ResultRef != "",
		})
	}
	counts := map[string]int{}
	for _, st := range steps {
		counts[string(st.State)]++
	}
	truncated := false
	if len(steps) > maxJobSteps {
		// 保留最近的步骤：时间序里最新的才反映任务当前进展（计数仍为全量）。
		items = items[len(items)-maxJobSteps:]
		truncated = true
	}
	body["steps"] = items
	body["stepCounts"] = counts
	body["stepTotal"] = len(steps)
	body["stepsTruncated"] = truncated
	writeJSON(w, http.StatusOK, body)
}

// publicJobsSummary 批量返回任务的「范围 + 阶段 + 步骤计数」，供任务列表的两列展示。
func publicJobsSummary(w http.ResponseWriter, r *http.Request, jobs JobStore) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	ids := parseJobIDList(r.URL.Query().Get("ids"))
	if len(ids) == 0 {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("ids is required")))
		return
	}
	if len(ids) > maxJobSummaryIDs {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("too many ids: %d (max %d)", len(ids), maxJobSummaryIDs)))
		return
	}
	inspector, ok := jobs.(JobInspector)
	if !ok {
		writeConnectError(w, connect.NewError(connect.CodeUnimplemented,
			errors.New("job summary is not supported by the configured job store")))
		return
	}
	list, err := inspector.GetMany(r.Context(), principal.TenantID, ids)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	out := make(map[string]any, len(list))
	for _, job := range list {
		out[job.ID] = map[string]any{
			"scope":      jobScopeFromSnapshot(string(job.Kind), job.InputSnapshot),
			"phase":      "",
			"stepTotal":  0,
			"stepCounts": map[string]int{},
		}
	}
	// 请求但未返回的 ID（并发删除等）不补占位：前端按缺失显示「—」，不伪造范围。
	steps, err := inspector.StepSummaries(r.Context(), principal.TenantID, ids)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"jobs": out, "stepsError": "load_failed"})
		return
	}
	for id, sum := range steps {
		entry, ok := out[id].(map[string]any)
		if !ok {
			continue
		}
		// Phase 由「最近更新的步骤类型」推导（job_steps 无阶段列），无步骤时为空串。
		entry["phase"] = sum.Phase
		entry["stepTotal"] = sum.Total
		counts := make(map[string]int, len(sum.Counts))
		for state, n := range sum.Counts {
			counts[string(state)] = n
		}
		entry["stepCounts"] = counts
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

// fillUnavailableSteps 在无法读取步骤时填入明确的不可用标记（而非空列表）。
func fillUnavailableSteps(body map[string]any, reason string) {
	body["steps"] = []any{}
	body["stepCounts"] = map[string]int{}
	body["stepTotal"] = 0
	body["stepsTruncated"] = false
	body["stepsError"] = reason
}

// parseJobIDList 解析逗号分隔的任务 ID：去空白、去重、丢弃空项。
func parseJobIDList(raw string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, 8)
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// jobScopeFromSnapshot 按任务种类解析输入快照，推导范围/受影响页/输入版本。
// 解析失败或种类未知时返回 kind=unknown（**不报错**）：范围不可识别不应让整个详情接口失败，
// 前端据此显示「—」而不是伪造范围。
func jobScopeFromSnapshot(kind, snapshot string) jobScope {
	out := jobScope{Kind: "unknown", AffectedPages: []string{}}
	if strings.TrimSpace(snapshot) == "" {
		return out
	}
	tally := newPageTally()
	switch pipeline.JobKind(kind) {
	case pipeline.KindNarration:
		var snap struct {
			Slides []struct {
				SlideID        string `json:"slideId"`
				ScriptRevision int64  `json:"scriptRevision"`
			} `json:"slides"`
			SegmentIDs []string `json:"segmentIds"`
		}
		if err := json.Unmarshal([]byte(snapshot), &snap); err != nil {
			return out
		}
		// 局部重生成（segmentIds 非空）与按页生成是两种范围。
		out.Kind = "pages"
		if len(snap.SegmentIDs) > 0 {
			out.Kind = "segments"
		}
		for _, s := range snap.Slides {
			tally.add(s.SlideID)
			if s.ScriptRevision > out.InputRevision {
				out.InputRevision = s.ScriptRevision
			}
		}
	case pipeline.KindScriptDraft:
		var snap struct {
			SlideIDs   []string `json:"slideIds"`
			RevisionNo int64    `json:"revisionNo"`
		}
		if err := json.Unmarshal([]byte(snapshot), &snap); err != nil {
			return out
		}
		out.InputRevision = snap.RevisionNo
		if len(snap.SlideIDs) == 0 {
			// 空 = 全部页面（app.ScriptDraftSnapshot.SlideIDs 注释）。
			out.Kind = "project"
			return out
		}
		out.Kind = "pages"
		for _, id := range snap.SlideIDs {
			tally.add(id)
		}
	case pipeline.KindParse:
		var snap struct {
			RevisionNo int64 `json:"revisionNo"`
		}
		if err := json.Unmarshal([]byte(snapshot), &snap); err != nil {
			return out
		}
		// 解析面向整份源文件（全篇）。
		out.Kind = "project"
		out.InputRevision = snap.RevisionNo
	case pipeline.KindExport:
		var snap struct {
			Format      string   `json:"format"`
			PagePNGKeys []string `json:"pagePngKeys"`
		}
		if err := json.Unmarshal([]byte(snapshot), &snap); err != nil {
			return out
		}
		out.Kind = "export"
		out.Format = snap.Format
		// pagePngKeys 是对象键（内部存储键），只用于计数，不作为页面 ID 透出。
		out.PageCount = len(snap.PagePNGKeys)
		return out
	default:
		return out
	}
	out.PageCount = tally.count
	out.AffectedPages = tally.pages
	return out
}

// pageTally 统计去重后的页面数，并按 maxScopePages 上限保留页面 ID；
// count 始终为完整计数，故 pageCount > len(affectedPages) 即表示数组已截断。
type pageTally struct {
	seen  map[string]bool
	pages []string
	count int
}

func newPageTally() *pageTally {
	return &pageTally{seen: make(map[string]bool), pages: []string{}}
}

func (t *pageTally) add(id string) {
	id = strings.TrimSpace(id)
	if id == "" || t.seen[id] {
		return
	}
	t.seen[id] = true
	t.count++
	if len(t.pages) < maxScopePages {
		t.pages = append(t.pages, id)
	}
}
