package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// 底图投递（tasks 2.0）。
//
// 渲染服务只从自己的 `-assets-dir` 读 baseImage（ADR-14），而页面图在对象存储里。
// 两者之间原本没有通路。这里把「把字节送到渲染服务能读的位置」抽成一个接口：
//
//	单机部署 → LocalAssetPublisher：写进共享目录（导出容器与渲染容器挂同一个卷）。
//	多机/S3   → 另一个实现：上传到渲染服务可读的存储，返回**同一个语义**的相对键。
//
// 接口的返回值刻意定义为「渲染服务资产根下的相对路径」，而不是本地绝对路径：
// 路径一旦带上前缀，换实现就必然要动调用方。S3 实现只要能保证渲染侧按同一个相对键
// 取值（例如渲染服务新增 `PUT /v1/assets/{path}`，或直接以 S3 为资产根），
// 导出侧一行都不用改。

// AssetPublisher 把一份资产投递到渲染服务可读的位置。
//
// 返回值是渲染服务资产根下的相对路径（斜杠分隔）。同一次导出内多次投递同一份字节
// MUST 返回同一个路径——计划里的 baseImage 会被 `planHash` 固定下来，
// 路径不稳定会让同一份计划算出两个哈希。
type AssetPublisher interface {
	Publish(ctx context.Context, contentType string, data []byte) (relPath string, err error)
}

// 共享卷是跨容器的：两个容器常常不是同一个 uid。目录必须可穿透（o+x）、
// 文件必须可读（o+r），否则导出侧写成功了，渲染侧读的时候是 permission denied，
// 而错误发生在渲染容器里，看起来像「服务坏了」。
const (
	assetDirPerm  os.FileMode = 0o755
	assetFilePerm os.FileMode = 0o644
)

// LocalAssetPublisher 把资产写进一个本地目录，并返回相对路径。
//
// 命名用内容摘要而不是「页码」：
//   - 同一份图重复导出只写一次（页面图按源版本不可变，重复很常见）；
//   - 不同租户的页图不可能互相覆盖——按页码命名时，两个租户的第 3 页写的是同一个
//     文件，后写完的那次会让前一次导出渲染出别人的页面，这是数据串租户，不只是脏读；
//   - 写入用「同目录临时文件 + rename」，渲染侧永远看不到半个文件。
//
// 代价是目录只增不减。它是**缓存**而不是数据：删掉不会丢任何东西，下次导出重写。
// 清理策略交给部署（按 mtime 定期 prune），不在每次导出里做——那会把一次导出
// 变成一次全目录扫描。
type LocalAssetPublisher struct {
	root string
}

// NewLocalAssetPublisher 校验目录可用并建好根目录。
//
// 启动时探一次可写：等到第一次导出才发现目录不可写，表现为「每单都静默降级成静态版」，
// 那是最难查的一类故障。这里失败就让调用方在启动日志里说清楚。
func NewLocalAssetPublisher(root string) (*LocalAssetPublisher, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("animation: asset dir is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("animation: asset dir %q: %w", root, err)
	}
	if err := os.MkdirAll(abs, assetDirPerm); err != nil {
		return nil, fmt.Errorf("animation: create asset dir %q: %w", abs, err)
	}
	probe, err := os.CreateTemp(abs, ".probe-*")
	if err != nil {
		return nil, fmt.Errorf("animation: asset dir %q is not writable: %w", abs, err)
	}
	name := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return nil, fmt.Errorf("animation: probe asset dir %q: %w", abs, closeErr)
	}
	if removeErr != nil {
		return nil, fmt.Errorf("animation: probe asset dir %q: %w", abs, removeErr)
	}
	return &LocalAssetPublisher{root: abs}, nil
}

// Root 返回根目录的绝对路径，供启动日志与部署核对使用。
func (p *LocalAssetPublisher) Root() string {
	if p == nil {
		return ""
	}
	return p.root
}

// Publish 写入一份资产并返回相对路径。
func (p *LocalAssetPublisher) Publish(ctx context.Context, contentType string, data []byte) (string, error) {
	if len(data) == 0 {
		// 空图会让那页渲染成透明/黑。宁可在这里失败让调用方降级，
		// 也不要把一个「能编译、能渲染、结果是空白」的资产送过去。
		return "", errors.New("animation: empty asset")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ext, err := assetExt(contentType)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	// 两级散列前缀：一千万页图放一个目录里，readdir 会慢到影响运维排查。
	rel := path.Join("pages", digest[:2], digest+ext)
	dst := filepath.Join(p.root, filepath.FromSlash(rel))

	switch info, statErr := os.Lstat(dst); {
	case statErr == nil:
		// 已存在即复用。但仍然要检查它不是符号链接、也不是目录——
		// 共享卷可能被别的进程写过，而 rename 到符号链接上是跟随写入，
		// 等于给了对方一个「往任意路径写」的把手。
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("animation: asset %s exists but is not a regular file (mode=%s)", rel, info.Mode().Type())
		}
		return rel, nil
	case !errors.Is(statErr, fs.ErrNotExist):
		return "", fmt.Errorf("animation: stat asset %s: %w", rel, statErr)
	}

	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, assetDirPerm); err != nil {
		return "", fmt.Errorf("animation: create asset dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("animation: create temp asset: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return "", fmt.Errorf("animation: write asset: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", fmt.Errorf("animation: close asset: %w", err)
	}
	// CreateTemp 给的是 0600，跨 uid 的渲染容器读不到。
	if err := os.Chmod(tmpName, assetFilePerm); err != nil {
		cleanup()
		return "", fmt.Errorf("animation: chmod asset: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		cleanup()
		return "", fmt.Errorf("animation: publish asset: %w", err)
	}
	return rel, nil
}

// assetExt 把内容类型映射成扩展名。
//
// 只接受明确认识的类型：渲染侧按扩展名判断解码器，给错扩展名会得到
// 「文件存在但解不出来」。未知类型在这里失败，比在渲染服务里变成一张空白页好查。
func assetExt(contentType string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/png":
		return ".png", nil
	case "image/jpeg", "image/jpg":
		return ".jpg", nil
	case "image/webp":
		return ".webp", nil
	default:
		return "", fmt.Errorf("animation: unsupported asset content type %q", contentType)
	}
}
