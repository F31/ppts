package render

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// 缓存存储层的判据只有四条：命中给出同样的字节、未命中不留下垃圾、
// 原子写（并发读不到半个文件）、超限时淘汰旧数据。其余都是实现细节。

func writeTemp(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

func cacheKey(t *testing.T, fill byte) string {
	t.Helper()
	keys, err := SegmentKeys(multiPagePlan(2), testEnv())
	if err != nil {
		t.Fatal(err)
	}
	key := []byte(keys[0])
	key[63] = fill
	return string(key)
}

func TestDirSegmentCacheRoundTrip(t *testing.T) {
	root := t.TempDir()
	c, err := NewDirSegmentCache(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	src := writeTemp(t, t.TempDir(), "segment_000.mp4", bytes.Repeat([]byte{7}, 2048))
	key := cacheKey(t, 'a')

	if err := c.Put(context.Background(), key, src); err != nil {
		t.Fatalf("Put: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "out.mp4")
	ok, err := c.Get(context.Background(), key, dst)
	if err != nil || !ok {
		t.Fatalf("Get = (%v, %v), want (true, nil)", ok, err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(src)
	if !bytes.Equal(got, want) {
		t.Fatalf("bytes differ: got %d, want %d", len(got), len(want))
	}
	// Get 是复制，不是链接：源文件必须在原地。
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("Get must not move the source: %v", err)
	}
}

func TestDirSegmentCacheMissLeavesNothing(t *testing.T) {
	c, err := NewDirSegmentCache(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "out.mp4")
	ok, err := c.Get(context.Background(), cacheKey(t, 'b'), dst)
	if err != nil || ok {
		t.Fatalf("Get = (%v, %v), want (false, nil)", ok, err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("miss must not create dst: %v", err)
	}
}

func TestDirSegmentCacheRejectsMalformedKey(t *testing.T) {
	c, err := NewDirSegmentCache(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	src := writeTemp(t, t.TempDir(), "s.mp4", bytes.Repeat([]byte{1}, 2048))
	for _, key := range []string{"", "../../etc/passwd", "xyz", "0123456789ABCDEF"} {
		if err := c.Put(context.Background(), key, src); err == nil {
			t.Fatalf("key %q must be rejected", key)
		}
		if _, err := c.Get(context.Background(), key, filepath.Join(t.TempDir(), "x")); err == nil {
			t.Fatalf("key %q must be rejected by Get", key)
		}
	}
}

// 并发写同一个键：读到的人只会看到完整文件（临时文件 + rename）。
func TestDirSegmentCacheConcurrentPutIsAtomic(t *testing.T) {
	c, err := NewDirSegmentCache(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	key := cacheKey(t, 'c')
	payload := bytes.Repeat([]byte{9}, 64<<10)
	src := writeTemp(t, t.TempDir(), "s.mp4", payload)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.Put(context.Background(), key, src); err != nil {
				t.Errorf("Put: %v", err)
			}
		}()
	}
	wg.Wait()

	dst := filepath.Join(t.TempDir(), "out.mp4")
	if ok, err := c.Get(context.Background(), key, dst); err != nil || !ok {
		t.Fatalf("Get = (%v, %v)", ok, err)
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, payload) {
		t.Fatalf("concurrent Put produced a torn file: %d bytes", len(got))
	}
	// 每次 Put 都应清理掉自己的临时文件。
	entries, _ := os.ReadDir(filepath.Join(c.Root(), key[:2]))
	for _, e := range entries {
		if filepath.Ext(e.Name()) != segmentCacheSuffix {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}

func TestDirSegmentCacheEvictsOldestFirst(t *testing.T) {
	root := t.TempDir()
	// 上限 4 KiB，每条 1 KiB：塞 6 条必然触发淘汰。
	c, err := NewDirSegmentCache(root, 4<<10)
	if err != nil {
		t.Fatal(err)
	}
	src := writeTemp(t, t.TempDir(), "s.mp4", bytes.Repeat([]byte{3}, 1<<10))
	keys := []string{cacheKey(t, 'd'), cacheKey(t, 'e'), cacheKey(t, 'f'), cacheKey(t, '1'), cacheKey(t, '2'), cacheKey(t, '3')}
	for _, k := range keys {
		if err := c.Put(context.Background(), k, src); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	// 最新一条必须在（不能把刚写进来的删掉）。
	if _, ok := c.Lookup(keys[len(keys)-1]); !ok {
		t.Fatal("eviction must not remove the entry just written")
	}
	var total int64
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	if total > 4<<10 {
		t.Fatalf("cache over budget after eviction: %d bytes", total)
	}
	if total == 0 {
		t.Fatal("eviction removed everything; it should stop at the target watermark")
	}
}

func TestDirSegmentCacheRejectsEmptySource(t *testing.T) {
	c, err := NewDirSegmentCache(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	empty := writeTemp(t, t.TempDir(), "empty.mp4", nil)
	if err := c.Put(context.Background(), cacheKey(t, '4'), empty); err == nil {
		t.Fatal("an empty segment must not be cached")
	}
}
