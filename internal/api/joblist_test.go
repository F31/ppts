package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/F31/ppts/internal/pipeline"
)

// B4-M6b 测试：/jobs/page 的参数校验、payload 形态与降级行为。

// jobPageStub 在 JobStore 之上实现 JobPager，并记录收到的筛选条件。
type jobPageStub struct {
	jobDetailStub
	rows         []pipeline.JobPageRow
	next         string
	counts       map[string]int
	listErr      error
	countsErr    error
	lastFilter   pipeline.JobFilter
	lastCursor   string
	lastPageSize int
}

func (s *jobPageStub) ListPage(_ context.Context, _ string, f pipeline.JobFilter, cursor string, pageSize int) ([]pipeline.JobPageRow, string, error) {
	s.lastFilter, s.lastCursor, s.lastPageSize = f, cursor, pageSize
	if s.listErr != nil {
		return nil, "", s.listErr
	}
	return s.rows, s.next, nil
}

func (s *jobPageStub) PhaseCounts(context.Context, string, string) (map[string]int, error) {
	if s.countsErr != nil {
		return nil, s.countsErr
	}
	return s.counts, nil
}

func serveJobPageRoute(store JobStore, target string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	registerJobListRoutes(mux, store, identityAuth)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req = req.WithContext(context.WithValue(req.Context(), principalKey{}, Principal{TenantID: "tenant-1", UserID: "user-1"}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type jobPageRowJSON struct {
	JobID           string `json:"jobId"`
	ProjectID       string `json:"projectId"`
	Kind            string `json:"kind"`
	State           string `json:"state"`
	Attempt         int    `json:"attempt"`
	ProgressPercent int    `json:"progressPercent"`
	CreatedAtUnix   int64  `json:"createdAtUnix"`
	UpdatedAtUnix   int64  `json:"updatedAtUnix"`
	Phase           string `json:"phase"`
	InputSnapshot   string `json:"inputSnapshot"`
}

type jobPageResponse struct {
	Jobs        []jobPageRowJSON `json:"jobs"`
	NextCursor  string           `json:"nextCursor"`
	PhaseCounts map[string]int   `json:"phaseCounts"`
	CountsErr   string           `json:"phaseCountsError"`
}

// 排序键/方向/页大小越界都必须 400：静默退回默认值会让用户的选择被无声吞掉。
func TestPublicJobsPageRejectsInvalidParams(t *testing.T) {
	store := &jobPageStub{}
	cases := []struct {
		name   string
		target string
	}{
		{"未知排序键", "/jobs/page?sort=whatever"},
		{"未知方向", "/jobs/page?dir=sideways"},
		{"limit 为零", "/jobs/page?limit=0"},
		{"limit 为负", "/jobs/page?limit=-3"},
		{"limit 超上限", "/jobs/page?limit=101"},
		{"limit 非数字", "/jobs/page?limit=abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveJobPageRoute(store, tc.target)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d want 400 (body=%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// 默认排序 created/desc、默认页大小，且筛选条件原样透传给 store。
func TestPublicJobsPagePassesFilterToStore(t *testing.T) {
	store := &jobPageStub{}
	rec := serveJobPageRoute(store, "/jobs/page?phase=export&projectId=p-1&cursor=abc&limit=7")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if store.lastFilter.Sort != "created" || !store.lastFilter.Desc {
		t.Fatalf("default sort = %q desc=%v want created/true", store.lastFilter.Sort, store.lastFilter.Desc)
	}
	if store.lastFilter.Phase != "export" || store.lastFilter.ProjectID != "p-1" {
		t.Fatalf("filter = %+v", store.lastFilter)
	}
	if store.lastCursor != "abc" || store.lastPageSize != 7 {
		t.Fatalf("cursor/pageSize = %q/%d want abc/7", store.lastCursor, store.lastPageSize)
	}
}

func TestPublicJobsPageAscDirection(t *testing.T) {
	store := &jobPageStub{}
	serveJobPageRoute(store, "/jobs/page?sort=pages&dir=asc")
	if store.lastFilter.Sort != "pages" || store.lastFilter.Desc {
		t.Fatalf("filter = %+v want pages/asc", store.lastFilter)
	}
}

// payload 形态：state 用 proto 枚举名（与 JobService.List 一致），phase 来自 jobs.phase。
func TestPublicJobsPageReturnsRowsCursorAndCounts(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := &jobPageStub{
		rows: []pipeline.JobPageRow{
			{Job: &pipeline.Job{
				ID: "job-1", ProjectID: "proj-1", Kind: pipeline.KindNarration,
				State: pipeline.StateQueued, Attempt: 2, Progress: 40,
				CreatedAt: now, UpdatedAt: now.Add(time.Minute),
				InputSnapshot: `{"slides":[{"slideId":"s1"}],"segmentIds":[]}`,
			}, Phase: "tts_segment", PageCount: 1},
		},
		next:   "cursor-2",
		counts: map[string]int{"tts_segment": 1},
	}
	rec := serveJobPageRoute(store, "/jobs/page")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var got jobPageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Jobs) != 1 {
		t.Fatalf("jobs = %+v", got.Jobs)
	}
	row := got.Jobs[0]
	if row.JobID != "job-1" || row.ProjectID != "proj-1" || row.Kind != "narration" {
		t.Fatalf("identity = %+v", row)
	}
	// state 必须是 proto 枚举名，否则前端 jobStateKey 查不到、文案与状态色都会失效。
	if row.State != "JOB_STATE_QUEUED" {
		t.Fatalf("state = %q want JOB_STATE_QUEUED", row.State)
	}
	if row.Phase != "tts_segment" {
		t.Fatalf("phase = %q want tts_segment", row.Phase)
	}
	if row.Attempt != 2 || row.ProgressPercent != 40 {
		t.Fatalf("attempt/progress = %d/%d", row.Attempt, row.ProgressPercent)
	}
	if row.CreatedAtUnix != now.Unix() || row.UpdatedAtUnix != now.Add(time.Minute).Unix() {
		t.Fatalf("timestamps = %d/%d", row.CreatedAtUnix, row.UpdatedAtUnix)
	}
	if row.InputSnapshot == "" {
		t.Fatalf("inputSnapshot should be carried for the detail panel's raw view")
	}
	if got.NextCursor != "cursor-2" {
		t.Fatalf("nextCursor = %q want cursor-2", got.NextCursor)
	}
	if got.PhaseCounts["tts_segment"] != 1 {
		t.Fatalf("phaseCounts = %+v", got.PhaseCounts)
	}
	if got.CountsErr != "" {
		t.Fatalf("phaseCountsError = %q want empty", got.CountsErr)
	}
}

// 阶段计数失败不能拖垮列表：列表本身仍可用，只标注计数不可用。
func TestPublicJobsPageCountsFailureKeepsList(t *testing.T) {
	store := &jobPageStub{
		rows:      []pipeline.JobPageRow{{Job: &pipeline.Job{ID: "job-1", Kind: pipeline.KindParse, State: pipeline.StateRunning}}},
		countsErr: errors.New("counts unavailable"),
	}
	rec := serveJobPageRoute(store, "/jobs/page")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d want 200", rec.Code)
	}
	var got jobPageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Jobs) != 1 {
		t.Fatalf("jobs lost on counts failure: %+v", got)
	}
	if got.CountsErr != "load_failed" {
		t.Fatalf("phaseCountsError = %q want load_failed", got.CountsErr)
	}
}

// 非法游标属请求错误 → 400（不是 500）。
func TestPublicJobsPageBadCursorMapsTo400(t *testing.T) {
	store := &jobPageStub{listErr: pipeline.ErrBadJobCursor}
	rec := serveJobPageRoute(store, "/jobs/page?cursor=%21%21")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

// 存储故障 → 500（与游标非法区分开）。
func TestPublicJobsPageStoreFailureMapsTo500(t *testing.T) {
	store := &jobPageStub{listErr: errors.New("boom")}
	rec := serveJobPageRoute(store, "/jobs/page")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d want 500 (body=%s)", rec.Code, rec.Body.String())
	}
}

// store 未实现 JobPager → 501，明确告知"这个部署不支持筛选/排序"，而不是悄悄返回未排序结果。
func TestPublicJobsPageUnsupportedStore(t *testing.T) {
	rec := serveJobPageRoute(&jobDetailStub{}, "/jobs/page")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d want 501 (body=%s)", rec.Code, rec.Body.String())
	}
}
