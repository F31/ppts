package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestServer 构造带默认桩的最小 handler，参数顺序与 NewHandler 一致。
func newPprofTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(),
		&fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server.Close)
	return server
}

func getStatus(t *testing.T, url string, bearer string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestPprofNotMountedByDefault 守护默认值：诊断端点不得在不显式开启时出现。
// Go 1.22 ServeMux 对未注册 pattern 返回 404（而非 405），据此判断是否真的没挂。
func TestPprofNotMountedByDefault(t *testing.T) {
	t.Setenv("PPTS_PPROF", "")
	t.Setenv("PPTS_METRICS_TOKEN", "tok-value")
	srv := newPprofTestServer(t)
	if code := getStatus(t, srv.URL+"/debug/pprof/", "tok-value"); code != http.StatusNotFound {
		t.Fatalf("pprof index = %d, want 404 (disabled by default)", code)
	}
	if code := getStatus(t, srv.URL+"/debug/pprof/heap", "tok-value"); code != http.StatusNotFound {
		t.Fatalf("pprof heap = %d, want 404", code)
	}
}

// TestPprofRequiresTokenWhenEnabled 守护第二道门：开启了也不能匿名读。
func TestPprofRequiresTokenWhenEnabled(t *testing.T) {
	t.Setenv("PPTS_PPROF", "true")
	t.Setenv("PPTS_METRICS_TOKEN", "")
	srv := newPprofTestServer(t)
	if code := getStatus(t, srv.URL+"/debug/pprof/", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: pprof = %d, want 401", code)
	}
	if code := getStatus(t, srv.URL+"/debug/pprof/cmdline", "wrong-token"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: cmdline = %d, want 401", code)
	}
}

func TestPprofServesWithValidToken(t *testing.T) {
	t.Setenv("PPTS_PPROF", "1")
	t.Setenv("PPTS_METRICS_TOKEN", "s3cr3t-token")
	srv := newPprofTestServer(t)
	if code := getStatus(t, srv.URL+"/debug/pprof/", "s3cr3t-token"); code != http.StatusOK {
		t.Fatalf("with token: pprof = %d, want 200", code)
	}
}
