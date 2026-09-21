package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const columnsSQL = `tenant_id::text, name, kind, provider, base_url, encrypted_creds,
	model, vision_model, voice, sample_rate, is_default, enabled, version,
	EXTRACT(EPOCH FROM created_at)::bigint, EXTRACT(EPOCH FROM updated_at)::bigint`

type cacheKey struct {
	tenant string
	kind   Kind
}

type cacheEntry struct {
	cfg   *Config
	found bool
	at    time.Time
}

// PGStore 实现 Store 与 Resolver。
type PGStore struct {
	pool   *pgxpool.Pool
	cipher CredentialCipher
	ttl    time.Duration

	mu    sync.Mutex
	cache map[cacheKey]cacheEntry
}

// NewPGStore 创建存储。cipher 必填（key 加密/解密依赖）。
func NewPGStore(pool *pgxpool.Pool, cipher CredentialCipher) *PGStore {
	return &PGStore{pool: pool, cipher: cipher, ttl: 30 * time.Second, cache: map[cacheKey]cacheEntry{}}
}

// SetCacheTTL 调整解析缓存 TTL（测试用）。
func (s *PGStore) SetCacheTTL(d time.Duration) {
	if d > 0 {
		s.ttl = d
	}
}

// Invalidate 清除解析缓存（写操作后调用，跨进程靠 TTL 收敛）。
// 平台行变更时清除该 kind 的全部租户缓存（它们可能已缓存平台回退值）。
func (s *PGStore) Invalidate(tenantID string, kind Kind) {
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

type rawRow struct {
	tenantID, name, kind, provider, baseURL string
	enc                                     []byte
	model, visionModel, voice               string
	sampleRate                              int
	isDefault, enabled                      bool
	version                                 int
	createdAt, updatedAt                    int64
}

func (r *rawRow) scan(row interface{ Scan(...any) error }) error {
	return row.Scan(&r.tenantID, &r.name, &r.kind, &r.provider, &r.baseURL, &r.enc,
		&r.model, &r.visionModel, &r.voice, &r.sampleRate, &r.isDefault, &r.enabled,
		&r.version, &r.createdAt, &r.updatedAt)
}

func (s *PGStore) toGateway(r rawRow, apiKey string) *Gateway {
	return &Gateway{
		TenantID: r.tenantID, Name: r.name, Kind: Kind(r.kind), Provider: r.provider,
		BaseURL: r.baseURL, Model: r.model, VisionModel: r.visionModel, Voice: r.voice,
		SampleRate: r.sampleRate, IsDefault: r.isDefault, Enabled: r.enabled,
		Version: r.version, HasKey: apiKey != "", KeyMasked: MaskKey(apiKey),
		CreatedAt: r.createdAt, UpdatedAt: r.updatedAt,
	}
}

func (s *PGStore) List(ctx context.Context, tenantID string, kind Kind) ([]*Gateway, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+columnsSQL+` FROM model_gateways
		 WHERE tenant_id = $1 AND kind = $2 ORDER BY is_default DESC, created_at`,
		tenantID, string(kind))
	if err != nil {
		return nil, fmt.Errorf("gateway list: %w", err)
	}
	defer rows.Close()
	var out []*Gateway
	for rows.Next() {
		var r rawRow
		if err := r.scan(rows); err != nil {
			return nil, fmt.Errorf("gateway scan: %w", err)
		}
		key, _ := decodeCreds(s.cipher, r.tenantID, r.name, r.kind, r.enc)
		out = append(out, s.toGateway(r, key))
	}
	return out, rows.Err()
}

func (s *PGStore) Get(ctx context.Context, tenantID, name string, kind Kind) (*Gateway, error) {
	var r rawRow
	err := r.scan(s.pool.QueryRow(ctx,
		`SELECT `+columnsSQL+` FROM model_gateways WHERE tenant_id = $1 AND name = $2 AND kind = $3`,
		tenantID, name, string(kind)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("gateway get: %w", err)
	}
	key, _ := decodeCreds(s.cipher, r.tenantID, r.name, r.kind, r.enc)
	return s.toGateway(r, key), nil
}

func (s *PGStore) ResolveNamed(ctx context.Context, tenantID, name string, kind Kind) (*Config, error) {
	var r rawRow
	err := r.scan(s.pool.QueryRow(ctx,
		`SELECT `+columnsSQL+` FROM model_gateways WHERE tenant_id = $1 AND name = $2 AND kind = $3`,
		tenantID, name, string(kind)))
	if errors.Is(err, pgx.ErrNoRows) {
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

func (s *PGStore) Create(ctx context.Context, gw *Gateway, apiKey string) error {
	enc, err := encodeCreds(s.cipher, gw.TenantID, gw.Name, string(gw.Kind), apiKey)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO model_gateways
			(tenant_id, name, kind, provider, base_url, encrypted_creds, model, vision_model, voice, sample_rate, is_default, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		gw.TenantID, gw.Name, string(gw.Kind), gw.Provider, gw.BaseURL, enc,
		gw.Model, gw.VisionModel, gw.Voice, gw.SampleRate, gw.IsDefault, gw.Enabled)
	if err != nil {
		return fmt.Errorf("gateway create: %w", err)
	}
	s.Invalidate(gw.TenantID, gw.Kind)
	return nil
}

func (s *PGStore) Update(ctx context.Context, gw *Gateway, apiKey string) error {
	// apiKey 为空时原样保留已加密的凭据（不解密/不重加密）。
	var enc []byte
	if apiKey == "" {
		if err := s.pool.QueryRow(ctx,
			`SELECT encrypted_creds FROM model_gateways WHERE tenant_id = $1 AND name = $2 AND kind = $3`,
			gw.TenantID, gw.Name, string(gw.Kind)).Scan(&enc); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
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
	if err := s.updateTx(ctx, gw, enc, version); err != nil {
		return err
	}
	gw.Version = version
	s.Invalidate(gw.TenantID, gw.Kind)
	return nil
}

// updateTx 在事务内更新网关；is_default=true 时先清除同租户同类别的其他默认行
// （满足每租户每 kind 至多一个默认的部分唯一索引）。
func (s *PGStore) updateTx(ctx context.Context, gw *Gateway, enc []byte, version int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("gateway update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var tag pgconn.CommandTag
	if gw.IsDefault {
		if _, err := tx.Exec(ctx,
			`UPDATE model_gateways SET is_default=false WHERE tenant_id=$1 AND kind=$2 AND name <> $3`,
			gw.TenantID, string(gw.Kind), gw.Name); err != nil {
			return fmt.Errorf("gateway update: %w", err)
		}
	}
	tag, err = tx.Exec(ctx,
		`UPDATE model_gateways SET provider=$3, base_url=$4, encrypted_creds=$5, model=$6,
			vision_model=$7, voice=$8, sample_rate=$9, is_default=$10, enabled=$11,
			version=$12, updated_at=now()
		 WHERE tenant_id=$1 AND name=$2 AND kind=$13`,
		gw.TenantID, gw.Name, gw.Provider, gw.BaseURL, enc, gw.Model,
		gw.VisionModel, gw.Voice, gw.SampleRate, gw.IsDefault, gw.Enabled,
		version, string(gw.Kind))
	if err != nil {
		return fmt.Errorf("gateway update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("gateway update: %w", err)
	}
	return nil
}

// ChangeKind 把网关从 oldKind 迁移到 gw.Kind。主键含 kind，因此需在事务内插入新行、
// 删除旧行；apiKey 为空时用旧 kind 的 AAD 解密后按新 kind 重新加密。
func (s *PGStore) ChangeKind(ctx context.Context, gw *Gateway, oldKind Kind, apiKey string) error {
	if oldKind == gw.Kind {
		return s.Update(ctx, gw, apiKey)
	}
	var oldEnc []byte
	if err := s.pool.QueryRow(ctx,
		`SELECT encrypted_creds FROM model_gateways WHERE tenant_id = $1 AND name = $2 AND kind = $3`,
		gw.TenantID, gw.Name, string(oldKind)).Scan(&oldEnc); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("gateway change kind: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists int
	if err := tx.QueryRow(ctx,
		`SELECT 1 FROM model_gateways WHERE tenant_id = $1 AND name = $2 AND kind = $3`,
		gw.TenantID, gw.Name, string(gw.Kind)).Scan(&exists); err == nil {
		return ErrExists
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("gateway change kind: %w", err)
	}
	if gw.IsDefault {
		if _, err := tx.Exec(ctx,
			`UPDATE model_gateways SET is_default=false WHERE tenant_id=$1 AND kind=$2`,
			gw.TenantID, string(gw.Kind)); err != nil {
			return fmt.Errorf("gateway change kind: %w", err)
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO model_gateways
			(tenant_id, name, kind, provider, base_url, encrypted_creds, model, vision_model, voice, sample_rate, is_default, enabled)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		gw.TenantID, gw.Name, string(gw.Kind), gw.Provider, gw.BaseURL, enc,
		gw.Model, gw.VisionModel, gw.Voice, gw.SampleRate, gw.IsDefault, gw.Enabled); err != nil {
		return fmt.Errorf("gateway change kind: %w", err)
	}
	tag, err := tx.Exec(ctx,
		`DELETE FROM model_gateways WHERE tenant_id=$1 AND name=$2 AND kind=$3`,
		gw.TenantID, gw.Name, string(oldKind))
	if err != nil {
		return fmt.Errorf("gateway change kind: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("gateway change kind: %w", err)
	}
	gw.Version = version
	s.Invalidate(gw.TenantID, oldKind)
	s.Invalidate(gw.TenantID, gw.Kind)
	return nil
}

func (s *PGStore) Delete(ctx context.Context, tenantID, name string, kind Kind) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM model_gateways WHERE tenant_id = $1 AND name = $2 AND kind = $3`,
		tenantID, name, string(kind))
	if err != nil {
		return fmt.Errorf("gateway delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	s.Invalidate(tenantID, kind)
	return nil
}

func (s *PGStore) SetDefault(ctx context.Context, tenantID, name string, kind Kind) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("gateway set-default: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE model_gateways SET is_default=false WHERE tenant_id=$1 AND kind=$2`,
		tenantID, string(kind)); err != nil {
		return fmt.Errorf("gateway set-default: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE model_gateways SET is_default=true WHERE tenant_id=$1 AND name=$2 AND kind=$3`,
		tenantID, name, string(kind))
	if err != nil {
		return fmt.Errorf("gateway set-default: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("gateway set-default: %w", err)
	}
	s.Invalidate(tenantID, kind)
	return nil
}

// Resolve 按 租户默认 → 平台默认 顺序解析；带 TTL 缓存（含未命中缓存）。
func (s *PGStore) Resolve(ctx context.Context, tenantID string, kind Kind) (*Config, error) {
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

func (s *PGStore) resolveUncached(ctx context.Context, tenantID string, kind Kind) (*Config, bool, error) {
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

func (s *PGStore) lookupDefault(ctx context.Context, tenantID string, kind Kind) (*Config, bool, error) {
	var r rawRow
	err := r.scan(s.pool.QueryRow(ctx,
		`SELECT `+columnsSQL+` FROM model_gateways
		 WHERE tenant_id = $1 AND kind = $2 AND is_default AND enabled LIMIT 1`,
		tenantID, string(kind)))
	if errors.Is(err, pgx.ErrNoRows) {
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
