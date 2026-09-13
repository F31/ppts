// Package audit 归档器：把到期审计事件以 JSONL 写入对象存储后再删除（G3-4 保留/归档）。
package audit

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

// archiveBatch 分批大小；防止单租户一次读入过多事件。
const archiveBatch = 500

// TenantLister 是控制面租户列表能力（tenants 不在 RLS 保护范围）。
type TenantLister interface {
	ListTenants(ctx context.Context) ([]string, error)
}

// Archiver 把指定 cutOff 之前的审计事件归档到对象存储后删除（先写全再清）。
//
// 归档链路：逐租户分批 List(Before, limit) → JSONL 写入对象存储 → 全部批次写完后
// DeleteBefore(cutOff)。中途失败不删除任何事件（数据只增不丢），单租户失败不阻断其他租户。
type Archiver struct {
	store   Store
	tenants TenantLister
	objects objectstore.ObjectStore
	logger  *log.Logger
}

// NewArchiver 创建归档器。
func NewArchiver(store Store, tenants TenantLister, objects objectstore.ObjectStore, logger *log.Logger) *Archiver {
	return &Archiver{store: store, tenants: tenants, objects: objects, logger: logger}
}

// ArchiveBefore 对所有租户执行一次到期审计归档。
func (a *Archiver) ArchiveBefore(ctx context.Context, cutoff time.Time) error {
	tenants, err := a.tenants.ListTenants(ctx)
	if err != nil {
		return err
	}
	for _, tenantID := range tenants {
		if err := a.archiveTenant(ctx, tenantID, cutoff); err != nil {
			if a.logger != nil {
				a.logger.Printf("audit: archive tenant %s failed: %v", tenantID, err)
			}
		}
	}
	return nil
}

func (a *Archiver) archiveTenant(ctx context.Context, tenantID string, cutoff time.Time) error {
	var archived int
	for {
		events, err := a.store.List(ctx, tenantID, Filter{Before: cutoff, Limit: archiveBatch})
		if err != nil {
			return err
		}
		if len(events) == 0 {
			break
		}
		if err := a.writeBatch(ctx, tenantID, events); err != nil {
			return err
		}
		archived += len(events)
		if len(events) < archiveBatch {
			break
		}
	}
	if archived == 0 {
		return nil
	}
	deleted, err := a.store.DeleteBefore(ctx, tenantID, cutoff)
	if err != nil {
		return err
	}
	if a.logger != nil {
		a.logger.Printf("audit: archived %d and deleted %d events for tenant %s before %s", archived, deleted, tenantID, cutoff.Format(time.RFC3339))
	}
	return nil
}

func (a *Archiver) writeBatch(ctx context.Context, tenantID string, events []Event) error {
	var buf bytes.Buffer
	for _, e := range events {
		meta := e.Metadata
		if meta == nil {
			meta = map[string]any{}
		}
		line, err := json.Marshal(map[string]any{
			"id": e.ID, "actor_user": e.ActorUser, "action": e.Action,
			"resource_type": e.ResourceType, "resource_id": e.ResourceID,
			"metadata": meta, "created_at": e.CreatedAt.Format(time.RFC3339Nano),
		})
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	key := objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: "audit", Revision: "archive",
		AssetType: "audit", AssetID: randomArchiveID(), Ext: "jsonl",
	}
	return a.objects.Put(ctx, key, strings.NewReader(buf.String()), objectstore.ObjectMeta{
		ContentType: "application/x-ndjson",
		Size:        int64(buf.Len()),
	})
}

// randomArchiveID 生成不易冲突的资产 ID，避免归档对象互相覆盖。
func randomArchiveID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
