package render

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// tinyFont 造一个只含 cmap(format 12) 的最小 SFNT，用来充当「子集产物」。
// 本包的校验只读 cmap，不需要 glyf/CFF/head 这些表。
func tinyFont(cps ...uint32) []byte {
	groups := make([][3]uint32, 0, len(cps))
	for i, cp := range cps {
		groups = append(groups, [3]uint32{cp, cp, uint32(i + 1)})
	}
	f12 := make([]byte, 16+len(groups)*12)
	binary.BigEndian.PutUint16(f12[0:], 12)
	binary.BigEndian.PutUint32(f12[4:], uint32(len(f12)))
	binary.BigEndian.PutUint32(f12[12:], uint32(len(groups)))
	for i, g := range groups {
		at := 16 + i*12
		binary.BigEndian.PutUint32(f12[at:], g[0])
		binary.BigEndian.PutUint32(f12[at+4:], g[1])
		binary.BigEndian.PutUint32(f12[at+8:], g[2])
	}

	cmap := make([]byte, 12)
	binary.BigEndian.PutUint16(cmap[2:], 1)
	binary.BigEndian.PutUint16(cmap[4:], 3)
	binary.BigEndian.PutUint16(cmap[6:], 10)
	binary.BigEndian.PutUint32(cmap[8:], 12)
	cmap = append(cmap, f12...)

	buf := make([]byte, 12+16+len(cmap))
	binary.BigEndian.PutUint32(buf[0:], 0x00010000)
	binary.BigEndian.PutUint16(buf[4:], 1)
	copy(buf[12:16], "cmap")
	binary.BigEndian.PutUint32(buf[20:], 28)
	binary.BigEndian.PutUint32(buf[24:], uint32(len(cmap)))
	copy(buf[28:], cmap)
	return buf
}

type fakeRunner struct {
	mu       sync.Mutex
	calls    int
	charsets []string
	// product 非 nil 时写到 outPath；stdout/err 原样返回。
	product []byte
	stdout  []byte
	err     error
	delay   time.Duration
}

func (f *fakeRunner) run(ctx context.Context, charsetPath, outPath string) ([]byte, error) {
	b, _ := os.ReadFile(charsetPath)
	f.mu.Lock()
	f.calls++
	f.charsets = append(f.charsets, string(b))
	product, stdout, err, delay := f.product, f.stdout, f.err, f.delay
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			// 真实实现里子进程会被杀掉；这里模拟同样的时序。
			return nil, ctx.Err()
		}
	}
	if product != nil {
		if werr := os.WriteFile(outPath, product, 0o600); werr != nil {
			return nil, werr
		}
	}
	return stdout, err
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeRunner) lastCharset() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.charsets) == 0 {
		return ""
	}
	return f.charsets[len(f.charsets)-1]
}

func TestFontSubsetNormalizesAndCaches(t *testing.T) {
	r := &fakeRunner{
		product: tinyFont('B', 'A', 0x4E00),
		stdout:  []byte(`{"kept":[65,66,19968],"missing":[131072]}`),
	}
	s := newFontSubsetterWithRunner(FontSubsetConfig{Version: "v1"}, r)

	// 乱序 + 重复：字集是集合，不该影响缓存键。
	first, err := s.Subset(context.Background(), []rune{'B', 0x4E00, 'A', 'B'})
	if err != nil {
		t.Fatalf("Subset: %v", err)
	}
	// 每次一行十六进制，升序：A B 一
	if want := "41\n42\n4E00\n"; r.lastCharset() != want {
		t.Fatalf("charset = %q, want %q", r.lastCharset(), want)
	}
	if first.Codepoints != 3 {
		t.Fatalf("Codepoints = %d, want 3", first.Codepoints)
	}
	if len(first.Missing) != 1 || first.Missing[0] != 0x20000 {
		t.Fatalf("Missing = %U", first.Missing)
	}

	// 同一集合换个顺序第二次：必须命中缓存，不再起进程。
	if _, err := s.Subset(context.Background(), []rune{'A', 'B', 0x4E00}); err != nil {
		t.Fatalf("Subset(second): %v", err)
	}
	if n := r.callCount(); n != 1 {
		t.Fatalf("runner 被调 %d 次，want 1（缓存未命中）", n)
	}

	// 不同字集必须重新裁。
	if _, err := s.Subset(context.Background(), []rune{'A', 'C'}); err != nil {
		t.Fatalf("Subset(third): %v", err)
	}
	if n := r.callCount(); n != 2 {
		t.Fatalf("runner 被调 %d 次，want 2", n)
	}
}

// 这条是「不采信脚本自报」的证明：脚本说保留了三个字，产物里只有两个。
// 如果实现信任 stdout 的 kept 列表，这里会静默通过，然后成品包里出现方框。
func TestFontSubsetRejectsIncompleteProduct(t *testing.T) {
	r := &fakeRunner{
		product: tinyFont(0x4E00), // 产物只有「一」
		stdout:  []byte(`{"kept":[65,66,19968]}`),
	}
	s := newFontSubsetterWithRunner(FontSubsetConfig{Version: "v1"}, r)

	_, err := s.Subset(context.Background(), []rune{'A', 'B', 0x4E00})
	if err == nil {
		t.Fatal("产物缺字却通过了")
	}
	if !strings.Contains(err.Error(), "缺 2 个已声明码位") {
		t.Fatalf("错误信息没点出缺字数量：%v", err)
	}
}

func TestFontSubsetTimeout(t *testing.T) {
	r := &fakeRunner{delay: 500 * time.Millisecond, product: tinyFont('A')}
	s := newFontSubsetterWithRunner(FontSubsetConfig{Version: "v1", Timeout: 30 * time.Millisecond}, r)

	start := time.Now()
	_, err := s.Subset(context.Background(), []rune{'A'})
	if err == nil {
		t.Fatal("期望超时错误")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("错误信息不是超时：%v", err)
	}
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("超时没有被及时打断：%s", elapsed)
	}
}

func TestFontSubsetRejectsTooManyCodepoints(t *testing.T) {
	s := newFontSubsetterWithRunner(FontSubsetConfig{Version: "v1", MaxCodepoints: 3},
		&fakeRunner{product: tinyFont('A')})
	cps := []rune{'A', 'B', 'C', 'D'}
	if _, err := s.Subset(context.Background(), cps); err == nil {
		t.Fatal("超过上限却通过了")
	}
}

func TestFontSubsetEmptyRequest(t *testing.T) {
	s := newFontSubsetterWithRunner(FontSubsetConfig{Version: "v1"}, &fakeRunner{})
	if _, err := s.Subset(context.Background(), nil); err == nil {
		t.Fatal("空字集却通过了")
	}
}

func TestFontSubsetRunnerErrorIsWrapped(t *testing.T) {
	r := &fakeRunner{err: os.ErrNotExist}
	s := newFontSubsetterWithRunner(FontSubsetConfig{Version: "v1"}, r)
	_, err := s.Subset(context.Background(), []rune{'A'})
	if err == nil || !strings.Contains(err.Error(), "font subset") {
		t.Fatalf("错误没带上上下文：%v", err)
	}
}

func TestFontSubsetHandler(t *testing.T) {
	product := tinyFont(0x4E00, 'A')
	r := &fakeRunner{product: product, stdout: []byte(`{"kept":[65,19968],"missing":[131072]}`)}
	svc := &Service{cfg: ServiceConfig{
		FontSubsetter: newFontSubsetterWithRunner(FontSubsetConfig{Version: "v9"}, r),
	}}
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	body := `{"codepoints":[19968,65]}`
	resp, err := http.Post(srv.URL+"/v1/fonts/subset", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out fontSubsetResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	if len(got) != len(product) {
		t.Fatalf("字体字节被截断：%d != %d", len(got), len(product))
	}
	if out.Codepoints != 2 || out.BaseVersion != "v9" || out.Format != "otf" {
		t.Fatalf("响应字段不对：%+v", out)
	}
	if len(out.Missing) != 1 || out.Missing[0] != 0x20000 {
		t.Fatalf("Missing = %v", out.Missing)
	}

	// 方法不对
	mresp, err := http.Get(srv.URL + "/v1/fonts/subset")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	mresp.Body.Close()
	if mresp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", mresp.StatusCode)
	}

	// 非法码位（代理区）与空数组
	for _, bad := range []string{`{"codepoints":[]}`, `{"codepoints":[55296]}`} {
		r2, err := http.Post(srv.URL+"/v1/fonts/subset", "application/json", strings.NewReader(bad))
		if err != nil {
			t.Fatalf("POST bad: %v", err)
		}
		r2.Body.Close()
		if r2.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", bad, r2.StatusCode)
		}
	}
}

// 未配置子集器时端点必须不存在：调用方据此降级，而不是把 503 当临时故障反复重试。
func TestFontSubsetHandlerAbsentWhenUnconfigured(t *testing.T) {
	svc := &Service{cfg: ServiceConfig{}}
	rec := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/fonts/subset",
		strings.NewReader(`{"codepoints":[65]}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
