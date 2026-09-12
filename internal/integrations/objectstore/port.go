// Package objectstore 定义对象存储端口与多后端实现（V4.0 §12.4/12.5，ADR-014）。
//
// 业务代码只依赖 ObjectStore 接口与 ObjectKey，不直接引用任何云厂商 SDK。
// 键结构强制包含租户与项目前缀：{tenant_id}/{project_id}/{revision|artifact}/{type}/{id}.{ext}
// 预签名链接的生成必须校验调用方租户上下文与键前缀一致，越权前缀直接拒绝签发。
//
// 实现：local（本地文件系统，单机开发/测试）；s3 子包（S3 兼容多后端，G1-9）。
package objectstore

import (
	"context"
	"errors"
	"io"
	"time"
)

// Operation 限定签名链接允许的操作，避免用一个 token 同时读写。
type Operation string

const (
	OpRead   Operation = "read"
	OpWrite  Operation = "write"
	OpDelete Operation = "delete"
)

// ObjectMeta 描述对象的只读元数据。业务方不据此做授权决策。
type ObjectMeta struct {
	ContentType string
	Size        int64
	ContentHash string // sha256 hex，用于校验与内容去重
	ETag        string
}

// StorageClass 对应各后端存储类别（标准/低频/归档）；本地适配器忽略。
type StorageClass string

const (
	StorageClassStandard   StorageClass = "STANDARD"
	StorageClassInfrequent StorageClass = "INFREQUENT_ACCESS"
)

// LifecyclePolicy 描述对象的生命周期分层策略（V4.0 §12.5）。
// 由各后端适配器翻译为原生生命周期规则；本地适配器不支持时返回 ErrOperationNotSupported。
type LifecyclePolicy struct {
	Transitions []LifecycleTransition
	Expiration  *LifecycleExpiration
}

// LifecycleTransition 对象在 N 天后转入指定存储类别。
type LifecycleTransition struct {
	AfterDays int
	To        StorageClass
}

// LifecycleExpiration 对象在 N 天后过期删除。
type LifecycleExpiration struct {
	AfterDays int
}

// ObjectStore 是对象存储端口（V4.0 §12.4）。实现必须保证：
//   - Put/Delete 按 ObjectKey 完整语义执行，键内的租户/项目前缀由调用方保证合法；
//   - SignedURL 生成的链接只能操作指定 key 与 Operation，且受 ttl 限制；
//   - 未支持的能力显式返回错误，不得输出伪成功。
type ObjectStore interface {
	Put(ctx context.Context, key ObjectKey, r io.Reader, meta ObjectMeta) error
	Get(ctx context.Context, key ObjectKey) (io.ReadCloser, ObjectMeta, error)
	SignedURL(ctx context.Context, key ObjectKey, op Operation, ttl time.Duration) (string, error)
	Delete(ctx context.Context, key ObjectKey) error
	ApplyLifecyclePolicy(ctx context.Context, bucket string, policy LifecyclePolicy) error
}

// 错误哨兵：稳定错误语义，供上层分类处理。
var (
	// ErrKeyInvalid 表示 ObjectKey 不合法（空段、非法字符、路径穿越等）。
	ErrKeyInvalid = errors.New("objectstore: invalid object key")
	// ErrTenantMismatch 表示键的租户前缀与授权租户不一致。
	ErrTenantMismatch = errors.New("objectstore: key tenant prefix does not match authorized tenant")
	// ErrSignatureInvalid 表示签名校验失败（篡改、过期、操作/键不匹配）。
	ErrSignatureInvalid = errors.New("objectstore: invalid signature")
	// ErrOperationNotSupported 表示后端不支持该操作（如本地适配器的生命周期策略）。
	ErrOperationNotSupported = errors.New("objectstore: operation not supported by backend")
	// ErrObjectNotFound 表示对象不存在。
	ErrObjectNotFound = errors.New("objectstore: object not found")
	// ErrTokenExpired 表示签名令牌已过期。
	ErrTokenExpired = errors.New("objectstore: token expired")
)
