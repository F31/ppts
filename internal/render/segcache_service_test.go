package render

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/F31/ppts/internal/animation"
)

// 服务层接线的判据是「backend 收到了哪些页」与「产物从哪来」，
// 不是耗时：耗时随机器波动，而「第二次一页都没渲」是结构事实。

// fakeMP4Backend 产出一个能过 verifyMP4 的假片段（ftyp + 填充），
// 并记录每次被要求渲染的页。
type fakeMP4Backend struct {
	mu       sync.Mutex
	requests [][]int
	degraded []PageDegradation
	// fail 非 nil 时整次失败。
	fail error
}

func (b *fakeMP4Backend) Name() string { return "fake-mp4" }

func (b *fakeMP4Backend) Render(ctx context.Context, plan *Timeline, dir string,
	progress func(done, total int)) (RenderReport, error) {
	return b.RenderPages(ctx, plan, dir, allPages(len(plan.Pages)), progress)
}

func (b *fakeMP4Backend) RenderPages(ctx context.Context, plan *Timeline, dir string, pages []int,
	progress func(done, total int)) (RenderReport, error) {
	if b.fail != nil {
		return RenderReport{}, b.fail
	}
	b.mu.Lock()
	b.requests = append(b.requests, append([]int(nil), pages...))
	b.mu.Unlock()
	for n, i := range pages {
		body := make([]byte, 2048)
		copy(body[4:], "ftyp")
		// 每页内容带上页号与动画输入，便于验证「缓存没有串页」。
		copy(body[16:], fmt.Sprintf("page=%d duration=%d clip=%d", i, plan.Pages[i].DurationMs,
			clipDurationMs(plan.Pages[i])))
		if err := os.WriteFile(filepath.Join(dir, SegmentName(i)), body, 0o640); err != nil {
			return RenderReport{}, err
		}
		if progress != nil {
			progress(n+1, len(pages))
		}
	}
	return RenderReport{Degraded: append([]PageDegradation(nil), b.degraded...)}, nil
}

func (b *fakeMP4Backend) SegmentEnv() SegmentEnv { return SegmentEnv{Encoding: "crf=18;gop=30"} }

func (b *fakeMP4Backend) Requests() [][]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([][]int, len(b.requests))
	copy(out, b.requests)
	return out
}

func clipDurationMs(p Page) int64 {
	for _, l := range p.Layers {
		for _, c := range l.Clips {
			return c.DurationMs
		}
	}
	return 0
}

// cachePlan 造一份 n 页、每页都有转场的计划（转场让「改一页脏两页」成立）。
func cachePlan(n int) *Timeline {
	plan := multiPagePlan(n)
	for i := range plan.Pages {
		plan.Pages[i].BaseImage = fmt.Sprintf("pages/p%d.png", i)
		if i > 0 {
			plan.Pages[i].Transition = Transition{Type: animation.TransitionFade, DurationMs: 300}
		}
	}
	return plan
}

func newCacheService(t *testing.T, be Backend, cache SegmentCache) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	svc, err := NewService(ServiceConfig{
		Backend:      be,
		WorkDir:      dir,
		Version:      "test-1",
		SegmentCache: cache,
		Logf:         func(string, ...any) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, dir
}

func newCache(t *testing.T) *DirSegmentCache {
	t.Helper()
	c, err := NewDirSegmentCache(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSegmentCacheSecondRunRendersNothing(t *testing.T) {
	cache := newCache(t)
	first := &fakeMP4Backend{}
	svc1, _ := newCacheService(t, first, cache)
	id, err := svc1.Submit(cachePlan(5))
	if err != nil {
		t.Fatal(err)
	}
	st := waitState(t, svc1, id)
	if st.State != StateSucceeded {
		t.Fatalf("first run: %s (%s)", st.State, st.Error)
	}
	if st.CachedPages != 0 || st.RenderedPages != 5 {
		t.Fatalf("first run counts = cached %d rendered %d, want 0/5", st.CachedPages, st.RenderedPages)
	}

	// 新 Service 等价于「进程重启」：planHash 去重不会参与，只剩片段缓存。
	second := &fakeMP4Backend{}
	svc2, _ := newCacheService(t, second, cache)
	id2, err := svc2.Submit(cachePlan(5))
	if err != nil {
		t.Fatal(err)
	}
	st2 := waitState(t, svc2, id2)
	if st2.State != StateSucceeded {
		t.Fatalf("second run: %s (%s)", st2.State, st2.Error)
	}
	if got := second.Requests(); len(got) != 0 {
		t.Fatalf("second run must not call the backend, got requests %v", got)
	}
	if st2.CachedPages != 5 || st2.RenderedPages != 0 {
		t.Fatalf("second run counts = cached %d rendered %d, want 5/0", st2.CachedPages, st2.RenderedPages)
	}
	if len(st2.Artifacts) != 5 {
		t.Fatalf("artifacts = %d, want 5", len(st2.Artifacts))
	}
}

func TestSegmentCachePartialHitRendersOnlyDirtyPages(t *testing.T) {
	cache := newCache(t)
	svc1, _ := newCacheService(t, &fakeMP4Backend{}, cache)
	id, _ := svc1.Submit(cachePlan(10))
	waitState(t, svc1, id)

	// 只改第 5 页的动画时长：第 5 页脏，第 6 页因其转场画第 5 页末帧也脏。
	edited := cachePlan(10)
	edited.Pages[5].Layers[0].Clips[0].DurationMs = 900

	be := &fakeMP4Backend{}
	svc2, _ := newCacheService(t, be, cache)
	id2, _ := svc2.Submit(edited)
	st := waitState(t, svc2, id2)
	if st.State != StateSucceeded {
		t.Fatalf("state = %s (%s)", st.State, st.Error)
	}
	got := be.Requests()
	if len(got) != 1 {
		t.Fatalf("backend calls = %d, want 1", len(got))
	}
	want := []int{5, 6}
	if fmt.Sprint(got[0]) != fmt.Sprint(want) {
		t.Fatalf("rendered pages = %v, want %v", got[0], want)
	}
	if st.CachedPages != 8 || st.RenderedPages != 2 {
		t.Fatalf("counts = cached %d rendered %d, want 8/2", st.CachedPages, st.RenderedPages)
	}

	// 命中页的产物必须仍是旧内容、被改的页必须换新：缓存不能串页。
	checkArtifactMentions(t, svc2, st, 4, 500)
	checkArtifactMentions(t, svc2, st, 5, 900)
}

// checkArtifactMentions 读回产物，确认片段内容里的 clip 时长是期望值。
func checkArtifactMentions(t *testing.T, svc *Service, st JobStatus, page int, clipMs int64) {
	t.Helper()
	if len(st.Artifacts) == 0 {
		t.Fatal("no artifacts")
	}
	path := filepath.Join(svc.cfg.WorkDir, st.ID, SegmentName(page))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read artifact page %d: %v", page, err)
	}
	want := fmt.Sprintf("clip=%d", clipMs)
	if !contains(string(data), want) {
		t.Fatalf("page %d artifact does not contain %q: %q", page, want, string(data[16:64]))
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

func TestSegmentCacheTruncatedEntryIsRerendered(t *testing.T) {
	cache := newCache(t)
	svc1, _ := newCacheService(t, &fakeMP4Backend{}, cache)
	plan := cachePlan(3)
	id, _ := svc1.Submit(plan)
	waitState(t, svc1, id)

	keys, err := SegmentKeys(plan, SegmentEnv{RendererVersion: "test-1", Encoding: "crf=18;gop=30"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := cache.pathFor(keys[1])
	if err := os.WriteFile(p, []byte("truncated"), 0o640); err != nil {
		t.Fatal(err)
	}

	be := &fakeMP4Backend{}
	svc2, _ := newCacheService(t, be, cache)
	id2, _ := svc2.Submit(cachePlan(3))
	st := waitState(t, svc2, id2)
	if st.State != StateSucceeded {
		t.Fatalf("state = %s (%s)", st.State, st.Error)
	}
	got := be.Requests()
	if len(got) != 1 || fmt.Sprint(got[0]) != "[1]" {
		t.Fatalf("rendered pages = %v, want [[1]]", got)
	}
	if st.CachedPages != 2 || st.RenderedPages != 1 {
		t.Fatalf("counts = %d/%d, want 2/1", st.CachedPages, st.RenderedPages)
	}
}

func TestSegmentCacheDegradedPageIsNotStored(t *testing.T) {
	cache := newCache(t)
	be := &fakeMP4Backend{degraded: []PageDegradation{{Page: 1, Code: DegradationStill, Message: "boom"}}}
	svc, _ := newCacheService(t, be, cache)
	plan := cachePlan(3)
	id, _ := svc.Submit(plan)
	if st := waitState(t, svc, id); st.State != StateSucceeded {
		t.Fatalf("state = %s", st.State)
	}
	keys, err := SegmentKeys(plan, SegmentEnv{RendererVersion: "test-1", Encoding: "crf=18;gop=30"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.Lookup(keys[1]); ok {
		t.Fatal("degraded page must not be cached: a transient failure would become permanent")
	}
	for _, i := range []int{0, 2} {
		if _, ok := cache.Lookup(keys[i]); !ok {
			t.Fatalf("page %d should be cached", i)
		}
	}
}

// errCache 模拟「缓存整个坏掉」：读写都报错。
type errCache struct{}

func (errCache) Get(context.Context, string, string) (bool, error) {
	return false, errors.New("cache get exploded")
}
func (errCache) Put(context.Context, string, string) error {
	return errors.New("cache put exploded")
}

func TestSegmentCacheFailuresNeverFailTheJob(t *testing.T) {
	be := &fakeMP4Backend{}
	svc, _ := newCacheService(t, be, errCache{})
	id, err := svc.Submit(cachePlan(3))
	if err != nil {
		t.Fatal(err)
	}
	st := waitState(t, svc, id)
	if st.State != StateSucceeded {
		t.Fatalf("cache errors must not fail the job: %s (%s)", st.State, st.Error)
	}
	if len(st.Artifacts) != 3 {
		t.Fatalf("artifacts = %d, want 3", len(st.Artifacts))
	}
}

func TestSegmentCacheDisabledRendersEverything(t *testing.T) {
	be := &fakeMP4Backend{}
	svc, _ := newCacheService(t, be, nil)
	id, _ := svc.Submit(cachePlan(4))
	st := waitState(t, svc, id)
	if st.State != StateSucceeded {
		t.Fatalf("state = %s", st.State)
	}
	got := be.Requests()
	if len(got) != 1 || len(got[0]) != 4 {
		t.Fatalf("without a cache every page must be rendered, got %v", got)
	}
	if st.CachedPages != 0 {
		t.Fatalf("cached = %d, want 0", st.CachedPages)
	}
}

func TestStageAssetsOnlyForRenderedPages(t *testing.T) {
	assetDir := t.TempDir()
	plan := cachePlan(4)
	for i := range plan.Pages {
		name := fmt.Sprintf("p%d.png", i)
		if err := os.MkdirAll(filepath.Join(assetDir, "pages"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(assetDir, "pages", name), []byte("png"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	cache := newCache(t)
	svc1, _ := newCacheService(t, &fakeMP4Backend{}, cache)
	svc1.cfg.AssetDir = assetDir
	id, _ := svc1.Submit(plan)
	waitState(t, svc1, id)

	edited := cachePlan(4)
	edited.Pages[2].Layers[0].Clips[0].DurationMs = 900
	be := &fakeMP4Backend{}
	svc2, _ := newCacheService(t, be, cache)
	svc2.cfg.AssetDir = assetDir
	id2, _ := svc2.Submit(edited)
	st := waitState(t, svc2, id2)
	if st.State != StateSucceeded {
		t.Fatalf("state = %s (%s)", st.State, st.Error)
	}
	// 脏页是 2 与 3（3 的转场画 2 的末帧）；staging 还要带上 2 的转场所依赖的 1。
	// 少复制一张底图的表现是转场那几帧空白，不是报错——所以这条断言盯的是「集合恰好等于」。
	var copied []string
	entries, _ := os.ReadDir(filepath.Join(svc2.cfg.WorkDir, st.ID, "pages"))
	for _, e := range entries {
		copied = append(copied, e.Name())
	}
	sort.Strings(copied)
	if fmt.Sprint(copied) != "[p1.png p2.png p3.png]" {
		t.Fatalf("staged base images = %v, want [p1.png p2.png p3.png]", copied)
	}
}
