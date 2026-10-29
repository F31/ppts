package api

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/render"
)

// 逐页编辑器读取的页级上下文（tasks 3.2/3.3）。
//
// 这组用例守的是"编辑器看到的"与"渲染/校验用的"是同一套：
// 元素的顺序与标题/要点分类、页时长、分段序号。

func TestAnimationAnchorsFollowReadingOrder(t *testing.T) {
	ts, _ := newAnimationTestServer(t, true)

	status, payload := doJSON(t, http.MethodGet, ts.URL+animationURL()+"/anchors", nil)
	if status != http.StatusOK {
		t.Fatalf("anchors status = %d, want 200 (%v)", status, payload)
	}
	// 页时长来自 PageContext（测试里给的是 6000），不是常量 0：
	// 面板要靠它算"这一条要点第几秒出现"。
	if got := payload["durationMs"].(float64); got != 6000 {
		t.Fatalf("durationMs = %v, want 6000", got)
	}
	elements, _ := payload["elements"].([]any)
	if len(elements) != 2 {
		t.Fatalf("elements = %v, want 2", elements)
	}
	first := elements[0].(map[string]any)
	second := elements[1].(map[string]any)
	// 阅读顺序：Y 小的算标题，其余算要点。若这里退化成一律 shape，
	// 面板就只能显示"形状 1/2"，用户无法把它和"第 1 条要点"对上。
	if first["kind"] != animation.KindTitle || first["text"] != "第三季度回顾" {
		t.Fatalf("第一个元素 = %v, want title/第三季度回顾", first)
	}
	if second["kind"] != animation.KindBullet {
		t.Fatalf("第二个元素 kind = %v, want bullet", second["kind"])
	}
	if got := second["bulletIndex"].(float64); got != 1 {
		t.Fatalf("bulletIndex = %v, want 1（要点序号从 1 起）", got)
	}
	segments, _ := payload["segments"].([]any)
	if len(segments) != 1 || segments[0].(map[string]any)["segmentId"] != "seg-1" {
		t.Fatalf("segments = %v", segments)
	}
	presets, _ := payload["presets"].([]any)
	if len(presets) < 2 {
		t.Fatalf("presets = %v, want 至少两个预设", presets)
	}
}

func TestClipAnchorTextTruncatesByRune(t *testing.T) {
	short := "第三季度回顾"
	if got := clipAnchorText(short); got != short {
		t.Fatalf("短文本不应被改动: %q", got)
	}
	long := strings.Repeat("增", 60)
	got := clipAnchorText(long)
	// 按 rune 截而不是按字节：按字节切会把一个汉字劈成两个非法字节。
	if len([]rune(got)) != 41 || !strings.HasSuffix(got, "…") {
		t.Fatalf("截断结果 = %q (runes=%d)", got, len([]rune(got)))
	}
}

func TestPreviewCanvasKeepsPageAspect(t *testing.T) {
	// 4:3 的页塞进 16:9 画布会被拉伸，预览里"字歪了"会被读成动效做坏了。
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	canvas := previewCanvas(buf.Bytes())
	if canvas.Width != previewCanvasWidth {
		t.Fatalf("width = %d", canvas.Width)
	}
	if canvas.Height != 720 {
		t.Fatalf("height = %d, want 720（960 的 3:4）", canvas.Height)
	}
	if canvas.Height%2 != 0 {
		t.Fatal("画布高必须是偶数（H.264 yuv420p）")
	}
	// 坏图不能 panic，退到 16:9。
	if c := previewCanvas([]byte("not a png")); c.Width != previewCanvasWidth || c.Height%2 != 0 {
		t.Fatalf("坏图应退到 16:9 且有偶数高: %+v", c)
	}
}

func TestSegmentOffsetsAreRelativeAndClamped(t *testing.T) {
	offsets := segmentOffsetsOf(media.SlideCue{
		SlideID: "slide-2", StartUS: 5_000_000, EndUS: 9_000_000,
		Segments: []media.SegmentCue{
			{SegmentID: "seg-b", StartUS: 5_200_000},
			{SegmentID: "seg-a", StartUS: 4_000_000},
			{SegmentID: ""},
		},
	})
	if offsets["seg-b"] != 200 {
		t.Fatalf("seg-b = %d, want 200", offsets["seg-b"])
	}
	// 早于本页起点的分段归零：负起点会让动效从"页首之前"开始，等于整段消失。
	if offsets["seg-a"] != 0 {
		t.Fatalf("seg-a = %d, want 0（负偏移归零）", offsets["seg-a"])
	}
	if _, dup := offsets[""]; dup {
		t.Fatal("空 segmentId 不应进入偏移表")
	}
}

// ---- 预览 ----

func previewTimeline(t *testing.T, objects objectstore.ObjectStore) (string, string) {
	t.Helper()
	timeline, err := media.BuildTimeline([]media.SlideInput{
		{SlideID: "slide-1", Segments: []media.SegmentInput{{SegmentID: "seg-a", DisplayText: "第一页", AudioKey: "tenant-1/project-1/cache/audio/a.wav", DurationMS: 1000}}},
		{SlideID: anTestSlide, Segments: []media.SegmentInput{
			{SegmentID: "seg-1", DisplayText: "第二页前半", AudioKey: "tenant-1/project-1/cache/audio/b.wav", DurationMS: 1000},
			{SegmentID: "seg-2", DisplayText: "第二页后半", AudioKey: "tenant-1/project-1/cache/audio/c.wav", DurationMS: 1000},
		}},
	}, media.Timing{})
	if err != nil {
		t.Fatal(err)
	}
	// 键必须挂在请求身份所属的租户下：预览端点会用 principal.TenantID 校验，
	// 用别的租户写进去读的时候就是 not found（这本身是租户隔离在起作用）。
	timelineKey := objectstore.ObjectKey{TenantID: db.LocalTenantID, ProjectID: anTestProject, Revision: "narration", AssetType: "timeline", AssetID: "tl", Ext: "json"}
	manifestKey := objectstore.ObjectKey{TenantID: db.LocalTenantID, ProjectID: anTestProject, Revision: "parse", AssetType: "pages", AssetID: "m", Ext: "json"}
	pageKey := objectstore.ObjectKey{TenantID: db.LocalTenantID, ProjectID: anTestProject, Revision: "render", AssetType: "render", AssetID: anTestSlide, Ext: "png"}

	var pngBuf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 96, 72))
	img.Set(1, 1, color.RGBA{G: 255, A: 255})
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}
	// SRT/VTT 键是 loadBundle 的完整性判据（缺一即 "invalid timeline bundle"），
	// 预览不读字幕，但必须给一份能通过校验的 bundle。
	bundle, err := json.Marshal(app.TimelineAsset{Timeline: timeline, SRTKey: "t/s.srt", VTTKey: "t/s.vtt"})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(app.PageManifest{RevisionNo: 1, Pages: []app.PageEntry{
		{SlideID: "slide-1", Index: 0},
		{SlideID: anTestSlide, Index: 1, Key: pageKey.String()},
	}})
	if err != nil {
		t.Fatal(err)
	}
	putAPIObject(t, objects, timelineKey, bundle, "application/json")
	putAPIObject(t, objects, manifestKey, manifest, "application/json")
	putAPIObject(t, objects, pageKey, pngBuf.Bytes(), "image/png")
	return timelineKey.String(), manifestKey.String()
}

// fakeRenderService 是一个最小的渲染服务：只认 /v1/render、任务状态与产物下载。
// 用它断言"提交了几个页面"——预览如果提交了两页，这条用例必须红。
type fakeRenderService struct {
	server   *httptest.Server
	plans    []render.Timeline
	status   string
	artifact []byte
	// cancels 每收到一次取消请求发一个信号：超时后必须真的取消，
	// 否则渲染服务会替一个已被放弃的预览继续占工作槽。
	cancels chan struct{}
}

func newFakeRenderService(t *testing.T) *fakeRenderService {
	t.Helper()
	f := &fakeRenderService{status: string(render.StateSucceeded), artifact: []byte("MP4-BYTES"), cancels: make(chan struct{}, 8)}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/render":
			raw, _ := io.ReadAll(r.Body)
			var plan render.Timeline
			if err := json.Unmarshal(raw, &plan); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"bad plan"}`))
				return
			}
			f.plans = append(f.plans, plan)
			_, _ = w.Write([]byte(`{"jobId":"job-1","state":"queued"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/render/job-1":
			payload, _ := json.Marshal(render.JobStatus{
				ID: "job-1", State: render.JobState(f.status), PlanHash: "hash-1",
				Artifacts: []string{render.SegmentName(0)},
				Progress:  render.Progress{Done: 1, Total: 1},
			})
			_, _ = w.Write(payload)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/artifacts/"+render.SegmentName(0)):
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(f.artifact)
		// 取消是 DELETE /v1/render/{id}（渲染服务的 handleJob）。写成 POST /cancel 的话，
		// 用例会因为"假服务不认这个方法"而永远等不到信号——被测的其实是假服务。
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/render/job-1":
			select {
			case f.cancels <- struct{}{}:
			default:
			}
			_, _ = w.Write([]byte(`{"jobId":"job-1","state":"canceled"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func newPreviewTestServer(t *testing.T, objects objectstore.ObjectStore, jobs JobCreator, f *fakeRenderService, tweak func(*AnimationPreviewer)) *httptest.Server {
	t.Helper()
	store := newAnimationTestStore(t)
	rev := &animation.Revision{
		TenantID: db.LocalTenantID, ProjectID: anTestProject,
		SlideID: anTestSlide, Language: "zh-CN", Status: animation.StatusDraft,
		PresetID: "business-calm", Intensity: 1,
		Tracks: []*animation.Track{{
			ElementID: "shape-1", Kind: animation.KindTitle,
			Clips: []*animation.Clip{{
				ID: "c1", Type: "fadeIn", StartMs: 0, DurationMs: 400,
				Easing: "easeOut", Trigger: animation.TriggerEnter,
			}},
		}},
	}
	if _, err := store.Put(context.Background(), rev, 0); err != nil {
		t.Fatalf("seed spec: %v", err)
	}
	client, err := render.NewClient(render.ClientConfig{BaseURL: f.server.URL, Timeout: 5 * time.Second, Retries: 1})
	if err != nil {
		t.Fatal(err)
	}
	assets, err := app.NewLocalAssetPublisher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	page := animationTestPage()
	resolver := &fakeAnimationResolver{page: page, pc: animation.PageContext{DurationMS: 2000, FPS: 30, Elements: animdefault.PageAnchors(page)}}
	previewer := NewAnimationPreviewer(store, objects, jobs, assets, client, resolver)
	if previewer == nil {
		t.Fatal("previewer 不应为 nil")
	}
	if tweak != nil {
		tweak(previewer)
	}
	mux := http.NewServeMux()
	auth := func(h http.Handler) http.Handler {
		return AuthMiddlewareWithOptions(h, AuthOptions{
			LocalPrincipal: &Principal{TenantID: db.LocalTenantID, UserID: db.LocalUserID},
		})
	}
	previewer.Register(mux, auth)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestAnimationPreviewSubmitsOnePage(t *testing.T) {
	objects := testObjects(t)
	timelineKey, manifestKey := previewTimeline(t, objects)
	jobs := &narrationJobStub{
		job: &pipeline.Job{ID: "job-narr"}, stepRef: timelineKey,
		parseJob: &pipeline.Job{ID: "job-parse"}, pageRef: manifestKey,
	}
	fake := newFakeRenderService(t)
	ts := newPreviewTestServer(t, objects, jobs, fake, nil)

	body, _ := json.Marshal(map[string]any{"timelineKey": timelineKey})
	resp, err := http.Post(ts.URL+animationURL()+"/preview", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preview status = %d, body = %s", resp.StatusCode, raw)
	}
	if resp.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("content-type = %q", resp.Header.Get("Content-Type"))
	}
	if string(raw) != "MP4-BYTES" {
		t.Fatalf("body = %q, want 渲染服务的片段字节", raw)
	}
	// 只提交被请求的那一页：整片预览是另一个决策（Q1），不是"顺手多做一点"。
	if len(fake.plans) != 1 {
		t.Fatalf("提交了 %d 次计划，want 1", len(fake.plans))
	}
	plan := fake.plans[0]
	if len(plan.Pages) != 1 || plan.Pages[0].SlideID != anTestSlide {
		t.Fatalf("计划页 = %+v, want 只有 %s", plan.Pages, anTestSlide)
	}
	if plan.Pages[0].DurationMs != 2000 {
		t.Fatalf("页时长 = %d, want 2000（来自时间轴）", plan.Pages[0].DurationMs)
	}
	if plan.Pages[0].BaseImage == "" {
		t.Fatal("baseImage 为空：页图没有被投递，渲染服务会读不到图")
	}
	// 分段偏移要在预览里生效，否则 withSegment 的动效全落到页首。
	if len(plan.Pages[0].Layers) == 0 {
		t.Fatalf("计划里没有图层: %+v", plan.Pages[0])
	}
}

func TestAnimationPreviewWithoutSpecIs404(t *testing.T) {
	objects := testObjects(t)
	timelineKey, manifestKey := previewTimeline(t, objects)
	fake := newFakeRenderService(t)
	// 用另一页（没有规格）请求：预览没有输入，就不该提交任何计划。
	jobs := &narrationJobStub{job: &pipeline.Job{ID: "job-narr"}, stepRef: timelineKey, parseJob: &pipeline.Job{ID: "job-parse"}, pageRef: manifestKey}
	ts := newPreviewTestServer(t, objects, jobs, fake, nil)

	body, _ := json.Marshal(map[string]any{"timelineKey": timelineKey})
	resp, err := http.Post(ts.URL+"/api/projects/p-anim/slides/slide-1/animation/preview", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	if resp.StatusCode != http.StatusNotFound || payload["code"] != "no_animation" {
		t.Fatalf("status = %d payload = %v, want 404 no_animation", resp.StatusCode, payload)
	}
	if len(fake.plans) != 0 {
		t.Fatal("没有规格时不应提交计划")
	}
}

func TestAnimationPreviewRequiresTimeline(t *testing.T) {
	objects := testObjects(t)
	fake := newFakeRenderService(t)
	ts := newPreviewTestServer(t, objects, &narrationJobStub{job: &pipeline.Job{ID: "job-narr"}}, fake, nil)

	resp, err := http.Post(ts.URL+animationURL()+"/preview", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	if resp.StatusCode != http.StatusBadRequest || payload["code"] != "missing_timeline" {
		t.Fatalf("status = %d payload = %v", resp.StatusCode, payload)
	}
	if len(fake.plans) != 0 {
		t.Fatal("缺 timelineKey 时不应提交计划（页时长与分段偏移都无从确定）")
	}
}

// TestAnimationPreviewTimeoutCancelsJob 守住"超时不是静默空转"：
// 渲染服务一直 running 时必须返回 504 preview_timeout，并且**取消那个任务**——
// 不取消的话用户连点五次预览就在渲染服务里排了五个必然被丢掉的任务。
func TestAnimationPreviewTimeoutCancelsJob(t *testing.T) {
	objects := testObjects(t)
	timelineKey, manifestKey := previewTimeline(t, objects)
	jobs := &narrationJobStub{job: &pipeline.Job{ID: "job-narr"}, stepRef: timelineKey, parseJob: &pipeline.Job{ID: "job-parse"}, pageRef: manifestKey}

	fake := newFakeRenderService(t)
	fake.status = string(render.StateRunning)

	ts := newPreviewTestServer(t, objects, jobs, fake, func(p *AnimationPreviewer) {
		p.WithBudget(300 * time.Millisecond)
	})
	body, _ := json.Marshal(map[string]any{"timelineKey": timelineKey})
	resp, err := http.Post(ts.URL+animationURL()+"/preview", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	if resp.StatusCode != http.StatusGatewayTimeout || payload["code"] != "preview_timeout" {
		t.Fatalf("status = %d payload = %v, want 504 preview_timeout", resp.StatusCode, payload)
	}
	select {
	case <-fake.cancels:
	case <-time.After(2 * time.Second):
		t.Fatal("超时后没有取消渲染任务：它会继续占着渲染服务的工作槽")
	}
}

// TestAnimationResolverUsesTimelineDuration 守 pageDurationMS：
// 它此前恒为 0，导致 ?default=true 永远走 duration_unknown，编辑器一打开就没有起点。
func TestAnimationResolverUsesTimelineDuration(t *testing.T) {
	objects := testObjects(t)
	timelineKey, manifestKey := previewTimeline(t, objects)
	jobs := &narrationJobStub{job: &pipeline.Job{ID: "job-narr"}, stepRef: timelineKey, parseJob: &pipeline.Job{ID: "job-parse"}, pageRef: manifestKey}
	r := newAnimationResolver(nil, objects, nil, jobs)

	if got := r.pageDurationMS(context.Background(), db.LocalTenantID, anTestProject, anTestSlide); got != 2000 {
		t.Fatalf("pageDurationMS = %d, want 2000", got)
	}
	if got := r.pageDurationMS(context.Background(), db.LocalTenantID, anTestProject, "slide-unknown"); got != 0 {
		t.Fatalf("不存在的页 = %d, want 0", got)
	}
	// 没有时间轴（还没配音）时同样是 0：0 表示"未知"，不是"时长真的是零"。
	empty := newAnimationResolver(nil, objects, nil, &narrationJobStub{})
	if got := empty.pageDurationMS(context.Background(), db.LocalTenantID, anTestProject, anTestSlide); got != 0 {
		t.Fatalf("无时间轴 = %d, want 0", got)
	}
}

var _ = project.Page{}

// TestClampPreviewBudget 守住预算与前端兜底超时的先后关系。
//
// 3s 的默认预算是产品决策，但硬件差异让"一页渲不完 3s"成为常态：
// 部署方调大预算时必须被夹在 15s 以内，否则前端 20s 的兜底会先触发，
// 用户看到的是"网络断了"——一个会误导排查方向的错误提示。
func TestClampPreviewBudget(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"未配置", 0, 0},
		{"过短", 200 * time.Millisecond, PreviewBudgetMin},
		{"区间内", 8 * time.Second, 8 * time.Second},
		{"过长", 2 * time.Minute, PreviewBudgetMax},
		{"恰好上限", PreviewBudgetMax, PreviewBudgetMax},
	}
	for _, tc := range cases {
		if got := ClampPreviewBudget(tc.in); got != tc.want {
			t.Fatalf("%s: ClampPreviewBudget(%v) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
	if PreviewBudgetMax >= 90*time.Second {
		t.Fatal("服务端预算上限必须小于前端 90s 兜底，否则超时会被误判为网络故障")
	}
}

// TestSegmentOffsetsOfUnscopesTimelineKeys 守预览侧与导出侧同一条换算：
// 时间轴给 "slide-258:seg-258-4"，规格里存 "seg-258-4"，键对不上就等于
// "第 4 段开始时出现"变成"第 0 帧出现"——预览和导出会一起错，且都不报错。
func TestSegmentOffsetsOfUnscopesTimelineKeys(t *testing.T) {
	slide := media.SlideCue{
		SlideID: "slide-258", StartUS: 10_000_000, EndUS: 20_000_000,
		Segments: []media.SegmentCue{
			{SegmentID: "slide-258:seg-258-1", StartUS: 10_000_000},
			{SegmentID: "slide-258:seg-258-4", StartUS: 16_000_000},
		},
	}
	got := segmentOffsetsOf(slide)
	if got["seg-258-1"] != 0 || got["seg-258-4"] != 6000 {
		t.Fatalf("offsets = %v, want seg-258-1=0 seg-258-4=6000", got)
	}
	if _, exists := got["slide-258:seg-258-4"]; exists {
		t.Fatal("偏移表的键必须是页内 id，不能带页前缀")
	}
}
