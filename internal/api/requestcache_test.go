package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/animation/animstore"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/pipeline"
)

// 本文件守「一次请求里 document.json 与配音时间轴各读一次」。
//
// 起因：apply-all / apply-batch / restore 对每一页调一次 Resolve，而 Resolve 的实现
// 每页都读并解码整份 document.json，再读一次时间轴。实测 12 页是 48 次对象读
// （1 文档 + 3 时间轴，逐页重复），23 页的 deck 就是 92 次。数据在一次请求里不变，
// 这是纯浪费，而且都压在对象存储的连接与 RTT 上。

// countingObjects 数对象读次数。只包 Get：Put/Delete 在这条路径上不发生。
type countingObjects struct {
	objectstore.ObjectStore
	gets int32
}

func (c *countingObjects) Get(ctx context.Context, key objectstore.ObjectKey) (io.ReadCloser, objectstore.ObjectMeta, error) {
	atomic.AddInt32(&c.gets, 1)
	return c.ObjectStore.Get(ctx, key)
}

func (c *countingObjects) reset() { atomic.StoreInt32(&c.gets, 0) }

func (c *countingObjects) count() int32 { return atomic.LoadInt32(&c.gets) }

// deckFixture 造一份 n 页的解析产物 + 覆盖全部页的配音时间轴。
// 每页两个带包围盒的文字形状：没有元素就没有元素级轨道，用例就测不到生成链。
func deckFixture(t *testing.T, n int) (objectstore.ObjectStore, string, []string) {
	t.Helper()
	objects := &countingObjects{ObjectStore: testObjects(t)}

	pages := make([]map[string]any, 0, n)
	inputs := make([]media.SlideInput, 0, n)
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		slideID := fmt.Sprintf("slide-%02d", i)
		ids = append(ids, slideID)
		pages = append(pages, map[string]any{
			"index": i, "slideId": slideID,
			"shapes": []map[string]any{
				{"id": "sh-title", "kind": "text", "text": fmt.Sprintf("第 %d 页标题", i+1),
					"bounds": map[string]int64{"x": 800000, "y": 600000, "width": 8000000, "height": 900000}},
				{"id": "sh-body", "kind": "text", "text": fmt.Sprintf("要点 %d", i+1),
					"bounds": map[string]int64{"x": 800000, "y": 2000000, "width": 7000000, "height": 600000}},
			},
		})
		inputs = append(inputs, media.SlideInput{SlideID: slideID, Segments: []media.SegmentInput{
			{SegmentID: fmt.Sprintf("seg-%d-1", i), DisplayText: "第一段", AudioKey: fmt.Sprintf("%s/%s/cache/audio/%d.wav", db.LocalTenantID, anTestProject, i), DurationMS: 1000},
			{SegmentID: fmt.Sprintf("seg-%d-2", i), DisplayText: "第二段", AudioKey: fmt.Sprintf("%s/%s/cache/audio/%d-b.wav", db.LocalTenantID, anTestProject, i), DurationMS: 1000},
		}})
	}
	doc, err := json.Marshal(map[string]any{
		"schemaVersion": "1.0", "pages": pages, "features": map[string]any{"pageCount": n},
	})
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	putAPIObject(t, objects, objectstore.DocumentKey(db.LocalTenantID, anTestProject, srcRevString(1)), doc, "application/json")

	timeline, err := media.BuildTimeline(inputs, media.Timing{})
	if err != nil {
		t.Fatalf("build timeline: %v", err)
	}
	// SRT/VTT 键是 loadBundle 的完整性判据（缺一即 invalid timeline bundle）。
	bundle, err := json.Marshal(app.TimelineAsset{Timeline: timeline, SRTKey: "t/s.srt", VTTKey: "t/s.vtt"})
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	timelineKey := objectstore.ObjectKey{
		TenantID: db.LocalTenantID, ProjectID: anTestProject,
		Revision: "narration", AssetType: "timeline", AssetID: "tl", Ext: "json",
	}
	putAPIObject(t, objects, timelineKey, bundle, "application/json")
	return objects, timelineKey.String(), ids
}

// countingJobs 数 pageDurationMS 的两条 SQL（查最近成功任务、查步骤引用）。
// 它们和对象读一起逐页重复——只数对象读会漏掉一半的放大。
type countingJobs struct {
	JobCreator
	latest, refs int32
}

func (c *countingJobs) LatestSucceededJob(ctx context.Context, tenantID, projectID, kind string) (*pipeline.Job, error) {
	atomic.AddInt32(&c.latest, 1)
	return c.JobCreator.LatestSucceededJob(ctx, tenantID, projectID, kind)
}

func (c *countingJobs) StepResultRef(ctx context.Context, jobID, step string) (string, error) {
	atomic.AddInt32(&c.refs, 1)
	return c.JobCreator.StepResultRef(ctx, jobID, step)
}

// newRealResolverServer 用**真实的** animationResolver 装配 handler。
// 用假 resolver 测这件事没有意义：被缓存的正是它的读路径。
func newRealResolverServer(t *testing.T, store animstore.Store, objects objectstore.ObjectStore, stepRef string) (*httptest.Server, *countingJobs) {
	t.Helper()
	if store == nil {
		store = newAnimationTestStore(t)
	}
	jobs := &countingJobs{JobCreator: &narrationJobStub{job: &pipeline.Job{ID: "job-narr"}, stepRef: stepRef}}
	resolver := newAnimationResolver(&docProjectStore{&fakeProjectStore{}}, objects, nil, jobs)
	mux := http.NewServeMux()
	auth := func(h http.Handler) http.Handler {
		return AuthMiddlewareWithOptions(h, AuthOptions{
			LocalPrincipal: &Principal{TenantID: db.LocalTenantID, UserID: db.LocalUserID},
		})
	}
	NewAnimationHandler(store, resolver, true).
		WithDeck(resolver).
		Register(mux, auth)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, jobs
}

// postApplyAll 发一次「一键套用」，返回逐页结果。
func postApplyAll(t *testing.T, ts *httptest.Server) map[string]applyAllSlideResult {
	t.Helper()
	resp, err := http.Post(ts.URL+applyAllURL, "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("apply-all: %v", err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply-all status = %d payload = %v", resp.StatusCode, payload)
	}
	return applyAllResultOf(t, payload)
}

// TestApplyAllObjectReadsDoNotGrowWithSlides 是这条优化的判据：对象读次数与页数无关。
//
// 3 页与 12 页必须相同——改回逐页读立刻变成 6 vs 15（只丢文档缓存）或 7 vs 25
// （只丢时间轴缓存），一眼看出。上限 3 钉住「常数」具体是多少：1 份文档 +
// 时间轴（stat + 读）。若哪天时间轴也合并成一次读，这里会变小，届时收紧即可。
func TestApplyAllObjectReadsDoNotGrowWithSlides(t *testing.T) {
	readsFor := func(pages int) int32 {
		objects, timelineKey, ids := deckFixture(t, pages)
		counted := objects.(*countingObjects)
		ts, jobs := newRealResolverServer(t, nil, counted, timelineKey)
		counted.reset()
		atomic.StoreInt32(&jobs.latest, 0)
		atomic.StoreInt32(&jobs.refs, 0)
		results := postApplyAll(t, ts)
		if len(results) != len(ids) {
			t.Fatalf("%d 页只返回了 %d 条结果", len(ids), len(results))
		}
		// 每页都必须真的套上：读次数少也可能是因为整条链早退，那样用例就变成
		// "什么都没做所以很快"——一个自欺欺人的绿灯。
		for _, id := range ids {
			if results[id].Status != "applied" {
				t.Fatalf("页 %s 状态 = %s（%s），期望 applied", id, results[id].Status, results[id].Reason)
			}
		}
		// 两条 SQL 是 pageDurationMS 的另一半，同样必须与页数无关。
		if got := atomic.LoadInt32(&jobs.latest); got != 1 {
			t.Fatalf("%d 页请求查了 %d 次「最近成功配音任务」，期望 1", pages, got)
		}
		if got := atomic.LoadInt32(&jobs.refs); got != 1 {
			t.Fatalf("%d 页请求查了 %d 次步骤引用，期望 1", pages, got)
		}
		return counted.count()
	}

	small, large := readsFor(3), readsFor(12)
	if small != large {
		t.Fatalf("对象读次数随页数增长：3 页 %d 次，12 页 %d 次（应为常数）", small, large)
	}
	if large > 3 {
		t.Fatalf("12 页请求读了 %d 次对象，期望 ≤3（文档 1 次 + 时间轴 2 次）", large)
	}
}

// TestApplyAllReadsDeckOncePerRequest 守住缓存的作用域：同一条 server 连发两次请求，
// 每次都要各自读一次文档。缓存若挂错到进程级，第二次会读到 0 次——那种"优化"
// 在用户改了源版本之后会一直返回旧页面。
func TestApplyAllReadsDeckOncePerRequest(t *testing.T) {
	objects, timelineKey, _ := deckFixture(t, 5)
	counted := objects.(*countingObjects)
	ts, _ := newRealResolverServer(t, nil, counted, timelineKey)

	first := int32(0)
	for i := 0; i < 2; i++ {
		counted.reset()
		postApplyAll(t, ts)
		got := counted.count()
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("第 %d 次请求读了 %d 次对象，第一次是 %d：缓存跨请求复用了", i+1, got, first)
		}
	}
}

// TestRequestCacheIsIdempotentAndCachesNegative 守 memo 的两条语义：
// 同一个 ctx 上重复挂载必须是同一份（否则两处各建一份空缓存，谁也命中不了谁）；
// nil 是结论，必须能和"没算过"区分（读不到文档 / 没有时间轴都要缓存）。
func TestRequestCacheIsIdempotentAndCachesNegative(t *testing.T) {
	ctx := withRequestCache(context.Background())
	if again := withRequestCache(ctx); requestCacheFrom(again) != requestCacheFrom(ctx) {
		t.Fatal("重复挂载产生了第二份缓存")
	}
	cache := requestCacheFrom(ctx)
	key := documentCacheKey{tenantID: "t", projectID: "p", revision: 1}
	if _, ok := cache.documents.load(key); ok {
		t.Fatal("空缓存不该有命中")
	}
	cache.documents.store(key, nil)
	doc, ok := cache.documents.load(key)
	if !ok || doc != nil {
		t.Fatalf("nil 负缓存没生效：doc=%v ok=%v", doc, ok)
	}
	if requestCacheFrom(context.Background()) != nil {
		t.Fatal("未挂缓存的 ctx 不该有缓存")
	}
}

// TestApplyBatchObjectReadsDoNotGrowWithSlides 守同一条判据在批量套用上的落点。
//
// apply-batch 比 apply-all 更需要它：它一次最多接受 200 页，逐页读整份 document.json
// 在 200 页时就是 201 次对象读，而这条路径还要在同一次请求里做「全成或全不成」的预检。
func TestApplyBatchObjectReadsDoNotGrowWithSlides(t *testing.T) {
	const pages = 12
	objects, timelineKey, ids := deckFixture(t, pages)
	counted := objects.(*countingObjects)
	store := newAnimationTestStore(t)

	// 源页要有一份带预设的规格：apply-batch 复制的只是「风格」（preset + 强度），
	// 轨道必须由服务端按目标页重算。
	if _, err := store.Put(context.Background(), &animation.Revision{
		TenantID: db.LocalTenantID, ProjectID: anTestProject, SlideID: ids[0],
		Language: "zh-CN", SourceRevisionNo: 1, Status: animation.StatusDraft,
		PresetID: animdefault.DefaultPresetID, Intensity: 1,
	}, 0); err != nil {
		t.Fatalf("seed source spec: %v", err)
	}

	ts, jobs := newRealResolverServer(t, store, counted, timelineKey)
	counted.reset()
	atomic.StoreInt32(&jobs.latest, 0)
	atomic.StoreInt32(&jobs.refs, 0)

	body, err := json.Marshal(map[string]any{"sourceSlideId": ids[0], "slideIds": ids[1:]})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	resp, err := http.Post(ts.URL+"/api/projects/"+anTestProject+"/animation/apply-batch?sourceRevisionNo=1&language=zh-CN",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("apply-batch: %v", err)
	}
	defer resp.Body.Close()
	var payload applyBatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply-batch status = %d payload = %+v", resp.StatusCode, payload)
	}
	if payload.Applied != pages-1 {
		t.Fatalf("applied = %d, want %d（读次数少也可能是一页都没写成）", payload.Applied, pages-1)
	}
	if got := counted.count(); got > 3 {
		t.Fatalf("%d 页批量套用读了 %d 次对象，期望 ≤3（文档 1 次 + 时间轴 2 次）", pages, got)
	}
	if got := atomic.LoadInt32(&jobs.latest); got != 1 {
		t.Fatalf("查了 %d 次「最近成功配音任务」，期望 1", got)
	}
}
