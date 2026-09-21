// Package gateway 管理 TTS/LLM 模型网关配置（G3 可视化配置）。
//
// 配置持久化在 model_gateways 表，API key 以 AES-GCM 密文保存；
// 平台默认行（tenant_id = 零 UUID）为全局兜底，租户行覆盖平台行。
// 运行时解析带 30s TTL 缓存：Web 侧保存后最迟 30 秒在 worker 生效，无需重启。
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/F31/ppts/internal/tenant"
)

// ErrNotFound 表示该租户（含平台回退）没有可用的网关配置。
var ErrNotFound = errors.New("gateway: not found")

// ErrExists 表示目标 (name, kind) 已存在同名的另一类网关，无法迁移类型。
var ErrExists = errors.New("gateway: already exists")

// Kind 区分网关用途。
type Kind string

const (
	KindTTS Kind = "tts"
	KindLLM Kind = "llm"
)

// PlatformTenantID 是平台默认行的租户（与 Web 开发身份一致）。
const PlatformTenantID = "00000000-0000-0000-0000-000000000000"

// Gateway 是管理面模型（API key 已掩码，不回显明文）。
type Gateway struct {
	TenantID    string `json:"tenantId"`
	Name        string `json:"name"`
	Kind        Kind   `json:"kind"`
	Provider    string `json:"provider"`
	BaseURL     string `json:"baseUrl"`
	Model       string `json:"model"`
	VisionModel string `json:"visionModel"`
	Voice       string `json:"voice"`
	SampleRate  int    `json:"sampleRate"`
	IsDefault   bool   `json:"isDefault"`
	Enabled     bool   `json:"enabled"`
	Version     int    `json:"version"`
	HasKey      bool   `json:"hasKey"`
	KeyMasked   string `json:"keyMasked"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

// Config 是运行时解析结果（含解密后的 API key，仅进程内使用，不得外泄）。
type Config struct {
	Name        string
	Kind        Kind
	BaseURL     string
	APIKey      string
	Model       string
	VisionModel string
	Voice       string
	SampleRate  int
	Version     int
}

// Store 管理网关配置的持久化（管理面）。
type Store interface {
	List(ctx context.Context, tenantID string, kind Kind) ([]*Gateway, error)
	Get(ctx context.Context, tenantID, name string, kind Kind) (*Gateway, error)
	// ResolveNamed 返回指定命名行的解密运行时配置（探活/调试用）。
	ResolveNamed(ctx context.Context, tenantID, name string, kind Kind) (*Config, error)
	Create(ctx context.Context, gw *Gateway, apiKey string) error
	// Update 更新网关；apiKey 为空时保留原 key。
	Update(ctx context.Context, gw *Gateway, apiKey string) error
	// ChangeKind 把网关从 oldKind 迁移到 gw.Kind（主键含 kind，需重建行并重加密凭据）；
	// apiKey 为空时复用旧 key。目标 (name, gw.Kind) 已存在时返回 ErrExists。
	ChangeKind(ctx context.Context, gw *Gateway, oldKind Kind, apiKey string) error
	Delete(ctx context.Context, tenantID, name string, kind Kind) error
	SetDefault(ctx context.Context, tenantID, name string, kind Kind) error
}

// Resolver 按租户解析运行时配置（租户默认 → 平台默认 → ErrNotFound），带 TTL 缓存。
type Resolver interface {
	Resolve(ctx context.Context, tenantID string, kind Kind) (*Config, error)
	Invalidate(tenantID string, kind Kind)
}

// StoreResolver 组合管理面与运行时解析能力（PGStore 实现）。
type StoreResolver interface {
	Store
	Resolver
}

// CredentialCipher 复用 BYOS 的加密原语（密钥来源：PPTS_GATEWAY_AES_KEY_BASE64，
// 未设置时回退 PPTS_BYOS_AES_KEY_BASE64）。
type CredentialCipher = tenant.CredentialCipher

func gatewayAAD(tenantID, name, kind string) []byte {
	return []byte("ppts:gateway:" + tenantID + ":" + name + ":" + string(kind))
}

// MaskKey 把 API key 掩码为 "前3****末3"（短 key 全掩码）。
func MaskKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:3] + "****" + key[len(key)-3:]
}

// credsPayload 是加密保存的凭据结构。
type credsPayload struct {
	APIKey string `json:"api_key"`
}

// encodeCreds 加密凭据。
func encodeCreds(cipher CredentialCipher, tenantID, name, kind, apiKey string) ([]byte, error) {
	if cipher == nil {
		return nil, errors.New("gateway: cipher not configured")
	}
	raw, err := json.Marshal(credsPayload{APIKey: apiKey})
	if err != nil {
		return nil, err
	}
	return cipher.Encrypt(raw, gatewayAAD(tenantID, name, kind))
}

// decodeCreds 解密凭据。
func decodeCreds(cipher CredentialCipher, tenantID, name, kind string, enc []byte) (string, error) {
	if cipher == nil {
		return "", errors.New("gateway: cipher not configured")
	}
	raw, err := cipher.Decrypt(enc, gatewayAAD(tenantID, name, kind))
	if err != nil {
		return "", fmt.Errorf("gateway: decrypt creds: %w", err)
	}
	var p credsPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("gateway: decode creds: %w", err)
	}
	return p.APIKey, nil
}
