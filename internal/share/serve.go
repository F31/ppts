package share

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"sync"
)

// 成品包的在线分发（M6-3）：把 zip 里的**单个条目**按请求取出。
//
// 为什么不是"导出时把包解开、逐条目写对象存储"（design ADR-3）：写放大几十倍，
// 删除要级联，而且与"成品就是一个 zip"的既有语义冲突。也不是"每请求整包读进内存"：
// 20 页包约 2.5MB，几十个并发就是几百 MB 的瞬时分配。
//
// 这里的前提是成品**内容寻址**——ContentHash 相同即字节相同，所以缓存不需要失效，
// 只需要淘汰。缓存键取 content_hash 而非 object_key：同一内容的多份对象键只占一份内存。

var (
	// ErrBundleEntryNotFound 表示包里没有该条目。调用方**不得**回退到 index.html：
	// 回退会让 slides/999.png 拿到一页 HTML，<img> 静默裂图，比 404 更难查。
	ErrBundleEntryNotFound = errors.New("share: bundle entry not found")
	// ErrBundleTooLarge 表示包超过单包读取上限。不截断——截断后的 zip 解析必然失败，
	// 只会把"包异常"伪装成"包损坏"。
	ErrBundleTooLarge = errors.New("share: bundle exceeds size limit")
	// ErrBundleInvalid 表示读到的字节不是合法 zip（或为空）。
	ErrBundleInvalid = errors.New("share: bundle is not a valid zip")
)

// DefaultBundleLimits 是默认上限，可由 NewBundleSource 覆盖。
const (
	DefaultMaxBundleBytes = 64 << 20 // 单包 64 MiB
	DefaultMaxCacheBytes  = 256 << 20
	DefaultMaxCacheItems  = 8
)

// BundleSource 按条目读取成品包，并在进程内做 LRU 缓存。
//
// 并发首请求用单飞合并（inFlight）：没有它，一次冷启动的 50 个并发资源请求会把
// 同一个包读 50 遍——这正是 zip 分发最容易踩的性能坑。
type BundleSource struct {
	fetch func(ctx context.Context, objectKey string) ([]byte, error)

	maxBundleBytes int64
	maxCacheBytes  int64
	maxCacheItems  int

	mu       sync.Mutex
	items    map[string]*bundleItem
	order    []string // 最近使用在尾部
	bytes    int64
	inFlight map[string]chan struct{}
}

type bundleItem struct {
	files map[string]*zip.File
	size  int64
}

// NewBundleSource 构造缓存。fetch 负责把 object_key 读成整包字节（对象存储的
// Get 返回不可 seek 的流，zip.Reader 需要 ReaderAt，所以必须先落成 []byte）。
func NewBundleSource(fetch func(ctx context.Context, objectKey string) ([]byte, error)) *BundleSource {
	return &BundleSource{
		fetch:          fetch,
		maxBundleBytes: DefaultMaxBundleBytes,
		maxCacheBytes:  DefaultMaxCacheBytes,
		maxCacheItems:  DefaultMaxCacheItems,
		items:          map[string]*bundleItem{},
		inFlight:       map[string]chan struct{}{},
	}
}

// WithLimits 覆盖默认上限（测试用；生产走默认值）。
func (s *BundleSource) WithLimits(maxBundleBytes, maxCacheBytes int64, maxCacheItems int) *BundleSource {
	if maxBundleBytes > 0 {
		s.maxBundleBytes = maxBundleBytes
	}
	if maxCacheBytes > 0 {
		s.maxCacheBytes = maxCacheBytes
	}
	if maxCacheItems > 0 {
		s.maxCacheItems = maxCacheItems
	}
	return s
}

// Entry 是一个条目的原始字节。
type Entry struct {
	Name string
	Data []byte
}

// ReadEntry 取包内单个条目的内容。
//
// name 是已经过调用方 URL 解码的相对路径（如 "slides/001.png"）。本函数**再校验一次**：
// 越界路径在这里返回 ErrBundleEntryNotFound，与"条目不存在"同码。理由见 SafeEntryName。
func (s *BundleSource) ReadEntry(ctx context.Context, objectKey, contentHash, name string) (*Entry, error) {
	if !SafeEntryName(name) {
		return nil, ErrBundleEntryNotFound
	}
	files, err := s.files(ctx, objectKey, contentHash)
	if err != nil {
		return nil, err
	}
	f, ok := files[name]
	if !ok {
		return nil, ErrBundleEntryNotFound
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	// 条目大小取中央目录里的 UncompressedSize64：不信 zip 头里的声明上限，
	// 但也不要无限读——LimitReader 多留 1 字节用于判断"实际比声明的长"。
	limit := int64(f.UncompressedSize64)
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != limit {
		return nil, ErrBundleInvalid
	}
	return &Entry{Name: name, Data: data}, nil
}

// SafeEntryName 判断一个请求路径能不能当包内条目名。
//
// 不满足就拒绝，而不是"清理后再用"：`filepath.Clean` 会把 `a/../b` 修成 `b`，
// 等于把攻击路径**修好**再放行——正确行为是拒绝。规则与 objectstore.checkResourceKey
// 同一套纪律：拒绝绝对路径、反斜杠、空段、`.`/`..` 段、结尾斜杠（目录）。
func SafeEntryName(name string) bool {
	if name == "" || len(name) > 512 {
		return false
	}
	if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	if strings.HasSuffix(name, "/") {
		return false
	}
	// 控制字符一律拒绝：它们既不是合法文件名，也常被用来绕过日志/终端显示。
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	if name != path.Clean(name) {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func (s *BundleSource) files(ctx context.Context, objectKey, contentHash string) (map[string]*zip.File, error) {
	key := contentHash
	if key == "" {
		key = objectKey
	}
	if files, ok := s.lookup(key); ok {
		return files, nil
	}
	// 单飞：同 key 的并发首请求只读一次包。
	for {
		s.mu.Lock()
		if files, ok := s.items[key]; ok {
			s.touchLocked(key)
			s.mu.Unlock()
			return files.files, nil
		}
		if ch, ok := s.inFlight[key]; ok {
			s.mu.Unlock()
			select {
			case <-ch:
				continue // 别人读完了，回到循环头部命中缓存
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		ch := make(chan struct{})
		s.inFlight[key] = ch
		s.mu.Unlock()

		files, size, err := s.load(ctx, objectKey)

		s.mu.Lock()
		delete(s.inFlight, key)
		if err == nil {
			s.storeLocked(key, files, size)
		}
		s.mu.Unlock()
		close(ch)
		return files, err
	}
}

func (s *BundleSource) load(ctx context.Context, objectKey string) (map[string]*zip.File, int64, error) {
	data, err := s.fetch(ctx, objectKey)
	if err != nil {
		return nil, 0, err
	}
	size := int64(len(data))
	if s.maxBundleBytes > 0 && size > s.maxBundleBytes {
		return nil, 0, ErrBundleTooLarge
	}
	zr, err := zip.NewReader(bytes.NewReader(data), size)
	if err != nil {
		return nil, 0, ErrBundleInvalid
	}
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		// 重名条目取第一个：zip 规范允许重名，不同工具的解压行为不一致，
		// 这里固定一种解释，避免"同样的包在不同环境出不同内容"。
		if _, ok := files[f.Name]; !ok {
			files[f.Name] = f
		}
	}
	return files, size, nil
}

func (s *BundleSource) lookup(key string) (map[string]*zip.File, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[key]
	if !ok {
		return nil, false
	}
	s.touchLocked(key)
	return item.files, true
}

func (s *BundleSource) touchLocked(key string) {
	for i, k := range s.order {
		if k == key {
			s.order = append(append(s.order[:i:i], s.order[i+1:]...), key)
			return
		}
	}
}

func (s *BundleSource) storeLocked(key string, files map[string]*zip.File, size int64) {
	if _, ok := s.items[key]; ok {
		return
	}
	s.items[key] = &bundleItem{files: files, size: size}
	s.order = append(s.order, key)
	s.bytes += size
	for len(s.order) > 0 &&
		((s.maxCacheItems > 0 && len(s.order) > s.maxCacheItems) ||
			(s.maxCacheBytes > 0 && s.bytes > s.maxCacheBytes)) {
		oldest := s.order[0]
		s.order = s.order[1:]
		if item, ok := s.items[oldest]; ok {
			s.bytes -= item.size
			delete(s.items, oldest)
		}
	}
}
