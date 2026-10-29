package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/render"
)

// animationBackend 是最小的渲染后端：按页序写 SegmentName(i)，可以跳过某几页或整体失败。
// 用真后端会拖进 Chrome；而这里要验的是编排（提交/等待/下载/缺页），不是光栅化。
type animationBackend struct {
	skip map[int]bool
	fail error
	// body 是每页片段的内容。用可辨识的字节，验证下载到的是**这一页**而不是上一页。
	body func(i int) []byte
	// degraded 模拟渲染服务把某几页换成了静态画面：片段仍在，但该页没动画。
	degraded []render.PageDegradation
}

func (b *animationBackend) Name() string { return "animation-test" }

func (b *animationBackend) Render(ctx context.Context, plan *render.Timeline, dir string,
	progress func(done, total int)) (render.RenderReport, error) {
	if b.fail != nil {
		return render.RenderReport{}, b.fail
	}
	for i := range plan.Pages {
		if b.skip[i] {
			continue
		}
		body := []byte("segment")
		if b.body != nil {
			body = b.body(i)
		}
		if err := os.WriteFile(filepath.Join(dir, render.SegmentName(i)), body, 0o640); err != nil {
			return render.RenderReport{}, err
		}
		progress(i+1, len(plan.Pages))
	}
	return render.RenderReport{Degraded: b.degraded}, nil
}

// newAnimationHandler 起一个真实的渲染服务（HTTP 契约 + 客户端），返回挂好动画通道的导出 handler。
func newAnimationHandler(t *testing.T, be render.Backend) (*ExportHandler, *httptest.Server) {
	t.Helper()
	svc, err := render.NewService(render.ServiceConfig{
		Backend: be,
		WorkDir: t.TempDir(),
		Version: "test-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	client, err := render.NewClient(render.ClientConfig{BaseURL: srv.URL, Retries: 0, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return NewExportHandler(nil, nil, nil, nil).WithAnimation(client, 20*time.Second), srv
}

func testAnimationRequest(pages int) AnimationRequest {
	req := AnimationRequest{Width: 1920, Height: 1080, FPS: 30}
	for i := 0; i < pages; i++ {
		req.Pages = append(req.Pages, AnimationPage{
			SlideID:    "slide-" + string(rune('a'+i)),
			DurationMs: 5000,
			BaseImage:  "pages/p" + string(rune('0'+i)) + ".png",
			Elements: []render.Element{{
				ID: "title", Kind: "title",
				Bounds: &render.EMUBox{X: 1000000, Y: 800000, Width: 8000000, Height: 1200000},
			}},
			Revision: &animation.Revision{
				Intensity: 1, Status: animation.StatusApproved,
				Tracks: []*animation.Track{{
					ElementID: "title", Kind: animation.KindTitle,
					Clips: []*animation.Clip{{
						ID: "in", Type: "fadeIn", StartMs: 0, DurationMs: 400,
						Easing: "easeOut", Trigger: animation.TriggerEnter,
					}},
				}},
			},
		})
	}
	return req
}

func TestRenderAnimationDownloadsEverySegment(t *testing.T) {
	handler, _ := newAnimationHandler(t, &animationBackend{body: func(i int) []byte {
		return bytes.Repeat([]byte{byte('A' + i)}, 1024+i)
	}})
	workDir := t.TempDir()

	got, err := handler.RenderAnimation(context.Background(), testAnimationRequest(3), workDir)
	if err != nil {
		t.Fatalf("RenderAnimation: %v", err)
	}
	if len(got.Segments) != 3 || len(got.Missing) != 0 {
		t.Fatalf("segments=%v missing=%v", got.Segments, got.Missing)
	}
	if got.PlanHash == "" {
		t.Fatal("planHash 为空：服务端没有回传，排查「同输入两份产物」时会失去线索")
	}
	for i, path := range got.Segments {
		// 顺序是语义：第 i 段必须对应第 i 页，错位会让整片内容顺序打乱。
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("page %d: %v", i, err)
		}
		want := bytes.Repeat([]byte{byte('A' + i)}, 1024+i)
		if !bytes.Equal(data, want) {
			t.Fatalf("page %d 内容不符：%d 字节，期望 %d 字节", i, len(data), len(want))
		}
	}
	// 无 PageContext 的页会得到 geometry_inferred 诊断（未给 slide EMU），
	// 这里只确认诊断被透传，不断言条数——诊断数量随编译器演进。
	if got.Diagnostics == nil {
		t.Fatal("诊断未透传：调用方无从知道哪些页的几何是推断的")
	}
}

// TestRenderAnimationTreatsServerDegradedPageAsPresent 固定 tasks 3.7 的上游语义：
// 渲染器把某页换成静态画面后仍然产出片段，编排层不能把它当成「缺页」再补一次——
// 那会把服务端刚出的静态段丢掉，换成另一份静态图，两边的画面还不一定一致。
func TestRenderAnimationTreatsServerDegradedPageAsPresent(t *testing.T) {
	handler, _ := newAnimationHandler(t, &animationBackend{
		degraded: []render.PageDegradation{{Page: 1, Code: render.DegradationStill, Message: "gpu process crashed"}},
	})

	got, err := handler.RenderAnimation(context.Background(), testAnimationRequest(3), t.TempDir())
	if err != nil {
		t.Fatalf("单页降级不该让编排失败: %v", err)
	}
	if len(got.Missing) != 0 {
		t.Fatalf("服务端已出的静态段被当成缺页: %v", got.Missing)
	}
	if got.Segments[1] == "" {
		t.Fatal("降级页的片段没有下载：用户会拿到一个空洞")
	}
}

// TestRenderAnimationLogsServerDegradedPage 固定留痕：降级只表现为「这页没动画」，
// 不留日志的话，用户反馈「这页不劲」时没有任何线索指向渲染器内部失败。
func TestRenderAnimationLogsServerDegradedPage(t *testing.T) {
	var buf bytes.Buffer
	handler, _ := newAnimationHandler(t, &animationBackend{
		degraded: []render.PageDegradation{{Page: 1, Code: render.DegradationStill, Message: "gpu process crashed"}},
	})
	handler.logger = slog.New(slog.NewTextHandler(&buf, nil))

	if _, err := handler.RenderAnimation(context.Background(), testAnimationRequest(3), t.TempDir()); err != nil {
		t.Fatalf("RenderAnimation: %v", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "degraded to still") || !strings.Contains(logged, "page=1") {
		t.Fatalf("降级页没有留痕: %s", logged)
	}
	if !strings.Contains(logged, "gpu process crashed") {
		t.Fatalf("日志丢了渲染器给的原因: %s", logged)
	}
}

// TestRenderAnimationKeepsGoingWhenOneSegmentIsMissing 固定 ADR-23：服务端成功但缺某页时，
// 编排层换静态段继续，不整片失败。
func TestRenderAnimationKeepsGoingWhenOneSegmentIsMissing(t *testing.T) {
	handler, _ := newAnimationHandler(t, &animationBackend{skip: map[int]bool{1: true}})

	got, err := handler.RenderAnimation(context.Background(), testAnimationRequest(3), t.TempDir())
	if err != nil {
		t.Fatalf("缺一页不该让整片失败，实际：%v", err)
	}
	if len(got.Missing) != 1 || got.Missing[0] != 1 {
		t.Fatalf("missing = %v, want [1]", got.Missing)
	}
	if got.Segments[1] != "" {
		t.Fatalf("缺页不该有路径：%q", got.Segments[1])
	}
	if got.Segments[0] == "" || got.Segments[2] == "" {
		t.Fatal("其余页应当正常拿到片段")
	}
}

func TestRenderAnimationReportsRenderFailure(t *testing.T) {
	handler, _ := newAnimationHandler(t, &animationBackend{fail: errors.New("chrome crashed")})

	_, err := handler.RenderAnimation(context.Background(), testAnimationRequest(2), t.TempDir())
	if err == nil {
		t.Fatal("渲染失败必须报错，否则上层会拿一份空片段列表去拼接")
	}
	// 渲染器内部失败不是「服务不可用」：它是可重试的业务失败，不该被当成降级信号。
	if render.IsUnavailable(err) {
		t.Fatalf("内部失败被误判为不可用：%v", err)
	}
}

func TestRenderAnimationReportsUnavailable(t *testing.T) {
	handler, srv := newAnimationHandler(t, &animationBackend{})
	srv.Close() // 服务没起来

	_, err := handler.RenderAnimation(context.Background(), testAnimationRequest(1), t.TempDir())
	if !render.IsUnavailable(err) {
		t.Fatalf("want unavailable, got %v", err)
	}
}

// 4:3 的页按 16:9 输出：元素框会错位，必须明确拒绝（由上层降级成静态版），
// 而不是静默出一版歪的动画。
func TestRenderAnimationRejectsAspectMismatch(t *testing.T) {
	handler, _ := newAnimationHandler(t, &animationBackend{})
	req := testAnimationRequest(1)
	req.SlideWidthEMU, req.SlideHeightEMU = 9144000, 6858000 // 4:3

	_, err := handler.RenderAnimation(context.Background(), req, t.TempDir())
	if !errors.Is(err, render.ErrAspectMismatch) {
		t.Fatalf("want ErrAspectMismatch, got %v", err)
	}
}

func TestRenderAnimationWithoutRenderer(t *testing.T) {
	handler := NewExportHandler(nil, nil, nil, nil)
	_, err := handler.RenderAnimation(context.Background(), testAnimationRequest(1), t.TempDir())
	if !render.IsUnavailable(err) {
		t.Fatalf("未配置渲染器应报不可用（上层据此走静态），got %v", err)
	}
}
