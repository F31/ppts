package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/pipeline"
)

// B4-M6b：任务列表的「阶段筛选 + 排序 + 翻页」走原生 HTTP。
//
// 为什么不扩 JobService.List：本环境 buf/protoc 不可用（见实施计划「架构约定」），
// 既不能给它加 sort/phase 参数，也不能喂新的 proto 消息。
//
// 与 JobService.List 的分工：List 保持既有语义不变（供既有调用方），/jobs/page 专供控制台任务列表，
// 多出阶段筛选与排序；其游标是 (排序键值, id) 的 keyset（见 pipeline.ListPage），
// 而不是 List 的「created_at 时间戳」单键游标。
//
// 权限：与 JobService.Get/List 同级（任何已认证成员 + 租户隔离），**不加角色门禁**——
// 任务中心本身 viewer 可见，此处若要求 editor 会造出「能进列表、一筛选就 403」的假能力（A22）。

const (
	// maxJobPageSize 单页上限，与 store 的 clamp 保持一致；超过返回 400 而不是静默截断
	// （静默截断会让「我选了 100 条」和「实际拿到 20 条」对不上）。
	maxJobPageSize = 100
	// defaultJobPageSize 默认页大小；与 proto List 的缺省保持同一量级。
	defaultJobPageSize = 20
)

// jobSortKeys 排序键白名单。未知值返回 400 而不是悄悄退回默认排序：
// 排序是用户的显式选择，静默忽略等于骗人（A26 同源）。
var jobSortKeys = map[string]bool{
	"created": true, // 提交时间
	"updated": true, // 最近更新
	"phase":   true, // 阶段
	"pages":   true, // 受影响页数
}

// registerJobListRoutes 挂载任务列表端点（B4-M6b）。
func registerJobListRoutes(mux *http.ServeMux, jobs JobStore, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /jobs/page", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicJobsPage(w, r, jobs)
	})))
}

// publicJobsPage 分页返回筛选/排序后的任务，并附带各阶段的任务数（供筛选项展示可选值与数量）。
func publicJobsPage(w http.ResponseWriter, r *http.Request, jobs JobStore) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	q := r.URL.Query()
	f := pipeline.JobFilter{
		ProjectID: strings.TrimSpace(q.Get("projectId")),
		Phase:     strings.TrimSpace(q.Get("phase")),
		Sort:      strings.TrimSpace(q.Get("sort")),
	}
	if f.Sort == "" {
		f.Sort = "created"
	}
	if !jobSortKeys[f.Sort] {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("unsupported sort %q (want one of created|updated|phase|pages)", f.Sort)))
		return
	}
	// dir：缺省 desc（最近优先）。显式写了非法值才报错，避免 "dir=" 这类空参数被当成错误。
	switch dir := strings.TrimSpace(q.Get("dir")); dir {
	case "", "desc":
		f.Desc = true
	case "asc":
		f.Desc = false
	default:
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("unsupported dir %q (want asc|desc)", dir)))
		return
	}
	pageSize := defaultJobPageSize
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		n, cerr := strconv.Atoi(raw)
		if cerr != nil || n <= 0 || n > maxJobPageSize {
			writeConnectError(w, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("limit must be an integer in 1..%d", maxJobPageSize)))
			return
		}
		pageSize = n
	}
	cursor := strings.TrimSpace(q.Get("cursor"))

	pager, ok := jobs.(JobPager)
	if !ok {
		writeConnectError(w, connect.NewError(connect.CodeUnimplemented,
			errors.New("job list filtering/sorting is not supported by the configured job store")))
		return
	}
	rows, next, err := pager.ListPage(r.Context(), principal.TenantID, f, cursor, pageSize)
	if err != nil {
		if errors.Is(err, pipeline.ErrBadJobCursor) {
			// 游标是客户端回传的不透明串，非法时属请求错误而非服务端故障。
			writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid cursor")))
			return
		}
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, map[string]any{
			"jobId":     row.Job.ID,
			"projectId": row.Job.ProjectID,
			// kind 用库内取值（parse/render/script_draft/narration/export），与前端 jobKindKey 一致；
			// state 用 proto 枚举名（JOB_STATE_*），与 JobService.List 的 JSON 形态保持一致，
			// 使前端 JobState 映射与状态样式无需为两个来源各写一套。
			"kind":            string(row.Job.Kind),
			"state":           jobStateProto(row.Job.State).String(),
			"attempt":         row.Job.Attempt,
			"progressPercent": row.Job.Progress,
			"createdAtUnix":   row.Job.CreatedAt.Unix(),
			"updatedAtUnix":   row.Job.UpdatedAt.Unix(),
			// phase 取自 jobs.phase（B4-M6b），不再由 job_steps 现推——阶段只有这一个来源。
			"phase": row.Phase,
			// 与 JobService.List 保持同样带上 inputSnapshot：详情面板的「原始快照」读的就是它。
			"inputSnapshot": row.Job.InputSnapshot,
		})
	}
	body := map[string]any{"jobs": items, "nextCursor": next}
	// 阶段计数失败不拖垮列表：列表本身可用，只是筛选项少了数量。
	if counts, cerr := pager.PhaseCounts(r.Context(), principal.TenantID, f.ProjectID); cerr == nil {
		body["phaseCounts"] = counts
	} else {
		body["phaseCountsError"] = "load_failed"
	}
	writeJSON(w, http.StatusOK, body)
}

// 编译期确认 PGStore 满足 JobPager：接口漂移应在编译期暴露，而不是等到线上返回 501。
var _ JobPager = (*pipeline.PGStore)(nil)
