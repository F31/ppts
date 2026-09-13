package tenant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

// businessTables 是所有受 RLS 保护的租户业务表。删除顺序满足外键依赖
// （子表先删）；导出顺序无依赖，固定稳定便于检索。tenants 为控制面注册表不在此列。
var businessTables = []string{
	"job_steps",
	"uploads",
	"source_revisions",
	"artifacts",
	"jobs",
	"narration_segments",
	"narration_scripts",
	"projects",
	"tenant_members",
	"tenant_quotas",
	"quota_reservations",
	"usage_ledger",
	"audit_events",
}

// ExportFile 描述单个表的导出结果。
type ExportFile struct {
	Table     string
	ObjectKey string
	Rows      int64
}

// ExportManifest 是租户数据导出的汇总。
type ExportManifest struct {
	Files       []ExportFile
	TenantID    string
	ManifestKey string
	ExportedAt  time.Time
}

// objectTables 是持有对象键的租户表，数据擦除前三收集后做存储 GC。
var objectTables = []struct {
	table string
	col   string
}{
	{"uploads", "object_key"},
	{"source_revisions", "object_key"},
	{"artifacts", "object_key"},
}

// ExportTenant 把租户全部业务数据按表导出为对象存储 JSONL 并返回清单。
// 读取在各表租户上下文（RLS）内执行；不删除任何数据。
func (s *PGStore) ExportTenant(ctx context.Context, tenantID string, objects objectstore.ObjectStore) (*ExportManifest, error) {
	if _, err := s.Status(ctx, tenantID); err != nil {
		return nil, err
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	m := &ExportManifest{TenantID: tenantID, ExportedAt: time.Now()}
	for _, table := range businessTables {
		rows, err := s.dumpTable(ctx, tenantID, table)
		if err != nil {
			return nil, fmt.Errorf("tenant export %s: %w", table, err)
		}
		if len(rows) == 0 {
			continue
		}
		key := objectstore.ObjectKey{
			TenantID: tenantID, ProjectID: "export", Revision: "archive",
			AssetType: "tenant", AssetID: stamp + "-" + table, Ext: "jsonl",
		}
		if err := objects.Put(ctx, key, strings.NewReader(strings.Join(rows, "\n")+"\n"), objectstore.ObjectMeta{ContentType: "application/x-ndjson"}); err != nil {
			return nil, fmt.Errorf("tenant export put %s: %w", table, err)
		}
		m.Files = append(m.Files, ExportFile{Table: table, ObjectKey: key.String(), Rows: int64(len(rows))})
	}
	// 写入 manifest（JSON）。

	manifestKey := objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: "export", Revision: "archive",
		AssetType: "tenant", AssetID: stamp + "-manifest", Ext: "json",
	}
	raw, _ := json.MarshalIndent(m, "", "  ")
	if err := objects.Put(ctx, manifestKey, bytes.NewReader(raw), objectstore.ObjectMeta{ContentType: "application/json", Size: int64(len(raw))}); err != nil {
		return nil, fmt.Errorf("tenant export manifest: %w", err)
	}
	m.ManifestKey = manifestKey.String()
	return m, nil
}

// PurgeTenant 数据擦除：先对租户对象键做存储 GC，再在 RLS 上下文内删除全部业务行，
// 最后把控制面 tenants 状态置为 deleted。失败不产生部分终态承诺。
func (s *PGStore) PurgeTenant(ctx context.Context, tenantID string, objects objectstore.ObjectStore) (int64, error) {
	if _, err := s.Status(ctx, tenantID); err != nil {
		return 0, err
	}
	// 1) 收集并删除对象（行保留时先读键）。
	var keys []string
	for _, ot := range objectTables {
		rows, err := s.collectObjectKeys(ctx, tenantID, ot.table, ot.col)
		if err != nil {
			return 0, fmt.Errorf("tenant purge collect %s: %w", ot.table, err)
		}
		keys = append(keys, rows...)
	}
	for _, raw := range keys {
		key, err := objectstore.Parse(raw)
		if err != nil {
			continue
		}
		if err := objects.Delete(ctx, key); err != nil && err != objectstore.ErrObjectNotFound {
			return 0, fmt.Errorf("tenant purge object %s: %w", raw, err)
		}
	}
	// 2) RLS 上下文内删除业务行（依赖顺序）。
	var deleted int64
	for _, table := range businessTables {
		n, err := s.deleteTenantRows(ctx, tenantID, table)
		if err != nil {
			return 0, fmt.Errorf("tenant purge %s: %w", table, err)
		}
		deleted += n
	}
	// 3) 控制面置为 deleted。
	if err := s.markPurged(ctx, tenantID); err != nil {
		return 0, err
	}
	return deleted, nil
}

func (s *PGStore) dumpTable(ctx context.Context, tenantID, table string) ([]string, error) {
	var lines []string
	err := s.run(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		// 表格无统一 created_at（如 job_steps），导出不排序，一次全量读取。
		rows, err := tx.Query(ctx,
			fmt.Sprintf("SELECT to_jsonb(t) FROM %s t WHERE t.tenant_id=$1", table), tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			lines = append(lines, string(raw))
		}
		return rows.Err()
	})
	return lines, err
}

func (s *PGStore) collectObjectKeys(ctx context.Context, tenantID, table, col string) ([]string, error) {
	var keys []string
	err := s.run(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			fmt.Sprintf("SELECT %s FROM %s WHERE tenant_id=$1", col, table), tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return err
			}
			if k != "" {
				keys = append(keys, k)
			}
		}
		return rows.Err()
	})
	return keys, err
}

func (s *PGStore) deleteTenantRows(ctx context.Context, tenantID, table string) (int64, error) {
	var n int64
	err := s.run(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE tenant_id=$1", table), tenantID)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		return nil
	})
	return n, err
}

func (s *PGStore) markPurged(ctx context.Context, tenantID string) error {
	tag, err := s.pool.Exec(ctx,
		"UPDATE tenants SET status='deleted', suspended_at=coalesce(suspended_at, now()), updated_at=now() WHERE id=$1", tenantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTenantNotFound
	}
	return nil
}

// 内部小助手：让 export/purge 复用租户事务上下文。
func (s *PGStore) run(ctx context.Context, tenantID string, fn func(context.Context, pgx.Tx) error) error {
	return Run(ctx, s.pool, tenantID, fn)
}
