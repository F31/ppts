package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/api"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/objectstore/storefactory"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/pricing"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/pronunciation"
	"github.com/F31/ppts/internal/tenant"
	"github.com/F31/ppts/internal/upload"
	"github.com/F31/ppts/internal/usage"
	"github.com/F31/ppts/migrations"
)

// tenantBundle 汇总租户存储对外暴露的能力（策略/存储用量/归档/生命周期/状态/对象清单/
// 存储后端解析）。PostgreSQL 与 SQLite 实现均满足。
type tenantBundle interface {
	api.TenantPolicyReader
	api.TenantStorageReader
	api.TenantArchiveReader
	api.TenantLifecycle
	api.TenantStatusChecker
	objectstore.InventoryRecorder
	objectstore.BackendResolver
	objectstore.EnvelopeEncryptionResolver
	ListLifecyclePolicies(ctx context.Context) ([]tenant.LifecyclePolicySetting, error)
}

// usageBundle 汇总配额与用量能力（预占/结算/释放 + 汇总 + 币种）。
type usageBundle interface {
	usage.Store
	UsageSummary(ctx context.Context, tenantID, month string) (float64, float64, float64, error)
	ProjectUsage(ctx context.Context, tenantID, projectID string) (usage.ProjectUsage, error)
	Currency() string
}

// jobStore 汇总任务存储：pipeline.Store（worker）+ api.JobStore（API RPC）+ 调度监控方法。
type jobStore interface {
	pipeline.Store
	api.JobStore
	OldestQueuedAge(ctx context.Context) (time.Duration, error)
	CountActive(ctx context.Context, tenantID string) (int, error)
}

// storeSet 是驱动无关的 store 组合，供 server 与 worker 复用（方案 B 组合根）。
// pg 与 sqldb 二者至多一个非空，由 cfg.Driver 决定。
type storeSet struct {
	cfg   db.Config
	pg    *pgxpool.Pool
	sqldb *sql.DB

	projects      project.ProjectStore
	uploads       upload.Store
	scripts       narration.Store
	jobs          jobStore
	claimer       pipeline.Claimer
	artifacts     artifact.Store
	members       membership.Store
	audit         audit.Store
	tenant        tenantBundle
	usage         usageBundle
	pronunciation pronunciation.Store

	objects objectstore.ObjectStore
}

func (s *storeSet) isSQLite() bool { return s.cfg.Driver == db.DriverSQLite }

func (s *storeSet) close() {
	if s.pg != nil {
		s.pg.Close()
	}
	if s.sqldb != nil {
		_ = s.sqldb.Close()
	}
}

// openStores 按 PPTS_DB_DRIVER 打开数据库并构建全部 store。
// SQLite 模式自动应用迁移并播种本地租户/用户（幂等），实现开箱即用。
func openStores(ctx context.Context) (*storeSet, error) {
	cfg, err := db.FromEnv()
	if err != nil {
		return nil, err
	}
	set := &storeSet{cfg: cfg}

	switch cfg.Driver {
	case db.DriverPostgres:
		pool, err := pgxpool.New(ctx, cfg.DSN)
		if err != nil {
			return nil, err
		}
		if err := pool.Ping(ctx); err != nil {
			pool.Close()
			return nil, err
		}
		jobs, err := pipeline.NewPGStore(ctx, cfg.DSN)
		if err != nil {
			pool.Close()
			return nil, err
		}
		set.pg = pool
		set.jobs = jobs
		set.claimer = jobs
		set.projects = project.NewPGProjectStore(pool)
		set.uploads = upload.NewPGUploadStore(pool)
		set.scripts = narration.NewPGStore(pool)
		set.artifacts = artifact.NewPGStore(pool)
		set.members = membership.NewPGStore(pool)
		set.audit = audit.NewPGStore(pool)
		set.tenant = tenant.NewPGStore(pool)
		set.usage = usage.NewPGStore(pool)
		set.pronunciation = pronunciation.NewPGStore(pool)

	case db.DriverSQLite:
		sqldb, err := db.OpenSQLite(ctx, cfg.DSN)
		if err != nil {
			return nil, err
		}
		fsys, err := migrations.SQLite()
		if err != nil {
			_ = sqldb.Close()
			return nil, err
		}
		if _, err := db.MigrateSQLite(ctx, sqldb, fsys); err != nil {
			_ = sqldb.Close()
			return nil, err
		}
		if err := db.EnsureLocalIdentity(ctx, sqldb); err != nil {
			_ = sqldb.Close()
			return nil, err
		}
		jobs := pipeline.NewSQLiteStore(sqldb)
		set.sqldb = sqldb
		set.jobs = jobs
		set.claimer = jobs
		set.projects = project.NewSQLiteStore(sqldb)
		set.uploads = upload.NewSQLiteStore(sqldb)
		set.scripts = narration.NewSQLiteStore(sqldb)
		set.artifacts = artifact.NewSQLiteStore(sqldb)
		set.members = membership.NewSQLiteStore(sqldb)
		set.audit = audit.NewSQLiteStore(sqldb)
		set.tenant = tenant.NewSQLiteStore(sqldb)
		set.usage = usage.NewSQLiteStore(sqldb)
		set.pronunciation = pronunciation.NewSQLiteStore(sqldb)

	default:
		return nil, fmt.Errorf("db: unsupported driver %q", cfg.Driver)
	}

	// 对象存储注册 + 对象清单（local 后端开箱即用；S3 需相应环境变量）。
	registry, err := storefactory.FromEnv(set.tenant)
	if err != nil {
		set.close()
		return nil, err
	}
	objects := objectstore.WithInventory(registry, set.tenant)
	if objects, err = storefactory.WithEnvelopeEncryptionFromEnv(objects, set.tenant); err != nil {
		set.close()
		return nil, err
	}
	set.objects = objects
	return set, nil
}

// gatewayStore 按驱动构建网关存储（管理面 + 运行时解析）。
func (s *storeSet) gatewayStore(cipher gateway.CredentialCipher) gateway.StoreResolver {
	if s.pg != nil {
		return gateway.NewPGStore(s.pg, cipher)
	}
	return gateway.NewSQLiteStore(s.sqldb, cipher)
}

// localPrincipalFor 在 SQLite 单租户模式下返回固定本地身份；PostgreSQL 返回 nil。
func (s *storeSet) localPrincipalFor() *api.Principal {
	if !s.isSQLite() {
		return nil
	}
	return &api.Principal{TenantID: db.LocalTenantID, UserID: db.LocalUserID}
}

func (s *storeSet) scriptSourceStore() app.ScriptSourceStore {
	if s.pg != nil {
		return app.NewScriptSourceStore(s.pg)
	}
	if s.sqldb != nil {
		return app.NewSQLiteScriptSourceStore(s.sqldb)
	}
	return nil
}

func (s *storeSet) voiceSettingsStore() app.VoiceSettingsStore {
	if s.pg != nil {
		return app.NewVoiceSettingsStore(s.pg)
	}
	if s.sqldb != nil {
		return app.NewSQLiteVoiceSettingsStore(s.sqldb)
	}
	return nil
}

// defaultWorkerTenant 返回 worker 的租户上下文：SQLite 单租户下为本地租户；
// PostgreSQL 下沿用 PPTS_TENANT_ID（空=跨租户模式）。
func (s *storeSet) defaultWorkerTenant(envTenant string) string {
	if s.isSQLite() {
		return db.LocalTenantID
	}
	return envTenant
}

// applyPriceBook 把定价表注入用量存储（驱动无关）。未配置定价表时金额记 0。
func (s *storeSet) applyPriceBook(b *pricing.Book) {
	switch v := s.usage.(type) {
	case *usage.PGStore:
		v.WithPriceBook(b)
	case *usage.SQLiteStore:
		v.WithPriceBook(b)
	}
}
