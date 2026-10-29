package render

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// segmentBackend 产出**真实命名**的逐页片段（StubBackend 故意用 .mp4.stub，
// 就是为了让「拿错名字」在测试里立刻暴露；而下载链路要验的正是真实名字）。
type segmentBackend struct {
	skipPages map[int]bool
	body      []byte
}

func (b *segmentBackend) Name() string { return "segments" }

func (b *segmentBackend) Render(ctx context.Context, plan *Timeline, dir string, progress func(done, total int)) error {
	for i := range plan.Pages {
		if b.skipPages[i] {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, SegmentName(i)), b.body, 0o640); err != nil {
			return err
		}
		progress(i+1, len(plan.Pages))
	}
	return nil
}

func newDownloadTestServer(t *testing.T, be Backend) (*Client, string, string) {
	t.Helper()
	svc, workDir := newTestService(t, be)
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	client, err := NewClient(ClientConfig{BaseURL: srv.URL, Retries: 0})
	if err != nil {
		t.Fatal(err)
	}
	id, err := svc.Submit(validPlan())
	if err != nil {
		t.Fatal(err)
	}
	if st := waitState(t, svc, id); st.State != StateSucceeded {
		t.Fatalf("state = %s, err = %s", st.State, st.Error)
	}
	return client, workDir, id
}

func TestDownloadArtifactRoundTrips(t *testing.T) {
	body := []byte("\x00\x00\x00\x18ftypmp42-not-really-a-video-but-bytes-are-bytes")
	client, _, id := newDownloadTestServer(t, &segmentBackend{body: body})

	var got bytes.Buffer
	n, err := client.DownloadArtifact(context.Background(), id, SegmentName(0), &got)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if n != int64(len(body)) || !bytes.Equal(got.Bytes(), body) {
		t.Fatalf("got %d bytes (%q), want %d", n, got.String(), len(body))
	}
	// Content-Length 与实收一致：截断的分段能被拼接器当做好片（ffprobe 只看头），
	// 所以这一条必须在下载时就发现，而不是留到成片黑屏。
	if int64(got.Len()) != n {
		t.Fatalf("写入字节 %d 与声明 %d 不一致", got.Len(), n)
	}
}

// 清单拍完之后才出现的文件不能下载：可见性只有一份，就是 JobStatus.Artifacts。
func TestDownloadArtifactRejectsUnlistedFile(t *testing.T) {
	client, workDir, id := newDownloadTestServer(t, &segmentBackend{})
	// 落终态后才写进去的东西（渲染器残留、清理脚本产物）。
	if err := os.WriteFile(filepath.Join(workDir, id, "late.txt"), []byte("late"), 0o640); err != nil {
		t.Fatal(err)
	}

	var got bytes.Buffer
	_, err := client.DownloadArtifact(context.Background(), id, "late.txt", &got)
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusNotFound {
		t.Fatalf("want 404, got %v", err)
	}
	if got.Len() != 0 {
		t.Fatalf("被拒的产物仍然写出了 %d 字节", got.Len())
	}
}

// 越界路径必须在路径校验处就被拒（400），而不是因为「不在清单里」变成 404——
// 后者会让攻击尝试和打错名字长得一样，无从区分。
func TestDownloadArtifactRejectsEscapingPath(t *testing.T) {
	_, workDir, id := newDownloadTestServer(t, &segmentBackend{})

	// 直接打处理函数：经过 ServeMux 时 ".." 会先被 cleanPath 重定向掉，
	// 那样测到的是 mux 的行为，不是这里要守的边界。
	target := "/v1/render/" + id + "/artifacts/../../etc/passwd"
	rec := httptest.NewRecorder()
	svc := &Service{cfg: ServiceConfig{WorkDir: workDir}, jobs: map[string]*job{
		id: {id: id, state: StateSucceeded, arts: []string{"../../etc/passwd"}},
	}}
	svc.handleJob(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("越界路径 code = %d, want 400", rec.Code)
	}
}

// 服务没起来时，下载失败必须是「不可用」而不是普通错误：上层据此降级而不是重试。
func TestDownloadArtifactUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()

	client, err := NewClient(ClientConfig{BaseURL: base, Retries: 0})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DownloadArtifact(context.Background(), "job", SegmentName(0), &bytes.Buffer{})
	if !IsUnavailable(err) {
		t.Fatalf("want unavailable, got %v", err)
	}
}

// 产物名允许带子目录（未来可能附加封面/音频），逐段转义要保留 "/"。
func TestEscapeArtifactPath(t *testing.T) {
	cases := map[string]string{
		"segment_000.mp4": "segment_000.mp4",
		"plates/a b.png":  "plates/a%20b.png",
		"a/b/c&d.mp4":     "a/b/c&d.mp4",
	}
	for in, want := range cases {
		if got := escapeArtifactPath(in); got != want {
			t.Errorf("escapeArtifactPath(%q) = %q, want %q", in, got, want)
		}
	}
	if strings.Contains(escapeArtifactPath("a/b"), "%2F") {
		t.Fatal("分隔符被转义：服务端会把子目录压成一个文件名")
	}
}
