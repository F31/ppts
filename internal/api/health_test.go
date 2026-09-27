package api

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

// noPingStore 是不支持探活的对象存：用来验证"没法检查"必须报 unsupported 而非 up。
// 这正是把 Ping 设计成可选接口的全部理由——强迫这类实现返回 nil 等于让它假装健康。
type noPingStore struct{}

func (noPingStore) Put(context.Context, objectstore.ObjectKey, io.Reader, objectstore.ObjectMeta) error {
	return errors.New("unused")
}
func (noPingStore) Get(context.Context, objectstore.ObjectKey) (io.ReadCloser, objectstore.ObjectMeta, error) {
	return nil, objectstore.ObjectMeta{}, errors.New("unused")
}
func (noPingStore) SignedURL(context.Context, objectstore.ObjectKey, objectstore.Operation, time.Duration) (string, error) {
	return "", errors.New("unused")
}
func (noPingStore) Delete(context.Context, objectstore.ObjectKey) error { return errors.New("unused") }
func (noPingStore) ApplyLifecyclePolicy(context.Context, string, objectstore.LifecyclePolicy) error {
	return errors.New("unused")
}

// failPingStore 实现 Ping 但恒失败：模拟后端确实不可达。
type failPingStore struct{ noPingStore }

func (failPingStore) Ping(context.Context) error { return errors.New("backend unreachable") }

// downDB 模拟不可达的数据库。刻意不用真实连接池连"必然失败的地址"——某些网络环境下
// 那会表现为超时而非拒绝，让单元测试挂住。
type downDB struct{}

func (downDB) Ping(context.Context) error { return errors.New("connection refused") }

// TestHealthzLeavesNoTrace 防止探活本身变成存储泄漏源：每轮探活都建临时文件，
// 一旦忘了删，长期运行的实例会在对象根目录堆出大量探针文件。
// 这条同时证明 Ping 真的做了写入往返（只 Stat 的实现不会留下任何可以清理的东西）。
func TestHealthzLeavesNoTrace(t *testing.T) {
	root := t.TempDir()
	store := objectstore.NewLocal(root, nil)
	for i := 0; i < 3; i++ {
		if err := store.Ping(context.Background()); err != nil {
			t.Fatalf("ping %d: %v", i, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("healthz probes left %d file(s) behind: %v", len(entries), names)
	}
}

func TestHealthAllUp(t *testing.T) {
	root := t.TempDir()
	checker := HealthChecker{Objects: objectstore.NewLocal(root, nil), Timeout: 2 * time.Second}
	rep := checker.Check(context.Background())
	if rep.Status != "ok" {
		t.Fatalf("status = %q, checks = %+v", rep.Status, rep.Checks)
	}
	if rep.Checks["object"].State != checkUp {
		t.Fatalf("object = %+v, want up", rep.Checks["object"])
	}
	if _, ok := rep.Checks["database"]; ok {
		t.Fatalf("nil pool must not register a database check, got %+v", rep.Checks)
	}
}

func TestHealthDatabaseDownIsServiceUnavailable(t *testing.T) {
	root := t.TempDir()
	checker := HealthChecker{
		Pool:    downDB{},
		Objects: objectstore.NewLocal(root, nil),
		Timeout: 3 * time.Second,
	}
	rep := checker.Check(context.Background())
	if !rep.Degraded() {
		t.Fatalf("degraded = false, checks = %+v", rep.Checks)
	}
	if rep.Checks["database"].State != checkDown {
		t.Fatalf("database = %+v, want down", rep.Checks["database"])
	}
	// 关键：DB 坏了不能掩盖其它分项的结论。
	if rep.Checks["object"].State != checkUp {
		t.Fatalf("object = %+v, want up (must be checked independently)", rep.Checks["object"])
	}
	if rep.Status != "unhealthy" {
		t.Fatalf("status = %q, want unhealthy", rep.Status)
	}
}

// TestHealthUnsupportedIsNotUp 守护三态区分：无法探活的依赖不得被当作健康。
func TestHealthUnsupportedIsNotUp(t *testing.T) {
	checker := HealthChecker{Objects: noPingStore{}, Timeout: time.Second}
	rep := checker.Check(context.Background())
	if rep.Checks["object"].State != checkUnsupported {
		t.Fatalf("object = %+v, want unsupported", rep.Checks["object"])
	}
	if rep.Degraded() {
		t.Fatal("unsupported must not count as degraded")
	}
	if rep.Status != "ok" {
		t.Fatalf("status = %q, want ok", rep.Status)
	}
}

func TestHealthObjectDown(t *testing.T) {
	checker := HealthChecker{Objects: failPingStore{}, Timeout: time.Second}
	rep := checker.Check(context.Background())
	if rep.Checks["object"].State != checkDown {
		t.Fatalf("object = %+v, want down", rep.Checks["object"])
	}
	if rep.Status != "unhealthy" {
		t.Fatalf("status = %q, want unhealthy", rep.Status)
	}
}

// TestHealthDoesNotLeakErrorDetails 守护：/healthz 免鉴权，响应体只能有分项状态，
// 任何错误原文（连接串、路径、堆栈）都不得出现在零鉴权响应里。
func TestHealthDoesNotLeakErrorDetails(t *testing.T) {
	const ownSentinel = "healthz-no-such-dir-xyz"
	logs := &strings.Builder{}
	checker := HealthChecker{
		// 目录不存在且名字含哨兵串：既能让 Ping 失败，又能核对细节是否泄漏。
		Objects: objectstore.NewLocal(filepath.Join(t.TempDir(), ownSentinel), nil),
		Timeout: time.Second,
		Logger:  log.New(logs, "", 0),
	}
	srv := httptest.NewServer(healthHandler(checker))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(body), ownSentinel) {
		t.Fatalf("response leaks internal detail: %s", body)
	}
	if !strings.Contains(logs.String(), "healthz") {
		t.Fatalf("error detail must go to logger, logs = %q", logs.String())
	}
	// 详细错误应当出现在日志侧，而不是被压平丢弃。
	if !strings.Contains(logs.String(), ownSentinel) {
		t.Fatalf("logger lost the real error: %q", logs.String())
	}
}

// TestLocalPingDetectsReadonlyRoot 验证 Ping 检查的是"能写"而非仅"存在"。
// 存在但只读时 Stat 成功，若不验证写入能力，探活会对一个彻底不可用的后端报 up。
func TestLocalPingDetectsReadonlyRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "objects")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store := objectstore.NewLocal(root, nil)
	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("writable root should be up: %v", err)
	}
	if err := os.Chmod(root, 0o500); err != nil {
		t.Skipf("cannot chmod in this environment: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	// os.Chmod 在部分平台（Windows ACL）对目录写权限不生效：它会成功返回但限制无效。
	// 若实际仍能创建文件，说明这条路径在本平台根本无法构造 —— 必须 Skip 并说明理由，
	// 否则用例会在"看起来绿了"的同时什么也没验证到。
	if probeWritable := func() bool {
		p := filepath.Join(root, ".perm-probe")
		f, err := os.Create(p)
		if err != nil {
			return false
		}
		_ = f.Close()
		_ = os.Remove(p)
		return true
	}(); probeWritable {
		t.Skip("os.Chmod does not restrict directory writes on this platform; readonly path unverifiable")
	}

	if err := store.Ping(context.Background()); err == nil {
		t.Fatal("readonly root must be reported down")
	}
}
