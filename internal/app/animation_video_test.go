package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/render"
)

// mp4AnimationBackend 产**真的** mp4 片段：拼接前要逐段 ffprobe，假字节在第一道探测就失败，
// 那样测到的只是「探测会报错」，不是降级逻辑。
type mp4AnimationBackend struct {
	enc *media.MP4Encoder
	// skip 是「服务端没给这一段」的页。
	skip map[int]bool
	// fail 让整次渲染失败（等价于后端崩了）。
	fail error
	// segmentMS 覆盖片段实际时长，用来制造与计划不符的漂移。
	segmentMS int64
}

func (b *mp4AnimationBackend) Name() string { return "mp4-animation-test" }

func (b *mp4AnimationBackend) Render(ctx context.Context, plan *render.Timeline, dir string,
	progress func(done, total int)) error {
	if b.fail != nil {
		return b.fail
	}
	for i, p := range plan.Pages {
		if b.skip[i] {
			continue
		}
		ms := p.DurationMs
		if b.segmentMS > 0 {
			ms = b.segmentMS
		}
		if _, err := b.enc.EncodeStillSegment(ctx, pngForExportTest(), ms,
			filepath.Join(dir, render.SegmentName(i)), 20, 320, 180); err != nil {
			return err
		}
		progress(i+1, len(plan.Pages))
	}
	return nil
}

// countedHandler 数请求数：用来证明「开关关闭时一次都没有探测渲染服务」。
type countedHandler struct {
	inner http.Handler
	n     *int32
}

func (c countedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(c.n, 1)
	c.inner.ServeHTTP(w, r)
}

// newAnimationVideoHandler 起一个真实渲染服务（HTTP 契约 + 客户端），并统计请求数。
func newAnimationVideoHandler(t *testing.T, be render.Backend) (*ExportHandler, *httptest.Server, *int32) {
	t.Helper()
	enc, err := media.NewMP4Encoder()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	svc, err := render.NewService(render.ServiceConfig{Backend: be, WorkDir: t.TempDir(), Version: "test-1"})
	if err != nil {
		t.Fatal(err)
	}
	var hits int32
	srv := httptest.NewServer(countedHandler{inner: svc.Handler(), n: &hits})
	t.Cleanup(srv.Close)
	client, err := render.NewClient(render.ClientConfig{BaseURL: srv.URL, Retries: 0, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewExportHandler(nil, nil, nil, enc).WithAnimation(client, 60*time.Second)
	return handler, srv, &hits
}

// animationVideoOptions 的计划时长必须与 testAnimationRequest 的页时长一致：
// 不一致本身就会被判成漂移，测「单页缺失」时会变成测「漂移修复」。
func animationVideoOptions(pages int, pageMS int64) AnimationVideoOptions {
	opts := AnimationVideoOptions{FPS: 20, Width: 320, Height: 180}
	for i := 0; i < pages; i++ {
		opts.PagePNGs = append(opts.PagePNGs, pngForExportTest())
		opts.PageDurationsMS = append(opts.PageDurationsMS, pageMS)
	}
	return opts
}

// 单页没拿到片段 → 该页换成静态段，成片仍然完整。
// 断言 Degraded 而不是「文件存在」：跳过该页也能得到一个文件，只是它比时间轴短。
func TestTryAnimationDegradesSinglePageToStill(t *testing.T) {
	handler, _, hits := newAnimationVideoHandler(t, &mp4AnimationBackend{
		enc: mustEncoder(t), skip: map[int]bool{1: true},
	})

	av := handler.tryAnimation(context.Background(), testAnimationRequest(2), animationVideoOptions(2, 5000))
	if av == nil {
		t.Fatal("单页失败不该让整片退回静态：其余页的动画是有效的")
	}
	defer av.File.Close()
	if len(av.Degraded) != 1 || av.Degraded[0] != 1 {
		t.Fatalf("degraded = %v, want [1]", av.Degraded)
	}
	if len(av.Drifted) != 0 {
		t.Fatalf("缺失页被记成了漂移页：%v", av.Drifted)
	}
	if atomic.LoadInt32(hits) == 0 {
		t.Fatal("没有向渲染服务发起请求，这不该走到这里")
	}
}

// 时长漂移 → 换成静态段重拼一次，而不是接受音画错位。
func TestTryAnimationRepairsDurationDrift(t *testing.T) {
	handler, _, _ := newAnimationVideoHandler(t, &mp4AnimationBackend{
		enc: mustEncoder(t), segmentMS: 2500, // 计划 5000ms，实际 2500ms
	})

	av := handler.tryAnimation(context.Background(), testAnimationRequest(2), animationVideoOptions(2, 5000))
	if av == nil {
		t.Fatal("漂移应当可以靠静态段修复，不该整片退回")
	}
	defer av.File.Close()
	if len(av.Drifted) != 2 || len(av.Degraded) != 2 {
		t.Fatalf("degraded = %v, drifted = %v，want 两页都因漂移换静态段", av.Degraded, av.Drifted)
	}
}

// 三种「拿不到动画」都必须返回 nil —— 返回 error 会让上层把增强层的故障当业务失败。
func TestTryAnimationReturnsNilInsteadOfError(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T) *ExportHandler
	}{
		{"服务不可达", func(t *testing.T) *ExportHandler {
			handler, srv, _ := newAnimationVideoHandler(t, &mp4AnimationBackend{enc: mustEncoder(t)})
			srv.Close()
			return handler
		}},
		{"渲染整片失败", func(t *testing.T) *ExportHandler {
			handler, _, _ := newAnimationVideoHandler(t, &mp4AnimationBackend{enc: mustEncoder(t), fail: errors.New("chrome crashed")})
			return handler
		}},
		{"开关关闭", func(t *testing.T) *ExportHandler {
			// 没有渲染客户端 = 功能没开：连临时目录都不该建。
			return NewExportHandler(nil, nil, nil, mustEncoder(t))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if av := tc.run(t).tryAnimation(context.Background(), testAnimationRequest(2), animationVideoOptions(2, 5000)); av != nil {
				av.File.Close()
				t.Fatal("拿不到动画时必须返回 nil，交回静态路径")
			}
		})
	}
}

// 用户可见的契约：四种故障下**都得得到一张 mp4**，且不该因为动画失败而判导出失败。
func TestExportProducesVideoRegardlessOfAnimation(t *testing.T) {
	cases := []struct {
		name       string
		withRender bool
		skip       map[int]bool
		fail       error
		unreach    bool
	}{
		{name: "动画可用", withRender: true},
		{name: "单页失败", withRender: true, skip: map[int]bool{1: true}},
		{name: "全部失败", withRender: true, fail: errors.New("chrome crashed")},
		{name: "服务不可达", withRender: true, unreach: true},
		{name: "开关关闭", withRender: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc := mustEncoder(t)
			objects := objectstore.NewLocal(t.TempDir(), nil)
			bundleKey, pageKeys := seedAnimationTimeline(t, objects, 2, 1000)

			// 服务端始终起起来并计数：这样「开关关闭」那一档才能证明**一次请求都没发**。
			h, srv, hits := newAnimationVideoHandler(t, &mp4AnimationBackend{enc: enc, skip: tc.skip, fail: tc.fail})
			renderer := h.renderer
			if tc.unreach {
				srv.Close()
			}
			if !tc.withRender {
				renderer = nil
			}

			artifacts := &artifactStoreStub{}
			handler := NewExportHandler(artifacts, &stepRecorder{}, objects, enc).
				WithAnimation(renderer, 60*time.Second).
				WithAnimationPages(func(_ context.Context, in AnimationPageInput) (AnimationRequest, error) {
					return animationRequestFor(in), nil
				})
			err := handler.Handle(context.Background(), exportJob(t, ExportSnapshot{
				Format: artifact.FormatMP4, TimelineKey: bundleKey, PagePNGKeys: pageKeys,
				Width: 320, Height: 180, FPS: 20,
				Animation: &AnimationOptions{Specs: []AnimationSpecRef{
					{SlideID: "slide-1", Language: "zh", Revision: 1},
					{SlideID: "slide-2", Language: "zh", Revision: 1},
				}},
			}))
			if err != nil {
				t.Fatalf("动画出问题时导出仍必须成功，实际：%v", err)
			}
			if artifacts.created == nil || artifacts.created.Format != artifact.FormatMP4 || artifacts.created.SizeBytes <= 0 {
				t.Fatalf("artifact = %+v", artifacts.created)
			}
			// 产物必须是 mp4：只看 size>0 的话，一份空壳也会过。
			key, err := objectstore.Parse(artifacts.created.ObjectKey)
			if err != nil {
				t.Fatal(err)
			}
			r, _, err := objects.Get(context.Background(), key)
			if err != nil {
				t.Fatalf("artifact object: %v", err)
			}
			defer r.Close()
			head := make([]byte, 12)
			if _, err := r.Read(head); err != nil {
				t.Fatalf("read artifact: %v", err)
			}
			if string(head[4:8]) != "ftyp" {
				t.Fatalf("产物不是 mp4：%q", head)
			}
			// 开关关闭时一次请求都不该发：否则「关闭」只是「跑了再丢掉」。
			if !tc.withRender && atomic.LoadInt32(hits) != 0 {
				t.Fatalf("开关关闭仍探测了渲染服务 %d 次", atomic.LoadInt32(hits))
			}
		})
	}
}

// seedAnimationTimeline 造一条 pages 页、每页 pageMS 的时间轴，并写入音频/字幕/页图。
func seedAnimationTimeline(t *testing.T, objects objectstore.ObjectStore, pages, pageMS int) (string, []string) {
	t.Helper()
	ms := int64(pageMS)
	t.Helper()
	slides := make([]media.SlideInput, 0, pages)
	for i := 0; i < pages; i++ {
		slides = append(slides, media.SlideInput{
			SlideID: fmt.Sprintf("slide-%d", i+1),
			Segments: []media.SegmentInput{{
				SegmentID: fmt.Sprintf("seg-%d", i+1), DisplayText: "字幕",
				AudioKey:   fmt.Sprintf("tenant-1/project-1/cache/audio/a%d.wav", i+1),
				DurationMS: ms,
			}},
		})
	}
	timeline, err := media.BuildTimeline(slides, media.Timing{})
	if err != nil {
		t.Fatal(err)
	}
	for _, slide := range timeline.Slides {
		for _, seg := range slide.Segments {
			key, err := objectstore.Parse(seg.AudioKey)
			if err != nil {
				t.Fatal(err)
			}
			// 每段音频必须与**该段**的计划时长一致：WriteTimelineWAV 会逐段校验，
			// 长度不符直接报错（这是它的既有契约，不是本测试的偏好）。
			putObject(t, objects, key, pcmWAVForExportTest(8000, 8000*pageMS/1000), "audio/wav")
		}
	}
	srtKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "narration-job", AssetType: "subtitle", AssetID: "sub", Ext: "srt"}
	vttKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "narration-job", AssetType: "subtitle", AssetID: "sub", Ext: "vtt"}
	bundleKey := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "narration-job", AssetType: "timeline", AssetID: "tl", Ext: "json"}
	putObject(t, objects, srtKey, []byte("1\r\n00:00:00,000 --> 00:00:01,000\r\n字幕\r\n"), "application/x-subrip")
	putObject(t, objects, vttKey, []byte("WEBVTT\n\n00:00:00.000 --> 00:00:01.000\n字幕\n"), "text/vtt")
	pageKeys := make([]string, 0, pages)
	for i := 0; i < pages; i++ {
		key := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "render", AssetType: "render", AssetID: fmt.Sprintf("slide-%d", i+1), Ext: "png"}
		putObject(t, objects, key, pngForExportTest(), "image/png")
		pageKeys = append(pageKeys, key.String())
	}
	bundle := TimelineAsset{Timeline: timeline, SRTKey: srtKey.String(), VTTKey: vttKey.String()}
	b, _ := json.Marshal(bundle)
	putObject(t, objects, bundleKey, b, "application/json")
	return bundleKey.String(), pageKeys
}

// animationRequestFor 把导出材料转成动画请求。底图相对路径在生产里要靠投递机制（tasks 2.0），
// 这里只验接线，所以直接按页序编一个路径。
func animationRequestFor(in AnimationPageInput) AnimationRequest {
	req := AnimationRequest{Width: 320, Height: 180, FPS: 20}
	for i, slide := range in.Slides {
		req.Pages = append(req.Pages, AnimationPage{
			SlideID: slide.SlideID, DurationMs: (slide.EndUS - slide.StartUS) / 1000,
			BaseImage: fmt.Sprintf("pages/%d.png", i),
			Elements: []render.Element{{
				ID: "title", Kind: "title",
				Bounds: &render.EMUBox{X: 100000, Y: 100000, Width: 800000, Height: 400000},
			}},
		})
	}
	return req
}

func mustEncoder(t *testing.T) *media.MP4Encoder {
	t.Helper()
	enc, err := media.NewMP4Encoder()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	return enc
}
