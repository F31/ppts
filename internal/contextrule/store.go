package contextrule

import (
	"context"
	"errors"
)

// ErrNotFound 规则不存在。
var ErrNotFound = errors.New("contextual rule not found")

// Store 管理上下文替换规则的持久化（tenant 隔离，应用层显式过滤）。
type Store interface {
	ListByTenant(ctx context.Context, tenantID string) ([]*Record, error)
	Create(ctx context.Context, rec *Record) error
	Update(ctx context.Context, rec *Record) error
	Delete(ctx context.Context, tenantID, id string) error
	// LoadAllEffective 加载某租户生效的全部规则（租户行 + 平台默认行），
	// 按 priority 升序（与 textnorm 规则执行顺序一致）。
	// 供 textnorm 引擎装配（对标 pronunciation 的 LoadTenantDefault+LoadPlatformDefault 合并）。
	LoadAllEffective(ctx context.Context, tenantID string) ([]*Record, error)
}
