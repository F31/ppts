package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/F31/ppts/internal/pipeline"
)

// errTestSteps 模拟步骤读取失败（真实存储故障）。
var errTestSteps = errors.New("steps unavailable")

// B4-M6a 测试：任务范围推导（纯函数）与 /jobs/{jid}/detail、/jobs/summary 端点行为。

// jobDetailStub 只实现 JobStore（不含 JobInspector），用于验证"store 不支持步骤读取"的降级。
type jobDetailStub struct {
	job    *pipeline.Job
	getErr error
}

func (s *jobDetailStub) Create(context.Context, string, string, string, string, string, time.Time) (*pipeline.Job, error) {
	return s.job, nil
}

func (s *jobDetailStub) LatestSucceededJob(context.Context, string, string, string) (*pipeline.Job, error) {
	return nil, pipeline.ErrNoSucceededJob
}

func (s *jobDetailStub) StepResultRef(context.Context, string, string) (string, error) { return "", nil }

func (s *jobDetailStub) Get(context.Context, string, string) (*pipeline.Job, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.job == nil {
		return nil, pipeline.ErrJobNotFound
	}
	return s.job, nil
}

func (s *jobDetailStub) List(context.Context, string, string, string, string, int) ([]*pipeline.Job, string, error) {
	return nil, "", nil
}

func (s *jobDetailStub) Cancel(context.Context, string, string) (*pipeline.Job, error) {
	return nil, pipeline.ErrJobNotFound
}

func (s *jobDetailStub) RetryFailed(context.Context, string, string) (*pipeline.Job, error) {
	return nil, pipeline.ErrJobNotFound
}

// jobDetailInspectorStub 在 JobStore 之上实现 JobInspector（步骤读取）。
type jobDetailInspectorStub struct {
	jobDetailStub
	steps       []pipeline.JobStep
	summaries   map[string]pipeline.JobStepSummary
	many        []*pipeline.Job
	stepsErr    error
	summaryErr  error
	manyErr     error
	lastManyIDs []string
}

func (s *jobDetailInspectorStub) ListSteps(context.Context, string, string) ([]pipeline.JobStep, error) {
	if s.stepsErr != nil {
		return nil, s.stepsErr
	}
	return s.steps, nil
}

func (s *jobDetailInspectorStub) StepSummaries(_ context.Context, _ string, _ []string) (map[string]pipeline.JobStepSummary, error) {
	if s.summaryErr != nil {
		return nil, s.summaryErr
	}
	return s.summaries, nil
}

func (s *jobDetailInspectorStub) GetMany(_ context.Context, _ string, ids []string) ([]*pipeline.Job, error) {
	s.lastManyIDs = ids
	if s.manyErr != nil {
		return nil, s.manyErr
	}
	return s.many, nil
}

// identityAuth 跳过鉴权包装：测试直接在请求上下文注入 Principal。
func identityAuth(h http.Handler) http.Handler { return h }

func serveJobRoute(store JobStore, target string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	registerJobDetailRoutes(mux, store, identityAuth)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req = req.WithContext(context.WithValue(req.Context(), principalKey{}, Principal{TenantID: "tenant-1", UserID: "user-1"}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type jobDetailResponse struct {
	JobID   string `json:"jobId"`
	Kind    string `json:"kind"`
	TraceID string `json:"traceId"`
	Scope   struct {
		Kind          string   `json:"kind"`
		PageCount     int      `json:"pageCount"`
		AffectedPages []string `json:"affectedPages"`
		InputRevision int64    `json:"inputRevision"`
		Format        string   `json:"format"`
	} `json:"scope"`
	Steps []struct {
		StepType      string `json:"stepType"`
		State         string `json:"state"`
		UpdatedAtUnix int64  `json:"updatedAtUnix"`
		HasResult     bool   `json:"hasResult"`
	} `json:"steps"`
	StepCounts     map[string]int `json:"stepCounts"`
	StepTotal      int            `json:"stepTotal"`
	StepsTruncated bool           `json:"stepsTruncated"`
	StepsError     string         `json:"stepsError"`
}

type jobSummaryResponse struct {
	Jobs map[string]struct {
		Scope struct {
			Kind          string `json:"kind"`
			PageCount     int    `json:"pageCount"`
			InputRevision int64  `json:"inputRevision"`
		} `json:"scope"`
		Phase      string         `json:"phase"`
		StepTotal  int            `json:"stepTotal"`
		StepCounts map[string]int `json:"stepCounts"`
	} `json:"jobs"`
	StepsError string `json:"stepsError"`
}

func decodeDetail(t *testing.T, rec *httptest.ResponseRecorder) jobDetailResponse {
	t.Helper()
	var out jobDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode detail response: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

func TestJobScopeFromSnapshot(t *testing.T) {
	cases := []struct {
		name          string
		kind          string
		snapshot      string
		wantKind      string
		wantPages     int
		wantRevision  int64
		wantPageIDs   []string
		wantFormat    string
		wantTruncated bool
	}{
		{
			name: "narration 按页生成", kind: "narration",
			snapshot:     `{"slides":[{"slideId":"s1","scriptRevision":3},{"slideId":"s2","scriptRevision":4}],"segmentIds":[],"language":"zh"}`,
			wantKind:     "pages",
			wantPages:    2,
			wantRevision: 4,
			wantPageIDs:  []string{"s1", "s2"},
		},
		{
			name: "narration 局部重生成视为 segments", kind: "narration",
			snapshot:     `{"slides":[{"slideId":"s1","scriptRevision":2}],"segmentIds":["seg-1"]}`,
			wantKind:     "segments",
			wantPages:    1,
			wantRevision: 2,
			wantPageIDs:  []string{"s1"},
		},
		{
			name: "narration 重复与空页 ID 去重", kind: "narration",
			snapshot:     `{"slides":[{"slideId":"s1"},{"slideId":" s1 "},{"slideId":""}],"segmentIds":[]}`,
			wantKind:     "pages",
			wantPages:    1,
			wantPageIDs:  []string{"s1"},
		},
		{
			name: "script_draft 空 slideIds 表示全篇", kind: "script_draft",
			snapshot:     `{"projectId":"p1","revisionNo":7,"mode":"original"}`,
			wantKind:     "project",
			wantPages:    0,
			wantRevision: 7,
			wantPageIDs:  []string{},
		},
		{
			name: "script_draft 指定页", kind: "script_draft",
			snapshot:     `{"slideIds":["s9"],"revisionNo":2}`,
			wantKind:     "pages",
			wantPages:    1,
			wantRevision: 2,
			wantPageIDs:  []string{"s9"},
		},
		{
			name: "parse 面向全篇并带输入版本", kind: "parse",
			snapshot:     `{"sourceRevisionId":"rev-1","revisionNo":5,"parserVersion":"v1"}`,
			wantKind:     "project",
			wantPages:    0,
			wantRevision: 5,
			wantPageIDs:  []string{},
		},
		{
			name: "export 只计数不暴露对象键", kind: "export",
			snapshot:     `{"format":"mp4","pagePngKeys":["t/s1.png","t/s2.png","t/s3.png"]}`,
			wantKind:     "export",
			wantPages:    3,
			wantPageIDs:  []string{},
			wantFormat:   "mp4",
		},
		{
			name: "空快照", kind: "narration", snapshot: "",
			wantKind: "unknown", wantPageIDs: []string{},
		},
		{
			name: "非法 JSON", kind: "narration", snapshot: `{"slides":`,
			wantKind: "unknown", wantPageIDs: []string{},
		},
		{
			name: "未知种类", kind: "render", snapshot: `{"slideId":"s1"}`,
			wantKind: "unknown", wantPageIDs: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := jobScopeFromSnapshot(tc.kind, tc.snapshot)
			if got.Kind != tc.wantKind {
				t.Fatalf("kind = %q want %q", got.Kind, tc.wantKind)
			}
			if got.PageCount != tc.wantPages {
				t.Fatalf("pageCount = %d want %d", got.PageCount, tc.wantPages)
			}
			if got.InputRevision != tc.wantRevision {
				t.Fatalf("inputRevision = %d want %d", got.InputRevision, tc.wantRevision)
			}
			if got.Format != tc.wantFormat {
				t.Fatalf("format = %q want %q", got.Format, tc.wantFormat)
			}
			if len(got.AffectedPages) != len(tc.wantPageIDs) {
				t.Fatalf("affectedPages = %v want %v", got.AffectedPages, tc.wantPageIDs)
			}
			for i, id := range tc.wantPageIDs {
				if got.AffectedPages[i] != id {
					t.Fatalf("affectedPages[%d] = %q want %q", i, got.AffectedPages[i], id)
				}
			}
		})
	}
}

// 超出上限时 affectedPages 截断，但 pageCount 仍是完整计数（前端据此判断"已截断"）。
func TestJobScopePageCountKeepsFullCountWhenTruncated(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"slides":[`)
	for i := 0; i <= maxScopePages; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"slideId":"s`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`"}`)
	}
	b.WriteString(`],"segmentIds":[]}`)
	got := jobScopeFromSnapshot("narration", b.String())
	if got.PageCount != maxScopePages+1 {
		t.Fatalf("pageCount = %d want %d", got.PageCount, maxScopePages+1)
	}
	if len(got.AffectedPages) != maxScopePages {
		t.Fatalf("affectedPages len = %d want %d", len(got.AffectedPages), maxScopePages)
	}
}

func TestParseJobIDListTrimsDedupesAndDropsEmpty(t *testing.T) {
	got := parseJobIDList(" a , b,a ,, ")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("parseJobIDList = %v want [a b]", got)
	}
	if len(parseJobIDList("")) != 0 {
		t.Fatalf("empty input should yield no ids")
	}
}

func TestPublicJobDetailReturnsScopeStepsAndTraceID(t *testing.T) {
	job := &pipeline.Job{
		ID: "job-1", Kind: pipeline.KindNarration,
		InputSnapshot: `{"slides":[{"slideId":"s1","scriptRevision":2},{"slideId":"s2","scriptRevision":2}],"segmentIds":[]}`,
		TraceParent:   "00-0123456789abcdef0123456789abcdef-1112131415161718-01",
	}
	store := &jobDetailInspectorStub{
		jobDetailStub: jobDetailStub{job: job},
		steps: []pipeline.JobStep{
			{StepType: "render", StepKey: "k1", State: pipeline.StepSuccess, ResultRef: "t/s1.png", UpdatedAt: time.Unix(100, 0)},
			{StepType: "tts_segment", StepKey: "k2", State: pipeline.StepFailed, UpdatedAt: time.Unix(200, 0)},
		},
	}
	rec := serveJobRoute(store, "/jobs/job-1/detail")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	got := decodeDetail(t, rec)
	if got.TraceID != job.TraceParent {
		t.Fatalf("traceId = %q want %q", got.TraceID, job.TraceParent)
	}
	if got.Scope.Kind != "pages" || got.Scope.PageCount != 2 {
		t.Fatalf("scope = %+v", got.Scope)
	}
	if got.Kind != "narration" || got.JobID != "job-1" {
		t.Fatalf("job identity = %q/%q", got.JobID, got.Kind)
	}
	if len(got.Steps) != 2 || got.StepTotal != 2 {
		t.Fatalf("steps = %+v total=%d", got.Steps, got.StepTotal)
	}
	if got.StepsTruncated {
		t.Fatalf("steps should not be flagged truncated")
	}
	if got.StepsError != "" {
		t.Fatalf("stepsError = %q want empty", got.StepsError)
	}
	if got.StepCounts["success"] != 1 || got.StepCounts["failed"] != 1 {
		t.Fatalf("stepCounts = %v", got.StepCounts)
	}
	// result_ref 是内部对象键，只回 hasResult，不透出键本身。
	if !got.Steps[0].HasResult || got.Steps[1].HasResult {
		t.Fatalf("hasResult = %v/%v", got.Steps[0].HasResult, got.Steps[1].HasResult)
	}
}

func TestPublicJobDetailMarksUnsupportedStore(t *testing.T) {
	job := &pipeline.Job{ID: "job-2", Kind: pipeline.KindParse, InputSnapshot: `{"revisionNo":3}`, TraceParent: "00-x-01"}
	rec := serveJobRoute(&jobDetailStub{job: job}, "/jobs/job-2/detail")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d want 200", rec.Code)
	}
	got := decodeDetail(t, rec)
	// 明确标注不可用；traceId/范围仍可用 —— 不能伪装成"没有步骤"。
	if got.StepsError != "unsupported" {
		t.Fatalf("stepsError = %q want unsupported", got.StepsError)
	}
	if got.TraceID != "00-x-01" || got.Scope.InputRevision != 3 {
		t.Fatalf("部分可用数据丢失: %+v", got)
	}
	if got.StepTotal != 0 || len(got.Steps) != 0 {
		t.Fatalf("steps should be empty when unsupported")
	}
}

func TestPublicJobDetailStepFailureStillReturnsPartialData(t *testing.T) {
	job := &pipeline.Job{ID: "job-3", Kind: pipeline.KindNarration, InputSnapshot: `{"slides":[{"slideId":"s1"}],"segmentIds":[]}`, TraceParent: "00-y-01"}
	store := &jobDetailInspectorStub{
		jobDetailStub: jobDetailStub{job: job},
		stepsErr:      errTestSteps,
	}
	rec := serveJobRoute(store, "/jobs/job-3/detail")
	got := decodeDetail(t, rec)
	if got.StepsError != "load_failed" {
		t.Fatalf("stepsError = %q want load_failed", got.StepsError)
	}
	if got.TraceID != "00-y-01" || got.Scope.PageCount != 1 {
		t.Fatalf("部分可用数据丢失: %+v", got)
	}
}

func TestPublicJobDetailNotFoundMapsTo404(t *testing.T) {
	rec := serveJobRoute(&jobDetailStub{}, "/jobs/missing/detail")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d want 404 (body=%s)", rec.Code, rec.Body.String())
	}
}

// 步骤超出上限时保留最近的 maxJobSteps 条，计数仍为全量。
func TestPublicJobDetailTruncatesToMostRecentSteps(t *testing.T) {
	job := &pipeline.Job{ID: "job-4", Kind: pipeline.KindNarration, InputSnapshot: `{"slides":[],"segmentIds":[]}`}
	steps := make([]pipeline.JobStep, 0, maxJobSteps+1)
	for i := 0; i <= maxJobSteps; i++ {
		steps = append(steps, pipeline.JobStep{
			StepType: "tts_segment", StepKey: "k" + strconv.Itoa(i),
			State: pipeline.StepSuccess, UpdatedAt: time.Unix(int64(i), 0),
		})
	}
	store := &jobDetailInspectorStub{jobDetailStub: jobDetailStub{job: job}, steps: steps}
	got := decodeDetail(t, serveJobRoute(store, "/jobs/job-4/detail"))
	if !got.StepsTruncated {
		t.Fatalf("stepsTruncated = false want true")
	}
	if len(got.Steps) != maxJobSteps {
		t.Fatalf("steps len = %d want %d", len(got.Steps), maxJobSteps)
	}
	if got.StepTotal != maxJobSteps+1 {
		t.Fatalf("stepTotal = %d want %d", got.StepTotal, maxJobSteps+1)
	}
	if got.Steps[0].StepType != "tts_segment" {
		t.Fatalf("first step type = %q", got.Steps[0].StepType)
	}
	if got.StepCounts["success"] != maxJobSteps+1 {
		t.Fatalf("stepCounts = %v", got.StepCounts)
	}
}

func TestPublicJobsSummaryValidation(t *testing.T) {
	store := &jobDetailInspectorStub{}
	if rec := serveJobRoute(store, "/jobs/summary"); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing ids status = %d want 400", rec.Code)
	}
	ids := make([]string, 0, maxJobSummaryIDs+1)
	for i := 0; i <= maxJobSummaryIDs; i++ {
		ids = append(ids, "j"+strconv.Itoa(i))
	}
	rec := serveJobRoute(store, "/jobs/summary?ids="+strings.Join(ids, ","))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("too many ids status = %d want 400", rec.Code)
	}
}

func TestPublicJobsSummaryAggregatesScopeAndPhase(t *testing.T) {
	jobA := &pipeline.Job{ID: "job-a", Kind: pipeline.KindNarration, InputSnapshot: `{"slides":[{"slideId":"s1","scriptRevision":2}],"segmentIds":[]}`}
	jobB := &pipeline.Job{ID: "job-b", Kind: pipeline.KindScriptDraft, InputSnapshot: `{"revisionNo":9}`}
	store := &jobDetailInspectorStub{
		many: []*pipeline.Job{jobA, jobB},
		summaries: map[string]pipeline.JobStepSummary{
			"job-a": {Phase: "tts_segment", Total: 3, Counts: map[pipeline.JobStepState]int{pipeline.StepSuccess: 2, pipeline.StepPending: 1}},
		},
	}
	rec := serveJobRoute(store, "/jobs/summary?ids=job-a,job-b,job-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var got jobSummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 去重后传给 store 的 ids 只有两个。
	if len(store.lastManyIDs) != 2 {
		t.Fatalf("ids passed to store = %v want 2 items", store.lastManyIDs)
	}
	if got.StepsError != "" {
		t.Fatalf("stepsError = %q want empty", got.StepsError)
	}
	a, ok := got.Jobs["job-a"]
	if !ok {
		t.Fatalf("job-a missing from summary: %+v", got.Jobs)
	}
	if a.Phase != "tts_segment" || a.StepTotal != 3 || a.StepCounts["success"] != 2 {
		t.Fatalf("job-a entry = %+v", a)
	}
	if a.Scope.Kind != "pages" || a.Scope.PageCount != 1 {
		t.Fatalf("job-a scope = %+v", a.Scope)
	}
	b := got.Jobs["job-b"]
	// 无步骤的任务：phase 为空串（界面显示"—"），不伪造阶段。
	if b.Phase != "" || b.StepTotal != 0 {
		t.Fatalf("job-b entry = %+v", b)
	}
	if b.Scope.Kind != "project" || b.Scope.InputRevision != 9 {
		t.Fatalf("job-b scope = %+v", b.Scope)
	}
}

func TestPublicJobsSummaryReportsStepLoadFailure(t *testing.T) {
	store := &jobDetailInspectorStub{
		many:       []*pipeline.Job{{ID: "job-c", Kind: pipeline.KindParse, InputSnapshot: `{"revisionNo":1}`}},
		summaryErr: errTestSteps,
	}
	rec := serveJobRoute(store, "/jobs/summary?ids=job-c")
	var got jobSummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.StepsError != "load_failed" {
		t.Fatalf("stepsError = %q want load_failed", got.StepsError)
	}
	// 范围仍返回（部分可用），但阶段为空 —— 界面必须区分"无步骤"与"步骤读取失败"。
	if got.Jobs["job-c"].Scope.Kind != "project" {
		t.Fatalf("job-c scope = %+v", got.Jobs["job-c"].Scope)
	}
}

// 未实现 JobInspector 的 store：整体不可用 → 501（前端据此回退为不显示这两列并给出原因）。
func TestPublicJobsSummaryUnsupportedStore(t *testing.T) {
	rec := serveJobRoute(&jobDetailStub{}, "/jobs/summary?ids=job-a")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d want 501 (body=%s)", rec.Code, rec.Body.String())
	}
}
