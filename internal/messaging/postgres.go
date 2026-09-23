package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/mail"
)

type cacheKey struct {
	tenant string
	ch     Channel
}

type cacheEntry struct {
	cfg   *EmailRuntime
	found bool
	at    time.Time
}

// PGStore 实现 Store（PostgreSQL）。config 非敏感、encrypted_creds 密文。
type PGStore struct {
	pool   *pgxpool.Pool
	cipher CredentialCipher
	ttl    time.Duration

	mu    sync.Mutex
	cache map[cacheKey]cacheEntry
}

// NewPGStore 创建存储；cipher 必填（凭据加解密依赖）。
func NewPGStore(pool *pgxpool.Pool, cipher CredentialCipher) *PGStore {
	return &PGStore{pool: pool, cipher: cipher, ttl: 30 * time.Second, cache: map[cacheKey]cacheEntry{}}
}

// SetCacheTTL 调整解析缓存 TTL（测试用）。
func (s *PGStore) SetCacheTTL(d time.Duration) {
	if d > 0 {
		s.ttl = d
	}
}

func (s *PGStore) invalidate(tenantID string, ch Channel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tenantID == PlatformTenantID {
		for k := range s.cache {
			if k.ch == ch {
				delete(s.cache, k)
			}
		}
		return
	}
	delete(s.cache, cacheKey{tenant: tenantID, ch: ch})
}

type rawRow struct {
	tenantID, channel string
	enabled           bool
	config            []byte
	enc               []byte
	version           int
	updatedAt         int64
}

func (s *PGStore) getRow(ctx context.Context, tenantID string, ch Channel) (*rawRow, error) {
	var r rawRow
	err := s.pool.QueryRow(ctx,
		`SELECT tenant_id::text, channel, enabled, config, encrypted_creds, version,
		        EXTRACT(EPOCH FROM updated_at)::bigint
		 FROM message_channels WHERE tenant_id=$1 AND channel=$2`,
		tenantID, string(ch)).Scan(&r.tenantID, &r.channel, &r.enabled, &r.config, &r.enc, &r.version, &r.updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("messaging get: %w", err)
	}
	return &r, nil
}

func (s *PGStore) GetEmail(ctx context.Context, tenantID string) (*EmailConfig, error) {
	r, err := s.getRow(ctx, tenantID, ChannelEmail)
	if err != nil {
		return nil, err
	}
	var cfg emailConfigJSON
	if len(r.config) > 0 {
		if err := json.Unmarshal(r.config, &cfg); err != nil {
			return nil, fmt.Errorf("messaging: decode email config: %w", err)
		}
	}
	var creds emailCredsJSON
	if err := decodeCreds(s.cipher, tenantID, ChannelEmail, r.enc, &creds); err != nil {
		return nil, err
	}
	return &EmailConfig{
		Enabled: r.enabled, Host: cfg.Host, Port: cfg.Port, Username: cfg.Username,
		FromAddress: cfg.FromAddress, FromName: cfg.FromName, TLSMode: cfg.TLSMode,
		HasPassword: creds.Password != "", PasswordMasked: MaskSecret(creds.Password),
		Version: r.version, UpdatedAt: r.updatedAt,
	}, nil
}

func (s *PGStore) SaveEmail(ctx context.Context, tenantID string, in EmailInput) error {
	if err := ValidateEmail(in); err != nil {
		return err
	}
	cfg, err := json.Marshal(emailConfigJSON{
		Host: strings.TrimSpace(in.Host), Port: in.Port, Username: strings.TrimSpace(in.Username),
		FromAddress: strings.TrimSpace(in.FromAddress), FromName: strings.TrimSpace(in.FromName),
		TLSMode: strings.ToLower(strings.TrimSpace(in.TLSMode)),
	})
	if err != nil {
		return err
	}
	hasNew := in.Password != ""
	var enc []byte
	if hasNew {
		enc, err = encodeCreds(s.cipher, tenantID, ChannelEmail, emailCredsJSON{Password: in.Password})
		if err != nil {
			return err
		}
	}
	err = upsert(ctx, s.pool, tenantID, ChannelEmail, in.Enabled, cfg, enc, hasNew)
	if err != nil {
		return err
	}
	s.invalidate(tenantID, ChannelEmail)
	return nil
}

func (s *PGStore) GetSMS(ctx context.Context, tenantID string) (*SMSConfig, error) {
	r, err := s.getRow(ctx, tenantID, ChannelSMS)
	if err != nil {
		return nil, err
	}
	var cfg smsConfigJSON
	if len(r.config) > 0 {
		if err := json.Unmarshal(r.config, &cfg); err != nil {
			return nil, fmt.Errorf("messaging: decode sms config: %w", err)
		}
	}
	var creds smsCredsJSON
	if err := decodeCreds(s.cipher, tenantID, ChannelSMS, r.enc, &creds); err != nil {
		return nil, err
	}
	return &SMSConfig{
		Enabled: r.enabled, Provider: cfg.Provider, Endpoint: cfg.Endpoint,
		SignName: cfg.SignName, TemplateCode: cfg.TemplateCode, AccessKeyID: cfg.AccessKeyID,
		HasSecret: creds.AccessKeySecret != "", SecretMasked: MaskSecret(creds.AccessKeySecret),
		Version: r.version, UpdatedAt: r.updatedAt,
	}, nil
}

func (s *PGStore) SaveSMS(ctx context.Context, tenantID string, in SMSInput) error {
	cfg, err := json.Marshal(smsConfigJSON{
		Provider: strings.TrimSpace(in.Provider), Endpoint: strings.TrimSpace(in.Endpoint),
		SignName: strings.TrimSpace(in.SignName), TemplateCode: strings.TrimSpace(in.TemplateCode),
		AccessKeyID: strings.TrimSpace(in.AccessKeyID),
	})
	if err != nil {
		return err
	}
	hasNew := in.AccessKeySecret != ""
	var enc []byte
	if hasNew {
		enc, err = encodeCreds(s.cipher, tenantID, ChannelSMS, smsCredsJSON{AccessKeySecret: in.AccessKeySecret})
		if err != nil {
			return err
		}
	}
	if err := upsert(ctx, s.pool, tenantID, ChannelSMS, in.Enabled, cfg, enc, hasNew); err != nil {
		return err
	}
	s.invalidate(tenantID, ChannelSMS)
	return nil
}

// upsert 写入/更新一行；hasNewCreds 为 false 时保留原 encrypted_creds。
func upsert(ctx context.Context, pool *pgxpool.Pool, tenantID string, ch Channel, enabled bool, cfg, enc []byte, hasNewCreds bool) error {
	if enc == nil {
		enc = []byte{}
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO message_channels (tenant_id, channel, enabled, config, encrypted_creds, version, updated_at)
		 VALUES ($1, $2, $3, $4, $5, 1, now())
		 ON CONFLICT (tenant_id, channel) DO UPDATE SET
		   enabled = EXCLUDED.enabled,
		   config = EXCLUDED.config,
		   encrypted_creds = CASE WHEN $6 THEN EXCLUDED.encrypted_creds ELSE message_channels.encrypted_creds END,
		   version = message_channels.version + 1,
		   updated_at = now()`,
		tenantID, string(ch), enabled, cfg, enc, hasNewCreds)
	if err != nil {
		return fmt.Errorf("messaging upsert: %w", err)
	}
	return nil
}

// ResolveEmail 按 租户（enabled）→ 平台默认（enabled）解析；带 TTL 缓存（含未命中）。
func (s *PGStore) ResolveEmail(ctx context.Context, tenantID string) (*EmailRuntime, error) {
	key := cacheKey{tenant: tenantID, ch: ChannelEmail}
	s.mu.Lock()
	if e, ok := s.cache[key]; ok && time.Since(e.at) < s.ttl {
		s.mu.Unlock()
		if !e.found {
			return nil, ErrNotFound
		}
		return e.cfg, nil
	}
	s.mu.Unlock()

	cfg, found, err := s.resolveEmailUncached(ctx, tenantID)
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

func (s *PGStore) resolveEmailUncached(ctx context.Context, tenantID string) (*EmailRuntime, bool, error) {
	if cfg, found, err := s.lookupEmail(ctx, tenantID); err != nil {
		return nil, false, err
	} else if found {
		return cfg, true, nil
	}
	if tenantID != PlatformTenantID {
		if cfg, found, err := s.lookupEmail(ctx, PlatformTenantID); err != nil {
			return nil, false, err
		} else if found {
			return cfg, true, nil
		}
	}
	return nil, false, nil
}

func (s *PGStore) lookupEmail(ctx context.Context, tenantID string) (*EmailRuntime, bool, error) {
	r, err := s.getRow(ctx, tenantID, ChannelEmail)
	if errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !r.enabled {
		return nil, false, nil
	}
	var cfg emailConfigJSON
	if len(r.config) > 0 {
		if err := json.Unmarshal(r.config, &cfg); err != nil {
			return nil, false, fmt.Errorf("messaging: decode email config: %w", err)
		}
	}
	var creds emailCredsJSON
	if err := decodeCreds(s.cipher, tenantID, ChannelEmail, r.enc, &creds); err != nil {
		return nil, false, err
	}
	return &EmailRuntime{
		Enabled: true, Host: cfg.Host, Port: cfg.Port, Username: cfg.Username,
		Password: creds.Password, FromAddress: cfg.FromAddress, FromName: cfg.FromName, TLSMode: cfg.TLSMode,
	}, true, nil
}

// ResolveSender 解析发件箱并构建可直接发信的 mail.Sender。
func (s *PGStore) ResolveSender(ctx context.Context, tenantID string) (mail.Sender, error) {
	rt, err := s.ResolveEmail(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return BuildSender(rt)
}

// BuildSender 由运行时配置构建 SMTP 发送器。
func BuildSender(rt *EmailRuntime) (mail.Sender, error) {
	if rt == nil {
		return nil, ErrNotFound
	}
	from := strings.TrimSpace(rt.FromAddress)
	if name := strings.TrimSpace(rt.FromName); name != "" {
		from = fmt.Sprintf("%s <%s>", name, from)
	}
	return mail.NewSMTPSender(mail.SMTPConfig{
		Host: rt.Host, Port: rt.Port, Username: rt.Username, Password: rt.Password,
		From: from, Mode: rt.TLSMode,
	})
}

var _ Store = (*PGStore)(nil)
