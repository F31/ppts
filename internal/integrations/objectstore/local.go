package objectstore

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// localScheme 是本地适配器的签名链接协议。开发档 API 可将该链接映射为
// "校验令牌 → 读取 root 下对象"的本地下载/上传端点；不支持 HTTP 直连。
const localScheme = "local"

// LocalFS 是单机开发/测试用的本地文件系统适配器（V4.0 §12.4"本地文件系统"）。
// 不用于多节点生产部署。文件路径完全由 ObjectKey 段拼接，配合 keys.go 的
// 字符白名单天然阻断路径穿越；这里再做一次 root 边界校验兜底。
type LocalFS struct {
	root   string
	signer *Signer
}

// NewLocal 创建本地适配器。secret 为空时 SignedURL 显式返回不支持。
func NewLocal(root string, secret []byte) *LocalFS {
	return &LocalFS{root: root, signer: NewSigner(secret)}
}

// pathFor 将 ObjectKey 映射为 root 内文件路径；解析后强制落在 root 之内。
func (l *LocalFS) pathFor(key ObjectKey) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	full := filepath.Join(l.root, filepath.FromSlash(key.String()))
	rel, err := filepath.Rel(l.root, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%w: path escapes root", ErrKeyInvalid)
	}
	return full, nil
}

// Put 写对象（覆盖同键旧对象）。调用方保证 key 的租户/项目前缀已获授权。
// 采用「临时文件 + rename」原子替换：并发读（如导出读取共享音频缓存）不会读到半截文件，
// 避免因读到截断对象导致下游（ffprobe 校验等）间歇性失败。
func (l *LocalFS) Put(_ context.Context, key ObjectKey, r io.Reader, meta ObjectMeta) error {
	full, err := l.pathFor(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".ppts-put-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	// rename 同目录内原子替换；保留 mode 权限。
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Get 读对象。对象不存在返回 ErrObjectNotFound。
func (l *LocalFS) Get(_ context.Context, key ObjectKey) (io.ReadCloser, ObjectMeta, error) {
	full, err := l.pathFor(key)
	if err != nil {
		return nil, ObjectMeta{}, err
	}
	f, err := os.Open(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ObjectMeta{}, ErrObjectNotFound
		}
		return nil, ObjectMeta{}, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, ObjectMeta{}, err
	}
	return f, ObjectMeta{Size: st.Size()}, nil
}

// Delete 删对象；对象不存在时按幂等处理（不报错）。
func (l *LocalFS) Delete(_ context.Context, key ObjectKey) error {
	full, err := l.pathFor(key)
	if err != nil {
		return err
	}
	err = os.Remove(full)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SignedURL 生成 local:// 协议链接，内嵌绑定 (key, op, ttl) 的令牌。
// 使用方式：解析链接 → Verify 令牌 → 校验 op → 按 key 读写对象。
func (l *LocalFS) SignedURL(_ context.Context, key ObjectKey, op Operation, ttl time.Duration) (string, error) {
	if l.signer == nil || len(l.signer.secret) == 0 {
		return "", fmt.Errorf("%w: local backend requires a signer secret for signed URLs", ErrOperationNotSupported)
	}
	token, err := l.signer.Sign(key.TenantID, key, op, ttl)
	if err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("token", token)
	q.Set("op", string(op))
	return (&url.URL{Scheme: localScheme, Host: "object", Path: key.String(), RawQuery: q.Encode()}).String(), nil
}

// ParseSignedURL 解析本地签名链接并校验，返回其绑定的键与操作。
// 供开发档 API 的本地直连端点使用；生产后端由 S3 原生预签名替代。
func (l *LocalFS) ParseSignedURL(raw string) (ObjectKey, Operation, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != localScheme {
		return ObjectKey{}, "", ErrSignatureInvalid
	}
	token := u.Query().Get("token")
	if token == "" {
		return ObjectKey{}, "", ErrSignatureInvalid
	}
	key, op, err := l.signer.Verify(token)
	if err != nil {
		return ObjectKey{}, "", err
	}
	// 链接路径与令牌绑定键必须一致，防止换键。url.Parse 会给 path 加前导 "/"。
	if strings.TrimPrefix(u.Path, "/") != key.String() {
		return ObjectKey{}, "", ErrSignatureInvalid
	}
	return key, op, nil
}

// ApplyLifecyclePolicy 本地适配器不支持生命周期分层，显式返回不支持。
func (l *LocalFS) ApplyLifecyclePolicy(context.Context, string, LifecyclePolicy) error {
	return ErrOperationNotSupported
}
