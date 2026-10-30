package render

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 本文件是 M5 的片段缓存存储层。
//
// 设计取舍（详见 design.md ADR-2/ADR-4）：
//   - 接口按**对象存储语义**定义（按键读、按键写、无目录概念），本地实现是目录。
//     换 S3/MinIO 时只需再实现同一个接口，服务层与键计算都不动。
//   - 缓存是**派生物**，不是事实来源：任何读错误都退化成「未命中」，
//     任何写错误都只记日志。它 MUST NOT 有能力让一次渲染失败。
//   - 键由 SegmentKeys 给出（64 位十六进制），存储层只校验形状，不理解语义。

// SegmentCache 是片段缓存的最小契约。
//
// Get 把 key 对应的片段**复制**到 dst：调用方拿到的是自己的文件，
// 后续（拼接、校验、上传）都按普通临时文件对待，缓存目录始终只读不借。
type SegmentCache interface {
	// Get 命中时把片段复制到 dst 并返回 true。未命中返回 (false, nil)。
	Get(ctx context.Context, key, dst string) (bool, error)
	// Put 把 src 存到 key。已存在时覆盖（同键 = 同像素，覆盖是幂等的）。
	Put(ctx context.Context, key, src string) error
}

// DirSegmentCache 把片段存进本地目录，布局 `root/<key[:2]>/<key>.mp4`。
//
// 两级散列前缀与 animation.LocalAssetPublisher 同一理由：单目录放十万个文件后
// readdir 会慢到影响运维排查。
type DirSegmentCache struct {
	root string
	// maxBytes 是软上限（0 = 不限）。超限时按 mtime 从旧到新淘汰到 90%，
	// 留 10% 余量是为了避免「每次 Put 都触发一次全目录扫描」。
	maxBytes int64

	mu sync.Mutex
	// size 是最近一次扫描得到的近似总字节数；Put 后累加，淘汰时重算。
	size int64
	// scanned 为 false 时 size 不可信，下一次 Put 前先扫一遍。
	scanned bool
}

// segmentCacheSuffix 是片段文件的扩展名。带扩展名是为了让「目录里躺着一个
// 打不开的文件」在 ls 时就能看出它本该是什么。
const segmentCacheSuffix = ".mp4"

// segmentCacheEvictTarget 是淘汰后的目标水位（上限的 90%）。
const segmentCacheEvictTarget = 0.9

// NewDirSegmentCache 校验目录可用并返回缓存。
//
// 目录不存在时创建：缓存目录是运维配的，第一次部署不该要求先 mkdir。
func NewDirSegmentCache(root string, maxBytes int64) (*DirSegmentCache, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("render: segment cache root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("render: resolve segment cache root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("render: create segment cache root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("render: stat segment cache root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("render: segment cache root %q is not a directory", abs)
	}
	return &DirSegmentCache{root: abs, maxBytes: maxBytes}, nil
}

// Root 返回缓存根目录（绝对路径），供日志与运维核对。
func (c *DirSegmentCache) Root() string { return c.root }

// validSegmentKey 只接受 64 位小写十六进制。
//
// 存储层校验键的形状不是重复劳动：键最终会变成路径的一段，
// 而「路径来自请求」是这类组件被穿透的经典入口之一。
func validSegmentKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	for i := 0; i < len(key); i++ {
		ch := key[i]
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func (c *DirSegmentCache) pathFor(key string) (string, error) {
	if !validSegmentKey(key) {
		return "", fmt.Errorf("render: invalid segment key %q", key)
	}
	return filepath.Join(c.root, key[:2], key+segmentCacheSuffix), nil
}

// Get 实现 SegmentCache。
//
// 先读后删的顺序有讲究：dst 可能是「上一轮残留的同名片段」（job 目录在起跑前
// 已清空，但调用方也可能复用目录），所以未命中时 MUST NOT 留下任何文件——
// 否则调用方会把上一轮的片段当成这一轮的结果。这里选择「不碰 dst」，
// 由服务层保证目录是干净的（run 起跑即 RemoveAll）。
func (c *DirSegmentCache) Get(ctx context.Context, key, dst string) (bool, error) {
	src, err := c.pathFor(key)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	in, err := os.Open(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("render: open cached segment: %w", err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return false, fmt.Errorf("render: prepare segment dst dir: %w", err)
	}
	out, err := os.Create(dst)
	if err != nil {
		return false, fmt.Errorf("render: create segment dst: %w", err)
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return false, fmt.Errorf("render: copy cached segment: %w", copyErr)
	}
	if syncErr != nil {
		_ = os.Remove(dst)
		return false, fmt.Errorf("render: close segment dst: %w", syncErr)
	}
	return true, nil
}

// Put 实现 SegmentCache。写入走「临时文件 + rename」：
// 另一个副本可能在同键上并发写，rename 保证读到它的人只会看到完整文件。
func (c *DirSegmentCache) Put(ctx context.Context, key, src string) error {
	dst, err := c.pathFor(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("render: stat segment to cache: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("render: segment to cache is not a non-empty regular file")
	}
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("render: create segment cache dir: %w", err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("render: open segment to cache: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		in.Close()
		return fmt.Errorf("render: create temp cache file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	_, copyErr := io.Copy(tmp, in)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	in.Close()
	if copyErr != nil {
		return fmt.Errorf("render: write cached segment: %w", copyErr)
	}
	if syncErr != nil {
		return fmt.Errorf("render: sync cached segment: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("render: close cached segment: %w", closeErr)
	}
	if err := renameWithRetry(tmpName, dst); err != nil {
		return fmt.Errorf("render: publish cached segment: %w", err)
	}
	cleanup = false
	c.notePut(info.Size(), dst)
	return nil
}

// renameWithRetry 把临时文件改名成正式名。
//
// Windows 上 os.Rename 会失败并返回 "Access is denied"——当一个同名目标正被另一个
// 副本（或另一个读文件的进程）打开时。**同键 = 同像素**，所以别人写赢了不算失败：
// 重试几次，仍然失败但目标已经是一个非空文件时按成功处理。
// 不这么做的话，两台机器同时渲同一页会让其中一台的整个任务失败——
// 而缓存按设计没有能力让渲染失败。
func renameWithRetry(src, dst string) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if err = os.Rename(src, dst); err == nil {
			return nil
		}
		if info, statErr := os.Stat(dst); statErr == nil && info.Size() > 0 {
			_ = os.Remove(src)
			return nil
		}
		time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
	}
	return err
}

// notePut 更新计数并在超限时淘汰。
//
// 淘汰失败不影响本次 Put 的成功语义：缓存目录写不进去只该退化，
// 不该让渲染失败——所以这里只返回给调用方「是否发生了淘汰」的副作用，
// 错误被吞进 size 的重算里。
func (c *DirSegmentCache) notePut(size int64, written string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.scanned {
		c.size = c.scanSizeLocked()
		c.scanned = true
	}
	c.size += size
	if c.maxBytes <= 0 || c.size <= c.maxBytes {
		return
	}
	c.evictLocked(written)
}

// scanSizeLocked 重新统计缓存总大小。调用方必须已持有 c.mu。
func (c *DirSegmentCache) scanSizeLocked() int64 {
	var total int64
	_ = filepath.WalkDir(c.root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, statErr := d.Info(); statErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

type cacheEntry struct {
	path    string
	size    int64
	modTime int64
}

// evictLocked 按 mtime 从旧到新淘汰到目标水位。
//
// 刻意不删「本次刚写入的文件」：否则一次超限的 Put 会立刻把自己的产物删掉，
// 下一轮又要重渲——淘汰应该处置旧数据，而不是制造抖动。
func (c *DirSegmentCache) evictLocked(keep string) {
	entries := make([]cacheEntry, 0, 256)
	var total int64
	_ = filepath.WalkDir(c.root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		e := cacheEntry{path: p, size: info.Size(), modTime: info.ModTime().UnixNano()}
		total += e.size
		entries = append(entries, e)
		return nil
	})
	target := int64(float64(c.maxBytes) * segmentCacheEvictTarget)
	if total <= c.maxBytes {
		c.size = total
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].modTime < entries[j].modTime })
	for _, e := range entries {
		if total <= target {
			break
		}
		if e.path == keep {
			continue
		}
		if err := os.Remove(e.path); err != nil {
			continue
		}
		total -= e.size
	}
	c.size = total
}

// Lookup 是 Get 的「只查不拷」版本，供测试与运维脚本使用。
func (c *DirSegmentCache) Lookup(key string) (int64, bool) {
	p, err := c.pathFor(key)
	if err != nil {
		return 0, false
	}
	info, err := os.Stat(p)
	if err != nil {
		return 0, false
	}
	return info.Size(), true
}
