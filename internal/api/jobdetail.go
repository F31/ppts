package api

import (
	"context"
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
// 因此展示所需的字段**全部可读、当时无需新增列**。
//
// B4-M6b 的补充：展示之外还需要**按阶段/受影响页数排序与筛选**，而「受影响页」在 SQL 内无法从
// text 快照解析，故迁移 0026 新增 jobs.phase 与 jobs.affected_pages 两列（MarkStep 同事务维护
// phase、Create 提取 affected_pages）。两列是**查询用的投影**，展示仍以快照推导为准——
// 二者共用 pipeline.ScopeOf / pipeline 的同一份解析逻辑，不会出现两个答案。
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

// scopeFor 推导任务的「范围」，并按传输层上限截断受影响页数组（B4-M6b）。
//
// 推导本身在 pipeline.ScopeOf：落库的 jobs.affected_pages 与界面上的「范围」必须来自同一份解析
// 逻辑，否则同一任务的范围列与「按受影响页数排序」会互相矛盾。
// 截断只作用于返回的数组，PageCount 始终是完整计数——两者不一致即表示已截断，前端据此提示。
func scopeFor(kind, snapshot string) pipeline.Scope {
	s := pipeline.ScopeOf(pipeline.JobKind(kind), snapshot)
	if len(s.AffectedPages) > maxScopePages {
		s.AffectedPages = s.AffectedPages[:maxScopePages]
	}
	return s
}

// JobInspector 是任务扩展信息所需的读取能力（由 pipeline.PGStore 实现）。
// store 未实现时端点返回明确的 Unimplemented / stepsError，而不是让界面显示
// 「没有步骤」的空态（A26：失败必须可见且可解释）。
//
// 注意（B4-M6b）：这里**不含** StepSummaries——阶段已由 jobs.phase 承担（见 /jobs/page），
// 保留一份「由 job_steps 推导阶段」的并行实现正是要避免的双源问题。
type JobInspector interface {
	ListSteps(ctx context.Context, tenantID, jobID string) ([]pipeline.JobStep, error)
	GetMany(ctx context.Context, tenantID string, ids []string) ([]*pipeline.Job, error)
}

// JobPager 是任务列表的筛选/排序/分页能力（B4-M6b），由 pipeline.PGStore 实现。
// 未实现时 /jobs/page 明确返回 501，而不是退化成「没有筛选这回事」（A26）。
type JobPager interface {
	ListPage(ctx context.Context, tenantID string, f pipeline.JobFilter, cursor string, pageSize int) ([]pipeline.JobPageRow, string, error)
	PhaseCounts(ctx context.Context, tenantID, projectID string) (map[string]int, error)
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
		"scope":   scopeFor(string(job.Kind), job.InputSnapshot),
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

// publicJobsSummary 批量返回任务的「范围」，供任务列表的「范围」列与详情面板回退使用（B4-M6a）。
//
// 契约收窄（B4-M6b）：原先还返回 phase / stepTotal / stepCounts，但前端从未消费这三项
// （阶段改由 /jobs/page 提供；步骤计数只在任务详情里展示）。留着不消费的字段，等于把一个
// 「看起来有、实际不用」的能力摆给调用方，故一并去掉。
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
		out[job.ID] = map[string]any{"scope": scopeFor(string(job.Kind), job.InputSnapshot)}
	}
	// 请求但未返回的 ID（并发删除等）不补占位：前端按缺失显示「—」，不伪造范围。
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
