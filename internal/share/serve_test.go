package share

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func makeZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func TestSafeEntryName(t *testing.T) {
	ok := []string{
		"index.html", "manifest.js", "app.css",
		"slides/001.png", "slides/003-layer-00.png",
		"audio/seg-1.mp3", "字幕.srt",
	}
	for _, name := range ok {
		if !SafeEntryName(name) {
			t.Errorf("SafeEntryName(%q) = false, want true", name)
		}
	}
	// 每一条都对应一类真实攻击或一类会静默出错的输入：
	bad := []string{
		"",                   // 空路径
		"/etc/passwd",        // 绝对路径
		"../manifest.json",   // 上行穿越
		"slides/../../x.png", // 多级穿越
		"a/../b",             // Clean 会"修好"它——所以必须拒绝而不是清理
		"./index.html",       // 当前目录前缀
		"a//b",               // 空段
		"slides/",            // 目录（会让人以为能列举）
		`slides\001.png`,     // 反斜杠（Windows 上等价于分隔符）
		"..",                 // 纯上行
		"a\x00b",             // 控制字符
		"a\nb",               // 换行（日志注入）
		strings.Repeat("a", 513),
	}
	for _, name := range bad {
		if SafeEntryName(name) {
			t.Errorf("SafeEntryName(%q) = true, want false", name)
		}
	}
}

func TestReadEntryServesSingleEntry(t *testing.T) {
	ctx := context.Background()
	payload := makeZip(t, map[string]string{
		"index.html":     "<html>hi</html>",
		"slides/001.png": "PNGDATA",
	})
	src := NewBundleSource(func(context.Context, string) ([]byte, error) { return payload, nil })

	entry, err := src.ReadEntry(ctx, "k/1.zip", "hash-1", "slides/001.png")
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}
	if string(entry.Data) != "PNGDATA" {
		t.Fatalf("data = %q", entry.Data)
	}

	if _, err := src.ReadEntry(ctx, "k/1.zip", "hash-1", "slides/999.png"); !errors.Is(err, ErrBundleEntryNotFound) {
		t.Fatalf("missing entry err = %v", err)
	}
	// 越界路径与"不存在"同码：区分它们等于告诉攻击者哪些路径被处理过。
	if _, err := src.ReadEntry(ctx, "k/1.zip", "hash-1", "../x"); !errors.Is(err, ErrBundleEntryNotFound) {
		t.Fatalf("traversal err = %v", err)
	}
	// 目录必须拿不到 index.html（不能回退）。
	if _, err := src.ReadEntry(ctx, "k/1.zip", "hash-1", "slides/"); !errors.Is(err, ErrBundleEntryNotFound) {
		t.Fatalf("dir err = %v", err)
	}
}

func TestBundleSourceCachesByContentHash(t *testing.T) {
	ctx := context.Background()
	payload := makeZip(t, map[string]string{"index.html": "x"})
	var calls atomic.Int32
	src := NewBundleSource(func(context.Context, string) ([]byte, error) {
		calls.Add(1)
		return payload, nil
	})

	for i := 0; i < 5; i++ {
		if _, err := src.ReadEntry(ctx, "k/1.zip", "same-hash", "index.html"); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	// 同一 content_hash 不同 object_key 也只读一次：缓存键是内容，不是位置。
	if _, err := src.ReadEntry(ctx, "k/2.zip", "same-hash", "index.html"); err != nil {
		t.Fatalf("read other key: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("fetch calls = %d, want 1", n)
	}
}

func TestBundleSourceEvictsLRU(t *testing.T) {
	ctx := context.Background()
	zips := map[string][]byte{
		"h1": makeZip(t, map[string]string{"index.html": "1"}),
		"h2": makeZip(t, map[string]string{"index.html": "2"}),
		"h3": makeZip(t, map[string]string{"index.html": "3"}),
	}
	var calls atomic.Int32
	src := NewBundleSource(func(_ context.Context, key string) ([]byte, error) {
		calls.Add(1)
		return zips[strings.TrimPrefix(key, "k/")], nil
	}).WithLimits(1<<20, 1<<20, 2) // 只留 2 个包

	if _, err := src.ReadEntry(ctx, "k/h1", "h1", "index.html"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.ReadEntry(ctx, "k/h2", "h2", "index.html"); err != nil {
		t.Fatal(err)
	}
	// 访问 h1 让它变新，于是下一个淘汰的是 h2。
	if _, err := src.ReadEntry(ctx, "k/h1", "h1", "index.html"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.ReadEntry(ctx, "k/h3", "h3", "index.html"); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("fetch calls = %d, want 3 (h1 cached)", n)
	}
	// h2 已被淘汰，再读要重新取。
	if _, err := src.ReadEntry(ctx, "k/h2", "h2", "index.html"); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 4 {
		t.Fatalf("fetch calls = %d, want 4 (h2 reloaded)", n)
	}
}

func TestBundleSourceCoalescesConcurrentLoads(t *testing.T) {
	ctx := context.Background()
	payload := makeZip(t, map[string]string{"index.html": "x"})
	var calls atomic.Int32
	src := NewBundleSource(func(context.Context, string) ([]byte, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond) // 放大窗口，让并发真的撞在一起
		return payload, nil
	})

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := src.ReadEntry(ctx, "k/1.zip", "h", "index.html"); err != nil {
				t.Errorf("read: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("fetch calls = %d, want 1 (single-flight)", n)
	}
}

func TestBundleSourceRejectsOversizedAndInvalid(t *testing.T) {
	ctx := context.Background()
	// 用 Store（不压缩）构造：deflate 会把 4KB 的重复字符压到几十字节，
	// 用压缩后的长度做体积门禁断言等于什么都没验——判断的是"压缩率"而不是"上限"。
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "index.html", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(bytes.Repeat([]byte("x"), 4096)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	big := buf.Bytes()
	if len(big) <= 1024 {
		t.Fatalf("fixture too small to trip the limit: %d bytes", len(big))
	}

	src := NewBundleSource(func(context.Context, string) ([]byte, error) { return big, nil }).WithLimits(1024, 0, 0)
	if _, err := src.ReadEntry(ctx, "k", "h", "index.html"); !errors.Is(err, ErrBundleTooLarge) {
		t.Fatalf("oversized err = %v", err)
	}

	bad := NewBundleSource(func(context.Context, string) ([]byte, error) { return []byte("not a zip"), nil })
	if _, err := bad.ReadEntry(ctx, "k", "h", "index.html"); !errors.Is(err, ErrBundleInvalid) {
		t.Fatalf("invalid err = %v", err)
	}
}
