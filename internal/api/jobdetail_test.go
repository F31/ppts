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

// B4-M6a/M6b 测试：/jobs/{jid}/detail 与 /jobs/summary 的端点行为。
//
// 范围推导（pipeline.ScopeOf）的用例在 internal/pipeline/scope_test.go：它是纯函数，
// 放在 pipeline 里可以不依赖 HTTP 直接跑。此处只测「传输层」行为——上限截断、错误映射、字段裁剪。

// jobDetailStub 只实现 JobStore（不含 JobInspector），用于验证"store 不支持扩展读取"的降级。
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

func (s *jobDetailStub) StepResultRef(context.Context, string, string) (string, error) {
	return "", nil
}

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

// jobDetailInspectorStub 在 JobStore 之上实现 JobInspector（步骤读取 + 批量取任务）。
type jobDetailInspectorStub struct {
	jobDetailStub
	steps       []pipeline.JobStep
	many        []*pipeline.Job
	stepsErr    error
	manyErr     error
	lastManyIDs []string
}

func (s *jobDetailInspectorStub) ListSteps(context.Context, string, string) ([]pipeline.JobStep, error) {
	if s.stepsErr != nil {
		return nil, s.stepsErr
	}
	return s.steps, nil
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

// jobSummaryResponse 只声明契约里**确实存在**的字段（B4-M6b 已裁掉 phase/stepTotal/stepCounts）。
type jobSummaryResponse struct {
	Jobs map[string]struct {
		Scope struct {
			Kind          string   `json:"kind"`
			PageCount     int      `json:"pageCount"`
			AffectedPages []string `json:"affectedPages"`
			InputRevision int64    `json:"inputRevision"`
			Format        string   `json:"format"`
		} `json:"scope"`
	} `json:"jobs"`
}

func decodeDetail(t *testing.T, rec *httptest.ResponseRecorder) jobDetailResponse {
	t.Helper()
	var out jobDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode detail response: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

// 超出上限时 affectedPages 截断，但 pageCount 仍是完整计数（前端据此判断"已截断"）。
func TestScopeForTruncatesPagesButKeepsFullCount(t *testing.T) {
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
	got := scopeFor("narration", b.String())
	if got.PageCount != maxScopePages+1 {
		t.Fatalf("pageCount = %d want %d", got.PageCount, maxScopePages+1)
	}
	if len(got.AffectedPages) != maxScopePages {
		t.Fatalf("affectedPages len = %d want %d", len(got.AffectedPages), maxScopePages)
	}
}

// 未超上限时不应无谓改动（避免把「截断」误报出来）。
func TestScopeForKeepsPagesWhenUnderLimit(t *testing.T) {
	got := scopeFor("narration", `{"slides":[{"slideId":"s1"},{"slideId":"s2"}],"segmentIds":[]}`)
	if len(got.AffectedPages) != 2 || got.PageCount != 2 {
		t.Fatalf("scope = %+v", got)
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

// B4-M6b：/jobs/summary 的契约已收窄为「只回范围」——不再回 phase/stepTotal/stepCounts，
// 因此这里同时断言「范围按 kind 正确推导」与「不再产出多余的步骤聚合字段」。
func TestPublicJobsSummaryReturnsScopePerJob(t *testing.T) {
	jobA := &pipeline.Job{ID: "job-a", Kind: pipeline.KindNarration, InputSnapshot: `{"slides":[{"slideId":"s1","scriptRevision":2}],"segmentIds":[]}`}
	jobB := &pipeline.Job{ID: "job-b", Kind: pipeline.KindScriptDraft, InputSnapshot: `{"revisionNo":9}`}
	store := &jobDetailInspectorStub{many: []*pipeline.Job{jobA, jobB}}
	rec := serveJobRoute(store, "/jobs/summary?ids=job-a,job-b,job-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	// 去重后传给 store 的 ids 只有两个。
	if len(store.lastManyIDs) != 2 {
		t.Fatalf("ids passed to store = %v want 2 items", store.lastManyIDs)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 顶层只应有 jobs 一个键：StepsError 等字段已被裁掉。
	if len(got) != 1 {
		t.Fatalf("top-level keys = %v want only [jobs]", got)
	}
	var parsed jobSummaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("decode typed: %v", err)
	}
	a, ok := parsed.Jobs["job-a"]
	if !ok {
		t.Fatalf("job-a missing from summary: %+v", parsed.Jobs)
	}
	if a.Scope.Kind != "pages" || a.Scope.PageCount != 1 || a.Scope.InputRevision != 2 {
		t.Fatalf("job-a scope = %+v", a.Scope)
	}
	b := parsed.Jobs["job-b"]
	if b.Scope.Kind != "project" || b.Scope.InputRevision != 9 {
		t.Fatalf("job-b scope = %+v", b.Scope)
	}
}

// 未实现 JobInspector 的 store：整体不可用 → 501（前端据此回退为不显示范围列并给出原因）。
func TestPublicJobsSummaryUnsupportedStore(t *testing.T) {
	rec := serveJobRoute(&jobDetailStub{}, "/jobs/summary?ids=job-a")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d want 501 (body=%s)", rec.Code, rec.Body.String())
	}
}
