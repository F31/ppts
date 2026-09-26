// Package retention 执行数据保留策略：源文件到期/按需删除与临时对象清理（V4.0 §13.1/§12.5）。
//
// 调度器按控制面 tenants 列表逐租户执行，所有租户数据访问在租户上下文（RLS）内完成。
package retention

import (
	"context"
	"log"
	"time"

	"github.com/F31/ppts/internal/audit"
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

// StaleReservation 是超过 TTL 仍未结算/释放的额度预占。
type StaleReservation struct {
	ReservationID      string
	LogicalOperationID string
	UsageKind          string
}

// DerivedToDelete 是超过租户派生产物保留分档的派生产物对象（artifact/audio/render）。
type DerivedToDelete struct {
	ObjectKey string
	AssetType string
	AssetID   string
}

// OrphanToDelete 是清单里仍有记录、但库内已无引用的对象。
type OrphanToDelete struct {
	ObjectKey string
	AssetType string
	AssetID   string
}

// Store 是保留策略所需的存储能力。
type Store interface {
	// ListTenants 返回控制面租户 ID（tenants 表不受 RLS 约束）。
	ListTenants(ctx context.Context) ([]string, error)
	// PendingUploadsBefore 返回指定租户在 cutoff 之前创建、仍 pending 的上传会话。
	PendingUploadsBefore(ctx context.Context, tenantID string, cutoff time.Time) ([]OrphanUpload, error)
	// AbortUpload 将 pending 会话置为 aborted（清理幂等）。
	AbortUpload(ctx context.Context, tenantID, uploadID string) error
	// SourcesToDelete 返回应删除源对象的版本：超过项目保留期（或租户级默认保留期），或已选择"处理后删除"且解析成功。
	SourcesToDelete(ctx context.Context, tenantID string, now time.Time) ([]SourceToDelete, error)
	// MarkSourceDeleted 标记源对象已删除。
	MarkSourceDeleted(ctx context.Context, tenantID, revisionID string) error
	// DerivedToDelete 返回超过租户派生产物保留分档的对象清单（分档见 assets.go 注册表）。
	DerivedToDelete(ctx context.Context, tenantID string, now time.Time) ([]DerivedToDelete, error)
	// OrphansToDelete 返回 cutoff 之前写入清单、且归属表已无引用的对象。
	OrphansToDelete(ctx context.Context, tenantID string, cutoff time.Time) ([]OrphanToDelete, error)
	// DeleteDerivedRecord 删除派生对象清单记录；artifact 同时删除 artifacts 表对应行。
	DeleteDerivedRecord(ctx context.Context, tenantID, objectKey, assetType string) error
	// StaleReservationsBefore 返回 cutoff 前仍 reserved 的额度预占。
	StaleReservationsBefore(ctx context.Context, tenantID string, cutoff time.Time) ([]StaleReservation, error)
	// ReleaseReservationByID 释放指定预占并回退 reserved_units。
	ReleaseReservationByID(ctx context.Context, tenantID, reservationID string) error
}

// Sweeper 周期性执行保留与清理。
type Sweeper struct {
	store        Store
	objects      objectstore.ObjectStore
	abandonTTL   time.Duration
	quotaTTL     time.Duration
	orphanGrace  time.Duration
	orphanDelete bool
	logger       *log.Logger
	auditor      audit.Recorder
}

// NewSweeper 创建清理器。abandonTTL 为上传会话滞留多久后视为孤儿。
func NewSweeper(store Store, objects objectstore.ObjectStore, abandonTTL time.Duration, logger *log.Logger) *Sweeper {
	return &Sweeper{store: store, objects: objects, abandonTTL: abandonTTL, logger: logger}
}

// WithQuotaReservationTTL 启用超过 ttl 的 reserved 额度预占自动释放；ttl<=0 表示关闭。
func (s *Sweeper) WithQuotaReservationTTL(ttl time.Duration) *Sweeper {
	s.quotaTTL = ttl
	return s
}

// WithOrphanScan 启用孤儿扫描；grace<=0 表示关闭。
//
// grace 是静默期：清单写入后这么久之内不判定为孤儿。它是正确性要求而非调优项——
// 写对象、落清单、提交业务行不是原子的，没有静默期就会把在途产物判成无主。
//
// deleteEnabled 默认应为 false：**删对象不可逆，而「无引用」本质上是启发式判定**
// （判据是归属表里查不到引用行，漏认一种引用关系就会删掉在用对象）。
// 故默认只写审计事件 orphan.detected，由人确认后再开删除。
func (s *Sweeper) WithOrphanScan(grace time.Duration, deleteEnabled bool) *Sweeper {
	s.orphanGrace = grace
	s.orphanDelete = deleteEnabled
	return s
}

// WithAuditor 记录删除类操作审计（G3-4）。
func (s *Sweeper) WithAuditor(auditor audit.Recorder) *Sweeper {
	s.auditor = auditor
	return s
}

// Sweep 对所有租户执行一次清理；单个租户失败不阻断其他租户。
func (s *Sweeper) Sweep(ctx context.Context) error {
	tenants, err := s.store.ListTenants(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	cutoff := now.Add(-s.abandonTTL)
	quotaCutoff := time.Time{}
	if s.quotaTTL > 0 {
		quotaCutoff = now.Add(-s.quotaTTL)
	}
	orphanCutoff := time.Time{}
	if s.orphanGrace > 0 {
		orphanCutoff = now.Add(-s.orphanGrace)
	}
	for _, tenantID := range tenants {
		if err := s.sweepTenant(ctx, tenantID, cutoff, quotaCutoff, orphanCutoff, now); err != nil {
			if s.logger != nil {
				s.logger.Printf("retention: tenant %s sweep failed: %v", tenantID, err)
			}
		}
	}
	return nil
}

func (s *Sweeper) sweepTenant(ctx context.Context, tenantID string, cutoff, quotaCutoff, orphanCutoff, now time.Time) error {
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
		s.record(ctx, tenantID, "upload.abort", "upload", o.UploadID, nil)
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
		s.record(ctx, tenantID, "source.delete", "source_revision", src.RevisionID, map[string]any{"object_key": src.ObjectKey})
	}

	derived, err := s.store.DerivedToDelete(ctx, tenantID, now)
	if err != nil {
		return err
	}
	for _, d := range derived {
		if err := s.deleteObject(ctx, d.ObjectKey); err != nil {
			return err
		}
		if err := s.store.DeleteDerivedRecord(ctx, tenantID, d.ObjectKey, d.AssetType); err != nil {
			return err
		}
		if s.logger != nil {
			s.logger.Printf("retention: deleted derived %s object %s (tenant %s)", d.AssetType, d.ObjectKey, tenantID)
		}
		s.record(ctx, tenantID, "derived.delete", d.AssetType, d.AssetID, map[string]any{"object_key": d.ObjectKey})
	}

	if !orphanCutoff.IsZero() {
		orphans, err := s.store.OrphansToDelete(ctx, tenantID, orphanCutoff)
		if err != nil {
			return err
		}
		for _, o := range orphans {
			if !s.orphanDelete {
				if s.logger != nil {
					s.logger.Printf("retention: orphan candidate (report-only) %s type=%s (tenant %s)", o.ObjectKey, o.AssetType, tenantID)
				}
				s.record(ctx, tenantID, "orphan.detected", "object", o.ObjectKey,
					map[string]any{"asset_type": o.AssetType, "deleted": false})
				continue
			}
			if err := s.deleteObject(ctx, o.ObjectKey); err != nil {
				return err
			}
			if err := s.store.DeleteDerivedRecord(ctx, tenantID, o.ObjectKey, o.AssetType); err != nil {
				return err
			}
			if s.logger != nil {
				s.logger.Printf("retention: deleted orphan %s type=%s (tenant %s)", o.ObjectKey, o.AssetType, tenantID)
			}
			s.record(ctx, tenantID, "orphan.delete", "object", o.ObjectKey,
				map[string]any{"asset_type": o.AssetType, "deleted": true})
		}
	}

	if !quotaCutoff.IsZero() {
		stale, err := s.store.StaleReservationsBefore(ctx, tenantID, quotaCutoff)
		if err != nil {
			return err
		}
		for _, r := range stale {
			if err := s.store.ReleaseReservationByID(ctx, tenantID, r.ReservationID); err != nil {
				return err
			}
			if s.logger != nil {
				s.logger.Printf("retention: released stale reservation %s (tenant %s, operation %s, kind %s)", r.ReservationID, tenantID, r.LogicalOperationID, r.UsageKind)
			}
			s.record(ctx, tenantID, "quota.reservation_release", "quota_reservation", r.ReservationID,
				map[string]any{"logical_operation_id": r.LogicalOperationID, "usage_kind": r.UsageKind})
		}
	}
	return nil
}

// record 追加审计事件；审计失败不阻断清理主流程。
func (s *Sweeper) record(ctx context.Context, tenantID, action, resourceType, resourceID string, metadata map[string]any) {
	if s.auditor == nil {
		return
	}
	if err := s.auditor.Record(ctx, audit.Event{
		TenantID: tenantID, ActorUser: "system", Action: action,
		ResourceType: resourceType, ResourceID: resourceID, Metadata: metadata,
	}); err != nil {
		if s.logger != nil {
			s.logger.Printf("retention: audit %s %s: %v", action, resourceID, err)
		}
	}
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
