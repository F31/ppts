package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/F31/ppts/internal/db"
)

// sqColumnsSQL 是 SQLite 的列清单：时间以 unix 秒返回，布尔以 INTEGER 返回。
const sqColumnsSQL = `tenant_id, name, kind, provider, base_url, encrypted_creds,
	model, vision_model, voice, sample_rate, is_default, enabled, version,
	CAST(strftime('%s', created_at) AS INTEGER), CAST(strftime('%s', updated_at) AS INTEGER)`

// SQLiteStore 是 Store 与 Resolver 的 SQLite 实现（单租户精简 profile）。
// 复用同包的 rawRow/toGateway/cacheKey/encodeCreds/decodeCreds。
type SQLiteStore struct {
	db     *sql.DB
	cipher CredentialCipher
	ttl    time.Duration

	mu    sync.Mutex
	cache map[cacheKey]cacheEntry
}

// NewSQLiteStore 创建存储。cipher 必填（凭据加密/解密依赖）。
func NewSQLiteStore(sqldb *sql.DB, cipher CredentialCipher) *SQLiteStore {
	return &SQLiteStore{db: sqldb, cipher: cipher, ttl: 30 * time.Second, cache: map[cacheKey]cacheEntry{}}
}

var _ Store = (*SQLiteStore)(nil)
var _ Resolver = (*SQLiteStore)(nil)

// SetCacheTTL 调整解析缓存 TTL（测试用）。
func (s *SQLiteStore) SetCacheTTL(d time.Duration) {
	if d > 0 {
		s.ttl = d
	}
}

// Invalidate 清除解析缓存（写操作后调用，跨进程靠 TTL 收敛）。
func (s *SQLiteStore) Invalidate(tenantID string, kind Kind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tenantID == PlatformTenantID {
		for k := range s.cache {
			if k.kind == kind {
				delete(s.cache, k)
			}
		}
		return
	}
	delete(s.cache, cacheKey{tenant: tenantID, kind: kind})
}

// sqScanRow 读取一行并转为 rawRow（布尔列转 bool）。
func sqScanRow(row rowScanner) (rawRow, error) {
	var r rawRow
	var isDefault, enabled int
	err := row.Scan(&r.tenantID, &r.name, &r.kind, &r.provider, &r.baseURL, &r.enc,
		&r.model, &r.visionModel, &r.voice, &r.sampleRate, &isDefault, &enabled,
		&r.version, &r.createdAt, &r.updatedAt)
	r.isDefault = isDefault != 0
	r.enabled = enabled != 0
	return r, err
}

// sqToGateway 把 rawRow 转为 Gateway（与 PGStore.toGateway 同逻辑，驱动无关）。
func sqToGateway(r rawRow, apiKey string) *Gateway {
	return &Gateway{
		TenantID: r.tenantID, Name: r.name, Kind: Kind(r.kind), Provider: r.provider,
		BaseURL: r.baseURL, Model: r.model, VisionModel: r.visionModel, Voice: r.voice,
		SampleRate: r.sampleRate, IsDefault: r.isDefault, Enabled: r.enabled,
		Version: r.version, HasKey: apiKey != "", KeyMasked: MaskKey(apiKey),
		CreatedAt: r.createdAt, UpdatedAt: r.updatedAt,
	}
}

type rowScanner interface{ Scan(dest ...any) error }

func (s *SQLiteStore) List(ctx context.Context, tenantID string, kind Kind) ([]*Gateway, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sqColumnsSQL+` FROM model_gateways
		 WHERE tenant_id = ? AND kind = ? ORDER BY is_default DESC, created_at`,
		tenantID, string(kind))
	if err != nil {
		return nil, fmt.Errorf("gateway list: %w", err)
	}
	defer rows.Close()
	var out []*Gateway
	for rows.Next() {
		r, err := sqScanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("gateway scan: %w", err)
		}
		key, _ := decodeCreds(s.cipher, r.tenantID, r.name, r.kind, r.enc)
		out = append(out, sqToGateway(r, key))
	}
	return out, rows.Err()
}

func (s *SQLiteStore) Get(ctx context.Context, tenantID, name string, kind Kind) (*Gateway, error) {
	r, err := sqScanRow(s.db.QueryRowContext(ctx,
		`SELECT `+sqColumnsSQL+` FROM model_gateways WHERE tenant_id = ? AND name = ? AND kind = ?`,
		tenantID, name, string(kind)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("gateway get: %w", err)
	}
	key, _ := decodeCreds(s.cipher, r.tenantID, r.name, r.kind, r.enc)
	return sqToGateway(r, key), nil
}

func (s *SQLiteStore) ResolveNamed(ctx context.Context, tenantID, name string, kind Kind) (*Config, error) {
	r, err := sqScanRow(s.db.QueryRowContext(ctx,
		`SELECT `+sqColumnsSQL+` FROM model_gateways WHERE tenant_id = ? AND name = ? AND kind = ?`,
		tenantID, name, string(kind)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("gateway resolve-named: %w", err)
	}
	apiKey, err := decodeCreds(s.cipher, r.tenantID, r.name, r.kind, r.enc)
	if err != nil {
		return nil, err
	}
	return &Config{
		Name: r.name, Kind: Kind(r.kind), BaseURL: r.baseURL, APIKey: apiKey,
		Model: r.model, VisionModel: r.visionModel, Voice: r.voice,
		SampleRate: r.sampleRate, Version: r.version,
	}, nil
}

func (s *SQLiteStore) Create(ctx context.Context, gw *Gateway, apiKey string) error {
	enc, err := encodeCreds(s.cipher, gw.TenantID, gw.Name, string(gw.Kind), apiKey)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO model_gateways
			(tenant_id, name, kind, provider, base_url, encrypted_creds, model, vision_model, voice,
			 sample_rate, is_default, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		gw.TenantID, gw.Name, string(gw.Kind), gw.Provider, gw.BaseURL, enc,
		gw.Model, gw.VisionModel, gw.Voice, gw.SampleRate, b2i(gw.IsDefault), b2i(gw.Enabled),
		dbNow(), dbNow()); err != nil {
		return fmt.Errorf("gateway create: %w", err)
	}
	s.Invalidate(gw.TenantID, gw.Kind)
	return nil
}

func (s *SQLiteStore) Update(ctx context.Context, gw *Gateway, apiKey string) error {
	var enc []byte
	if apiKey == "" {
		if err := s.db.QueryRowContext(ctx,
			`SELECT encrypted_creds FROM model_gateways WHERE tenant_id = ? AND name = ? AND kind = ?`,
			gw.TenantID, gw.Name, string(gw.Kind)).Scan(&enc); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("gateway update: %w", err)
		}
	} else {
		var err error
		enc, err = encodeCreds(s.cipher, gw.TenantID, gw.Name, string(gw.Kind), apiKey)
		if err != nil {
			return err
		}
	}
	version := gw.Version + 1
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("gateway update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if gw.IsDefault {
		if _, err := tx.ExecContext(ctx,
			`UPDATE model_gateways SET is_default = 0 WHERE tenant_id = ? AND kind = ? AND name <> ?`,
			gw.TenantID, string(gw.Kind), gw.Name); err != nil {
			return fmt.Errorf("gateway update: %w", err)
		}
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE model_gateways SET provider = ?, base_url = ?, encrypted_creds = ?, model = ?,
			vision_model = ?, voice = ?, sample_rate = ?, is_default = ?, enabled = ?,
			version = ?, updated_at = ?
		 WHERE tenant_id = ? AND name = ? AND kind = ?`,
		gw.Provider, gw.BaseURL, enc, gw.Model, gw.VisionModel, gw.Voice, gw.SampleRate,
		b2i(gw.IsDefault), b2i(gw.Enabled), version, dbNow(),
		gw.TenantID, gw.Name, string(gw.Kind))
	if err != nil {
		return fmt.Errorf("gateway update: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gateway update: %w", err)
	}
	gw.Version = version
	s.Invalidate(gw.TenantID, gw.Kind)
	return nil
}

// ChangeKind 把网关从 oldKind 迁移到 gw.Kind。主键含 kind，因此需在事务内插入新行、
// 删除旧行；apiKey 为空时用旧 kind 的 AAD 解密后按新 kind 重新加密。
func (s *SQLiteStore) ChangeKind(ctx context.Context, gw *Gateway, oldKind Kind, apiKey string) error {
	if oldKind == gw.Kind {
		return s.Update(ctx, gw, apiKey)
	}
	var oldEnc []byte
	if err := s.db.QueryRowContext(ctx,
		`SELECT encrypted_creds FROM model_gateways WHERE tenant_id = ? AND name = ? AND kind = ?`,
		gw.TenantID, gw.Name, string(oldKind)).Scan(&oldEnc); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("gateway change kind: %w", err)
	}
	if apiKey == "" {
		plain, err := decodeCreds(s.cipher, gw.TenantID, gw.Name, string(oldKind), oldEnc)
		if err != nil {
			return err
		}
		apiKey = plain
	}
	enc, err := encodeCreds(s.cipher, gw.TenantID, gw.Name, string(gw.Kind), apiKey)
	if err != nil {
		return err
	}
	version := gw.Version + 1
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("gateway change kind: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM model_gateways WHERE tenant_id = ? AND name = ? AND kind = ?`,
		gw.TenantID, gw.Name, string(gw.Kind)).Scan(&exists); err == nil {
		return ErrExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("gateway change kind: %w", err)
	}
	if gw.IsDefault {
		if _, err := tx.ExecContext(ctx,
			`UPDATE model_gateways SET is_default = 0 WHERE tenant_id = ? AND kind = ?`,
			gw.TenantID, string(gw.Kind)); err != nil {
			return fmt.Errorf("gateway change kind: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO model_gateways
			(tenant_id, name, kind, provider, base_url, encrypted_creds, model, vision_model, voice,
			 sample_rate, is_default, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		gw.TenantID, gw.Name, string(gw.Kind), gw.Provider, gw.BaseURL, enc,
		gw.Model, gw.VisionModel, gw.Voice, gw.SampleRate, b2i(gw.IsDefault), b2i(gw.Enabled),
		dbNow(), dbNow()); err != nil {
		return fmt.Errorf("gateway change kind: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`DELETE FROM model_gateways WHERE tenant_id = ? AND name = ? AND kind = ?`,
		gw.TenantID, gw.Name, string(oldKind))
	if err != nil {
		return fmt.Errorf("gateway change kind: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gateway change kind: %w", err)
	}
	gw.Version = version
	s.Invalidate(gw.TenantID, oldKind)
	s.Invalidate(gw.TenantID, gw.Kind)
	return nil
}

func (s *SQLiteStore) Delete(ctx context.Context, tenantID, name string, kind Kind) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM model_gateways WHERE tenant_id = ? AND name = ? AND kind = ?`,
		tenantID, name, string(kind))
	if err != nil {
		return fmt.Errorf("gateway delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.Invalidate(tenantID, kind)
	return nil
}

func (s *SQLiteStore) SetDefault(ctx context.Context, tenantID, name string, kind Kind) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("gateway set-default: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`UPDATE model_gateways SET is_default = 0 WHERE tenant_id = ? AND kind = ?`,
		tenantID, string(kind)); err != nil {
		return fmt.Errorf("gateway set-default: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE model_gateways SET is_default = 1 WHERE tenant_id = ? AND name = ? AND kind = ?`,
		tenantID, name, string(kind))
	if err != nil {
		return fmt.Errorf("gateway set-default: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gateway set-default: %w", err)
	}
	s.Invalidate(tenantID, kind)
	return nil
}

// Resolve 按 租户默认 → 平台默认 顺序解析；带 TTL 缓存（含未命中缓存）。
func (s *SQLiteStore) Resolve(ctx context.Context, tenantID string, kind Kind) (*Config, error) {
	key := cacheKey{tenant: tenantID, kind: kind}
	s.mu.Lock()
	if e, ok := s.cache[key]; ok && time.Since(e.at) < s.ttl {
		s.mu.Unlock()
		if !e.found {
			return nil, ErrNotFound
		}
		return e.cfg, nil
	}
	s.mu.Unlock()

	cfg, found, err := s.resolveUncached(ctx, tenantID, kind)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache[key] = cacheEntry{cfg: cfg, found: found, at: time.Now()}
	s.mu.Unlock()
	if !found {
		return nil, ErrNotFound
	}
	return cfg, nil
}

func (s *SQLiteStore) resolveUncached(ctx context.Context, tenantID string, kind Kind) (*Config, bool, error) {
	if cfg, found, err := s.lookupDefault(ctx, tenantID, kind); err != nil {
		return nil, false, err
	} else if found {
		return cfg, true, nil
	}
	if tenantID != PlatformTenantID {
		if cfg, found, err := s.lookupDefault(ctx, PlatformTenantID, kind); err != nil {
			return nil, false, err
		} else if found {
			return cfg, true, nil
		}
	}
	return nil, false, nil
}

func (s *SQLiteStore) lookupDefault(ctx context.Context, tenantID string, kind Kind) (*Config, bool, error) {
	r, err := sqScanRow(s.db.QueryRowContext(ctx,
		`SELECT `+sqColumnsSQL+` FROM model_gateways
		 WHERE tenant_id = ? AND kind = ? AND is_default = 1 AND enabled = 1 LIMIT 1`,
		tenantID, string(kind)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("gateway resolve: %w", err)
	}
	apiKey, err := decodeCreds(s.cipher, r.tenantID, r.name, r.kind, r.enc)
	if err != nil {
		return nil, false, err
	}
	return &Config{
		Name: r.name, Kind: Kind(r.kind), BaseURL: r.baseURL, APIKey: apiKey,
		Model: r.model, VisionModel: r.visionModel, Voice: r.voice,
		SampleRate: r.sampleRate, Version: r.version,
	}, true, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func dbNow() string { return db.FormatTime(time.Now()) }
