package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/animation/animstore"
	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/pipeline"
)

// 本文件是「异步预填」的 HTTP 侧验收（M1b）：
//
//	只该问模型的档才建任务；规则档必须当场返回；受理时先给一份规则底稿；
//	结果按内容指纹缓存，页面变了要能说出「stale」而不是套用旧结果。

// fakePrefillJobs 是最小的任务存储替身。Create 复刻真实实现的幂等语义：
// 同一把（tenant, kind, idemKey）返回同一行，因此重复 POST 不会造出第二个任务。
type fakePrefillJobs struct {
	job     *pipeline.Job
	creates int
	idem    string
}

func (f *fakePrefillJobs) Create(_ context.Context, tenantID, projectID, kind, idemKey, snapshot string, _ time.Time) (*pipeline.Job, error) {
	f.creates++
	if f.job != nil && f.idem == idemKey {
		return f.job, nil
	}
	f.idem = idemKey
	f.job = &pipeline.Job{
		ID:            "job-prefill-1",
		TenantID:      tenantID,
		ProjectID:     projectID,
		Kind:          pipeline.JobKind(kind),
		State:         pipeline.StateQueued,
		InputSnapshot: snapshot,
		CreatedAt:     time.Now(),
	}
	return f.job, nil
}

func (f *fakePrefillJobs) Get(_ context.Context, id, tenantID string) (*pipeline.Job, error) {
	if f.job == nil || f.job.ID != id || f.job.TenantID != tenantID {
		return nil, pipeline.ErrJobNotFound
	}
	return f.job, nil
}

type prefillHarness struct {
	ts       *httptest.Server
	resolver *fakeAnimationResolver
	objects  objectstore.ObjectStore
	jobs     *fakePrefillJobs
	store    animstore.Store
}

// newPrefillHarness 装配一条带异步预填的动画路由。
// wired=false 时用老构造（不注入任务/对象存储），用来钉住「没接线就明确不可用」。
func newPrefillHarness(t *testing.T, pc animation.PageContext, wired bool) *prefillHarness {
	t.Helper()
	store := newAnimationTestStore(t)
	page := prefillPage()
	pc.Elements = animdefault.PageAnchors(page)
	resolver := &fakeAnimationResolver{page: page, pc: pc}
	objects := objectstore.NewLocal(t.TempDir(), []byte("test-secret"))
	jobs := &fakePrefillJobs{}

	mux := http.NewServeMux()
	auth := func(h http.Handler) http.Handler {
		return AuthMiddlewareWithOptions(h, AuthOptions{
			LocalPrincipal: &Principal{TenantID: db.LocalTenantID, UserID: db.LocalUserID},
		})
	}
	h := NewAnimationHandler(store, resolver, true)
	if wired {
		h = h.WithPrefill(jobs, objects)
	}
	h.Register(mux, auth)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &prefillHarness{ts: ts, resolver: resolver, objects: objects, jobs: jobs, store: store}
}

func (h *prefillHarness) url() string { return h.ts.URL + animationURL() + "/prefill" }

func (h *prefillHarness) key() animation.Key {
	return animation.Key{
		TenantID:  db.LocalTenantID,
		ProjectID: anTestProject,
		SlideID:   anTestSlide,
		Language:  "zh-CN",
	}
}

func (h *prefillHarness) snapshot() bind.Snapshot {
	return prefillSnapshot(h.resolver.page, h.resolver.pc, h.key(), "")
}

// 版面无结构信号、讲稿也没念出结构的页（reason=script）才会被路由给模型。
func TestPrefillPostCreatesJobOnlyForModelBand(t *testing.T) {
	cases := []struct {
		name      string
		segs      []animation.ScriptSegment
		wantCode  int
		wantJob   bool
		wantRoute string
	}{
		{
			name:      "讲稿口语直讲 → 问模型",
			segs:      prefillSegments(),
			wantCode:  http.StatusAccepted,
			wantJob:   true,
			wantRoute: "rule", // 受理时给的是规则底稿，模型结果稍后由 GET 换
		},
		{
			name: "讲稿把结构念出来 → 规则更准，不建任务",
			segs: []animation.ScriptSegment{
				{ID: "seg-1", Text: "第一部分看整体：收入增长 18%。"},
				{ID: "seg-2", Text: "第二部分看毛利率，42%，比上季度高一个点。"},
			},
			wantCode:  http.StatusOK,
			wantJob:   false,
			wantRoute: "rule",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newPrefillHarness(t, prefillPC(c.segs...), true)
			status, payload := doJSON(t, http.MethodPost, h.url(), nil)
			if status != c.wantCode {
				t.Fatalf("status = %d，期望 %d（%v）", status, c.wantCode, payload)
			}
			gotJob, _ := payload["jobId"].(string)
			if c.wantJob != (gotJob != "") {
				t.Fatalf("jobId = %q，wantJob=%v", gotJob, c.wantJob)
			}
			spec, _ := payload["spec"].(map[string]any)
			if spec == nil {
				t.Fatalf("受理时没给任何 spec：编辑器在模型回来之前会空着（%v）", payload)
			}
			info, _ := spec["prefill"].(map[string]any)
			if info == nil {
				t.Fatal("spec 里没有 prefill 说明")
			}
			if route, _ := info["route"].(string); route != c.wantRoute {
				t.Fatalf("route = %q，期望 %q", route, c.wantRoute)
			}
			// 基线是「不带模型」跑出来的，它的 no_model 只说明这次调用的参数，
			// 不是这一页的结论。透出它会让结构化页也显示「还没有配置模型」——假话。
			if fb, _ := info["modelFallback"].(string); fb != "" {
				t.Fatalf("modelFallback = %q，基线不该带模型回退原因", fb)
			}
			if c.wantJob {
				if h.jobs.creates != 1 {
					t.Fatalf("建任务次数 = %d，期望 1", h.jobs.creates)
				}
				// 底稿必须是可用的一份规格，而不是空壳。
				if tracks, _ := spec["tracks"].([]any); len(tracks) == 0 {
					t.Fatal("受理时的规则底稿没有任何轨道")
				}
			} else if h.jobs.creates != 0 {
				t.Fatalf("规则档却建了任务（creates=%d）：多花十几秒只是拉低结果", h.jobs.creates)
			}
		})
	}
}

// 没有分段：说清「没做」，而不是给一份看起来做了的结果。
func TestPrefillPostWithoutSegments(t *testing.T) {
	h := newPrefillHarness(t, animation.PageContext{DurationMS: 6000, FPS: 30}, true)
	status, payload := doJSON(t, http.MethodPost, h.url(), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d (%v)", status, payload)
	}
	spec, _ := payload["spec"].(map[string]any)
	info, _ := spec["prefill"].(map[string]any)
	if info == nil || info["applied"] != false || info["reason"] != "no_segments" {
		t.Fatalf("prefill = %v，期望 applied=false reason=no_segments", info)
	}
	if h.jobs.creates != 0 {
		t.Fatal("没有分段却建了任务")
	}
}

// 同内容已经有结果时不再建任务：内容指纹就是缓存键，重复打开编辑器不该再花一次模型钱。
func TestPrefillPostReusesCachedResult(t *testing.T) {
	h := newPrefillHarness(t, prefillPC(prefillSegments()...), true)
	snap := h.snapshot()
	stored := &app.PrefillResult{
		SchemaVersion: "1",
		SlideID:       anTestSlide,
		ContentHash:   snap.ContentHash(),
		JobID:         "job-earlier",
		Route:         "model",
		Band:          "low-confidence",
		BandReason:    "script",
		Bindings: []bind.Binding{
			{ElementID: "shape-b1", SegmentID: "seg-1", Source: animation.TriggerSourceModel, Confidence: 0.9},
			{ElementID: "shape-b2", SegmentID: "seg-2", Source: animation.TriggerSourceModel, Confidence: 0.8},
		},
		GeneratedAt: time.Now().UTC(),
	}
	if err := app.SavePrefillResult(context.Background(), h.objects, db.LocalTenantID, anTestProject, 0, stored); err != nil {
		t.Fatalf("写缓存结果: %v", err)
	}

	status, payload := doJSON(t, http.MethodPost, h.url(), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d，命中缓存应直接 200（%v）", status, payload)
	}
	if id, _ := payload["jobId"].(string); id != "job-earlier" {
		t.Fatalf("jobId = %q，期望沿用产生这份结果的任务", id)
	}
	if h.jobs.creates != 0 {
		t.Fatal("命中缓存却建了任务")
	}
	spec, _ := payload["spec"].(map[string]any)
	info, _ := spec["prefill"].(map[string]any)
	if route, _ := info["route"].(string); route != "model" {
		t.Fatalf("route = %q，期望 model（这是模型产出的结果）", route)
	}
}

// GET 的三个终态：还在跑、跑完了（且页面没变）、页面变了。
func TestPrefillGetStates(t *testing.T) {
	t.Run("还在跑", func(t *testing.T) {
		h := newPrefillHarness(t, prefillPC(prefillSegments()...), true)
		_, payload := doJSON(t, http.MethodPost, h.url(), nil)
		jobID, _ := payload["jobId"].(string)

		status, got := doJSON(t, http.MethodGet, h.url()+"?jobId="+jobID, nil)
		if status != http.StatusAccepted || got["status"] != "pending" {
			t.Fatalf("status=%d payload=%v，期望 202 pending", status, got)
		}
	})

	t.Run("跑完了", func(t *testing.T) {
		h := newPrefillHarness(t, prefillPC(prefillSegments()...), true)
		_, payload := doJSON(t, http.MethodPost, h.url(), nil)
		jobID, _ := payload["jobId"].(string)

		h.jobs.job.State = pipeline.StateSucceeded
		snap := h.snapshot()
		stored := &app.PrefillResult{
			SchemaVersion: "1", SlideID: anTestSlide, ContentHash: snap.ContentHash(),
			JobID: jobID, Route: "model", Band: "low-confidence", BandReason: "script",
			Bindings:    []bind.Binding{{ElementID: "shape-b1", SegmentID: "seg-2", Source: animation.TriggerSourceModel, Confidence: 0.9}},
			GeneratedAt: time.Now().UTC(),
		}
		if err := app.SavePrefillResult(context.Background(), h.objects, db.LocalTenantID, anTestProject, 0, stored); err != nil {
			t.Fatalf("写结果: %v", err)
		}

		status, got := doJSON(t, http.MethodGet, h.url()+"?jobId="+jobID, nil)
		if status != http.StatusOK || got["status"] != "succeeded" {
			t.Fatalf("status=%d payload=%v，期望 200 succeeded", status, got)
		}
		spec, _ := got["spec"].(map[string]any)
		if spec == nil {
			t.Fatal("成功却没有 spec")
		}
		// 模型把 b1 绑到了 seg-2，响应必须反映这一点，而不是回落成「第 1 条绑第 1 段」。
		if trig := triggersOf(t, spec)["shape-b1"]; trig != "withSegment:seg-2" {
			t.Fatalf("shape-b1 触发器 = %q，期望 withSegment:seg-2（模型结果没被采纳）", trig)
		}
		info, _ := spec["prefill"].(map[string]any)
		if route, _ := info["route"].(string); route != "model" {
			t.Fatalf("route = %q，期望 model", route)
		}
	})

	t.Run("页面变了", func(t *testing.T) {
		h := newPrefillHarness(t, prefillPC(prefillSegments()...), true)
		_, payload := doJSON(t, http.MethodPost, h.url(), nil)
		jobID, _ := payload["jobId"].(string)

		// 任务跑完之前用户改了讲稿：结果算的是另一版内容，套上去就是错绑。
		h.resolver.pc.Segments = append([]animation.ScriptSegment{{ID: "seg-9", Text: "新加的一段。"}}, h.resolver.pc.Segments...)
		h.resolver.pc.SegmentIDs = append([]string{"seg-9"}, h.resolver.pc.SegmentIDs...)
		h.jobs.job.State = pipeline.StateSucceeded

		status, got := doJSON(t, http.MethodGet, h.url()+"?jobId="+jobID, nil)
		if status != http.StatusOK || got["status"] != "stale" {
			t.Fatalf("status=%d payload=%v，期望 200 stale", status, got)
		}
		if _, ok := got["spec"]; ok {
			t.Fatal("过期的结果不该带 spec：调用方会照着它渲染")
		}
	})
}

// 任务失败时明确报 failed，且不给 spec——手里的规则底稿就是最终结果。
func TestPrefillGetFailedJob(t *testing.T) {
	h := newPrefillHarness(t, prefillPC(prefillSegments()...), true)
	_, payload := doJSON(t, http.MethodPost, h.url(), nil)
	jobID, _ := payload["jobId"].(string)

	h.jobs.job.State = pipeline.StateFailed
	h.jobs.job.LastError = &pipeline.JobError{Code: "internal", Message: "boom"}

	status, got := doJSON(t, http.MethodGet, h.url()+"?jobId="+jobID, nil)
	if status != http.StatusOK || got["status"] != "failed" {
		t.Fatalf("status=%d payload=%v，期望 200 failed", status, got)
	}
	if reason, _ := got["reason"].(string); reason != "internal" {
		t.Fatalf("reason = %q，期望透出任务的错误码", reason)
	}
}

// 幂等键命中的是已终结任务时，不能把同一个 jobId 再抛给客户端：
// 那会让这一页的预填永久停在上一次的失败上——重开编辑器也只会等到同一个终态。
func TestPrefillPostRetriesAfterTerminalJob(t *testing.T) {
	h := newPrefillHarness(t, prefillPC(prefillSegments()...), true)
	snap := h.snapshot()
	hash := snap.ContentHash()
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	h.jobs.idem = "prefill:" + anTestSlide + ":" + hash
	h.jobs.job = &pipeline.Job{
		ID:            "job-dead",
		TenantID:      db.LocalTenantID,
		ProjectID:     anTestProject,
		Kind:          pipeline.KindAnimationPrefill,
		State:         pipeline.StateFailed,
		InputSnapshot: string(raw),
	}

	status, payload := doJSON(t, http.MethodPost, h.url(), nil)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d（%v），期望 202：应换一把键重开", status, payload)
	}
	if id, _ := payload["jobId"].(string); id == "job-dead" {
		t.Fatal("仍然返回了那个已失败的任务：这一页的预填从此永久失败")
	}
	if h.jobs.creates != 2 {
		t.Fatalf("建任务次数 = %d，期望 2（第一次命中终态、第二次换键重开）", h.jobs.creates)
	}
}

// 没有接线（无任务/对象存储）时明确说不可用，而不是悄悄跑一次同步模型调用。
func TestPrefillRequiresWiring(t *testing.T) {
	h := newPrefillHarness(t, prefillPC(prefillSegments()...), false)
	status, payload := doJSON(t, http.MethodPost, h.url(), nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d，期望 503 feature_unavailable（%v）", status, payload)
	}
	if code, _ := payload["code"].(string); code != "feature_unavailable" {
		t.Fatalf("code = %q", code)
	}
}

// jobId 为空要明确报 400：不猜「最近一个任务」，那会把别的页的结果套到这页上。
func TestPrefillGetRequiresJobID(t *testing.T) {
	h := newPrefillHarness(t, prefillPC(prefillSegments()...), true)
	status, payload := doJSON(t, http.MethodGet, h.url(), nil)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d（%v），期望 400", status, payload)
	}
	if code, _ := payload["code"].(string); code != "invalid_job_id" {
		t.Fatalf("code = %q", code)
	}
}

// 预填会改产物形态，能力关闭时必须与写规格一样返回 404，不暴露半成品。
func TestPrefillPostRespectsGate(t *testing.T) {
	store := newAnimationTestStore(t)
	page := prefillPage()
	pc := prefillPC(prefillSegments()...)
	pc.Elements = animdefault.PageAnchors(page)
	resolver := &fakeAnimationResolver{page: page, pc: pc}
	mux := http.NewServeMux()
	auth := func(h http.Handler) http.Handler {
		return AuthMiddlewareWithOptions(h, AuthOptions{LocalPrincipal: &Principal{TenantID: db.LocalTenantID, UserID: db.LocalUserID}})
	}
	NewAnimationHandler(store, resolver, false).
		WithPrefill(&fakePrefillJobs{}, objectstore.NewLocal(t.TempDir(), []byte("k"))).
		Register(mux, auth)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	status, payload := doJSON(t, http.MethodPost, ts.URL+animationURL()+"/prefill", nil)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d（%v），期望 404", status, payload)
	}
}

// 快照必须把元素与分段一起带上：worker 不重新解析，只吃这一份。
func TestPrefillJobSnapshotIsSelfContained(t *testing.T) {
	h := newPrefillHarness(t, prefillPC(prefillSegments()...), true)
	doJSON(t, http.MethodPost, h.url(), nil)
	if h.jobs.lastSnapshotForTest() == "" {
		t.Fatal("没有记录任务快照")
	}
	var snap bind.Snapshot
	if err := json.Unmarshal([]byte(h.jobs.lastSnapshotForTest()), &snap); err != nil {
		t.Fatalf("快照不是合法 JSON: %v", err)
	}
	if snap.SlideID != anTestSlide {
		t.Errorf("slideId = %q", snap.SlideID)
	}
	if len(snap.Elements) == 0 || len(snap.Segments) == 0 {
		t.Fatalf("快照缺少元素或分段：worker 拿不到输入（elements=%d segments=%d）", len(snap.Elements), len(snap.Segments))
	}
	if snap.ContentHash() == "" {
		t.Error("快照没有内容指纹")
	}
}

// lastSnapshotForTest 取最后一次建任务时的快照（测试只关切任务，不关心幂等细节）。
func (f *fakePrefillJobs) lastSnapshotForTest() string {
	if f.job == nil {
		return ""
	}
	return f.job.InputSnapshot
}
