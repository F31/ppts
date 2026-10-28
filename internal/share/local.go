package share

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/F31/ppts/internal/media"
)

// ErrOutDirNotEmpty 表示目标目录已有内容。
//
// 刻意拒绝覆盖：成品包按页数命名 slides/NNN.jpg，若上一轮是 5 页、这一轮是 3 页，
// 覆盖写会留下 slides/004.jpg 与 slides/005.jpg，肉眼看不到、体积却涨了，还可能在
// 后续巡检里被当成"内容泄漏"。要求空目录是最省事的正确做法。
var ErrOutDirNotEmpty = errors.New("share: output directory must be empty")

// DirReader 以本地目录为资源根，供隐私模式（本地生成）使用。
//
// key 一律是**相对 root 的 slash 分隔路径**（例如 "pages/001.png"、"audio/0001.wav"），
// 与对象存储 key 在打包器眼里同形，因此打包器不需要区分云端与本地（design D6）。
// 读取只走 os.DirFS：不解析 URL、不发网络请求（tasks 5.2）。
type DirReader struct {
	root string
	fsys fs.FS
}

// NewDirReader 打开本地素材根目录。root 必须是已存在的目录。
func NewDirReader(root string) (*DirReader, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("share: local resource root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("share: resolve local resource root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("share: local resource root %q: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("share: local resource root %q is not a directory", abs)
	}
	return &DirReader{root: abs, fsys: os.DirFS(abs)}, nil
}

// Root 返回素材根目录的绝对路径（日志与错误信息用）。
func (r *DirReader) Root() string {
	if r == nil {
		return ""
	}
	return r.root
}

// OpenPagePNG 与 OpenObject 在本地是同一个动作：按相对路径开文件。
// 分成两个方法只是为了满足 ResourceReader——云端才需要区分桶/前缀。
func (r *DirReader) OpenPagePNG(_ context.Context, key string) (io.ReadCloser, error) {
	return r.open(key)
}

func (r *DirReader) OpenObject(_ context.Context, key string) (io.ReadCloser, error) {
	return r.open(key)
}

func (r *DirReader) open(key string) (io.ReadCloser, error) {
	if r == nil || r.fsys == nil {
		return nil, errors.New("share: local reader is not initialized")
	}
	rel, err := normalizeLocalKey(key)
	if err != nil {
		return nil, err
	}
	f, err := r.fsys.Open(rel)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.IsDir() {
		f.Close()
		return nil, fmt.Errorf("share: local resource %q is a directory", key)
	}
	return f, nil
}

// normalizeLocalKey 把本地 key 收敛成 io/fs 认可的 slash 相对路径。
//
// Windows 用户习惯写反斜杠，直接交给 os.DirFS 会被当作分隔符，等于绕过 checkBundlePath
// 的字符白名单；这里先统一成 slash 再逐段检查，避免"看起来被拒、实际能读到"。
func normalizeLocalKey(key string) (string, error) {
	if err := checkResourceKey(key); err != nil {
		return "", err
	}
	rel := strings.ReplaceAll(strings.TrimSpace(key), "\\", "/")
	rel = strings.TrimPrefix(rel, "./")
	if !fs.ValidPath(rel) {
		return "", fmt.Errorf("share: local resource key %q is not a valid relative path", key)
	}
	return rel, nil
}

// LocalBundle 是隐私模式的一次打包：素材根目录 + 打包输入。
// BundleInput 中的 PagePNGKeys/SRTKey/VTTKey/AudioKey 全部相对 Root。
type LocalBundle struct {
	Root  string
	Input BundleInput
}

// BuildLocalToDir 在本地生成成品包并写到 outDir（tasks 5.1/5.2）。
//
// 全程只读 Root 下的文件、只写 outDir，不接触对象存储，也不发起任何网络请求。
func BuildLocalToDir(ctx context.Context, in LocalBundle, outDir string) (*Manifest, error) {
	reader, err := NewDirReader(in.Root)
	if err != nil {
		return nil, err
	}
	return NewBundleBuilder(reader, nil).BuildDir(ctx, in.Input, outDir)
}

// BuildLocalToZip 与 BuildLocalToDir 同源，只是落成可分发的 zip（测试与 CLI 用）。
func BuildLocalToZip(ctx context.Context, in LocalBundle, out io.Writer) (*Manifest, error) {
	reader, err := NewDirReader(in.Root)
	if err != nil {
		return nil, err
	}
	return NewBundleBuilder(reader, nil).Build(ctx, in.Input, out)
}

// ReadLocalTimeline 从本地目录读取时间轴，作为隐私模式的时间来源。
func ReadLocalTimeline(root, key string) (*media.Timeline, error) {
	reader, err := NewDirReader(root)
	if err != nil {
		return nil, err
	}
	data, err := readAllClose(reader.OpenObject(context.Background(), key))
	if err != nil {
		return nil, fmt.Errorf("share: read local timeline %q: %w", key, err)
	}
	var timeline media.Timeline
	if err := json.Unmarshal(data, &timeline); err != nil {
		return nil, fmt.Errorf("share: parse local timeline %q: %w", key, err)
	}
	if len(timeline.Slides) == 0 {
		return nil, fmt.Errorf("share: local timeline %q has no slides", key)
	}
	return &timeline, nil
}

// BuildDir 把成品包写成目录（解压后的形态），结构与 Build 产出的 zip 完全一致。
//
// 目录形态在隐私模式下更实用：用户直接双击 outDir/index.html 就能播，不必先解压。
func (b *BundleBuilder) BuildDir(ctx context.Context, in BundleInput, outDir string) (*Manifest, error) {
	if strings.TrimSpace(outDir) == "" {
		return nil, errors.New("share: output directory is required")
	}
	absDir, err := filepath.Abs(outDir)
	if err != nil {
		return nil, fmt.Errorf("share: resolve output directory: %w", err)
	}
	created, err := prepareOutputDir(absDir)
	if err != nil {
		return nil, err
	}

	var written []string
	manifest, files, buildErr := b.buildInto(ctx, in, func(name string, data []byte) error {
		target, joinErr := safeJoin(absDir, name)
		if joinErr != nil {
			return joinErr
		}
		if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
			return fmt.Errorf("share: create entry directory: %w", mkErr)
		}
		if writeErr := os.WriteFile(target, data, 0o644); writeErr != nil {
			return fmt.Errorf("share: write entry %q: %w", name, writeErr)
		}
		written = append(written, target)
		return nil
	})
	if buildErr != nil {
		// 失败时清理本次写入：留着半成品目录会让下一次导出撞上 ErrOutDirNotEmpty。
		cleanupWritten(absDir, written, created)
		return nil, buildErr
	}
	if err := manifest.Validate(files); err != nil {
		cleanupWritten(absDir, written, created)
		return nil, err
	}
	return manifest, nil
}

// prepareOutputDir 确保 outDir 存在且为空，返回"是否由本次创建"。
func prepareOutputDir(absDir string) (bool, error) {
	entries, err := os.ReadDir(absDir)
	switch {
	case err == nil:
		if len(entries) > 0 {
			return false, fmt.Errorf("%w: %s", ErrOutDirNotEmpty, absDir)
		}
		return false, nil
	case errors.Is(err, fs.ErrNotExist):
		if mkErr := os.MkdirAll(absDir, 0o755); mkErr != nil {
			return false, fmt.Errorf("share: create output directory: %w", mkErr)
		}
		return true, nil
	default:
		return false, fmt.Errorf("share: inspect output directory: %w", err)
	}
}

// safeJoin 把已校验的包内路径拼到输出目录下，再确认结果确实在目录内。
// checkBundlePath 已经拒绝绝对路径与 `..`，这里是第二道防线（防御性冗余）。
func safeJoin(base, name string) (string, error) {
	if err := checkBundlePath(name); err != nil {
		return "", err
	}
	target := filepath.Join(base, filepath.FromSlash(name))
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", fmt.Errorf("share: resolve entry %q: %w", name, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("share: entry %q escapes the output directory", name)
	}
	return target, nil
}

// cleanupWritten 删除本次写入的文件；只在目录由本次创建时连目录一起删。
func cleanupWritten(absDir string, written []string, createdDir bool) {
	for i := len(written) - 1; i >= 0; i-- {
		_ = os.Remove(written[i])
	}
	if createdDir {
		_ = os.RemoveAll(absDir)
		return
	}
	// 目录原本就存在：只回收本次创建的空子目录（slides/、audio/）。
	_ = os.Remove(filepath.Join(absDir, "slides"))
	_ = os.Remove(filepath.Join(absDir, "audio"))
}

// 编译期确认 DirReader 满足 ResourceReader（本地与对象存储可互换的前提）。
var _ ResourceReader = (*DirReader)(nil)
