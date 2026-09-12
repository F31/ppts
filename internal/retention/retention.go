// Package retention 执行数据保留策略：源文件到期/按需删除与临时对象清理（V4.0 §13.1/§12.5）。
//
// 调度器按控制面 tenants 列表逐租户执行，所有租户数据访问在租户上下文（RLS）内完成。
package retention

import (
	"context"
	"log"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

// SourceToDelete 是待删除源对象的不可变版本（行保留，仅删除对象）。
type SourceToDelete struct {
	RevisionID string
	ObjectKey  string
}

// OrphanUpload 是超时仍处于 pending 的上传会话。
type OrphanUpload struct {
	UploadID  string
	ObjectKey string
}

// Store 是保留策略所需的存储能力。
type Store interface {
	// ListTenants 返回控制面租户 ID（tenants 表不受 RLS 约束）。
	ListTenants(ctx context.Context) ([]string, error)
	// PendingUploadsBefore 返回指定租户在 cutoff 之前创建、仍 pending 的上传会话。
	PendingUploadsBefore(ctx context.Context, tenantID string, cutoff time.Time) ([]OrphanUpload, error)
	// AbortUpload 将 pending 会话置为 aborted（清理幂等）。
	AbortUpload(ctx context.Context, tenantID, uploadID string) error
	// SourcesToDelete 返回应删除源对象的版本：超过项目保留期，或已选择"处理后删除"且解析成功。
	SourcesToDelete(ctx context.Context, tenantID string, now time.Time) ([]SourceToDelete, error)
	// MarkSourceDeleted 标记源对象已删除。
	MarkSourceDeleted(ctx context.Context, tenantID, revisionID string) error
}

// Sweeper 周期性执行保留与清理。
type Sweeper struct {
	store      Store
	objects    objectstore.ObjectStore
	abandonTTL time.Duration
	logger     *log.Logger
}

// NewSweeper 创建清理器。abandonTTL 为上传会话滞留多久后视为孤儿。
func NewSweeper(store Store, objects objectstore.ObjectStore, abandonTTL time.Duration, logger *log.Logger) *Sweeper {
	return &Sweeper{store: store, objects: objects, abandonTTL: abandonTTL, logger: logger}
}

// Sweep 对所有租户执行一次清理；单个租户失败不阻断其他租户。
func (s *Sweeper) Sweep(ctx context.Context) error {
	tenants, err := s.store.ListTenants(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	cutoff := now.Add(-s.abandonTTL)
	for _, tenantID := range tenants {
		if err := s.sweepTenant(ctx, tenantID, cutoff, now); err != nil {
			if s.logger != nil {
				s.logger.Printf("retention: tenant %s sweep failed: %v", tenantID, err)
			}
		}
	}
	return nil
}

func (s *Sweeper) sweepTenant(ctx context.Context, tenantID string, cutoff, now time.Time) error {
	orphans, err := s.store.PendingUploadsBefore(ctx, tenantID, cutoff)
	if err != nil {
		return err
	}
	for _, o := range orphans {
		if err := s.deleteObject(ctx, o.ObjectKey); err != nil {
			return err
		}
		if err := s.store.AbortUpload(ctx, tenantID, o.UploadID); err != nil {
			return err
		}
		if s.logger != nil {
			s.logger.Printf("retention: aborted orphan upload %s (tenant %s)", o.UploadID, tenantID)
		}
	}

	sources, err := s.store.SourcesToDelete(ctx, tenantID, now)
	if err != nil {
		return err
	}
	for _, src := range sources {
		if err := s.deleteObject(ctx, src.ObjectKey); err != nil {
			return err
		}
		if err := s.store.MarkSourceDeleted(ctx, tenantID, src.RevisionID); err != nil {
			return err
		}
		if s.logger != nil {
			s.logger.Printf("retention: deleted source object for revision %s (tenant %s)", src.RevisionID, tenantID)
		}
	}
	return nil
}

// deleteObject 删除对象；对象已不存在视为成功（幂等）。
func (s *Sweeper) deleteObject(ctx context.Context, rawKey string) error {
	key, err := objectstore.Parse(rawKey)
	if err != nil {
		return err
	}
	if err := s.objects.Delete(ctx, key); err != nil && err != objectstore.ErrObjectNotFound {
		return err
	}
	return nil
}
