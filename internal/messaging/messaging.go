// Package messaging 管理消息服务配置（发件箱 SMTP / 短信网关），按租户持久化。
//
// 与 internal/gateway 同构：非敏感字段存 config(jsonb)，敏感凭据以 AES-GCM 密文存
// encrypted_creds（AAD 绑定 tenant_id+channel）；平台默认行 tenant_id=零 UUID，租户行覆盖。
// 运行时解析带 TTL 缓存，保存后最迟 30s 生效。
package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/F31/ppts/internal/mail"
	"github.com/F31/ppts/internal/tenant"
)

// ErrNotFound 表示该租户（含平台回退）没有配置该通道。
var ErrNotFound = errors.New("messaging: not found")

// PlatformTenantID 是平台默认行租户（与 Web 开发身份一致）。
const PlatformTenantID = "00000000-0000-0000-0000-000000000000"

// Channel 区分消息通道。
type Channel string

const (
	ChannelEmail Channel = "email"
	ChannelSMS   Channel = "sms"
)

// CredentialCipher 复用 BYOS/网关的加密原语（密钥：PPTS_GATEWAY_AES_KEY_BASE64，回退 BYOS）。
type CredentialCipher = tenant.CredentialCipher

// EmailConfig 是发件箱管理面视图（密码掩码，不回显明文）。
type EmailConfig struct {
	Enabled        bool   `json:"enabled"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Username       string `json:"username"`
	FromAddress    string `json:"from_address"`
	FromName       string `json:"from_name"`
	TLSMode        string `json:"tls_mode"`
	HasPassword    bool   `json:"has_password"`
	PasswordMasked string `json:"password_masked"`
	Version        int    `json:"version"`
	UpdatedAt      int64  `json:"updated_at"`
}

// EmailInput 是保存发件箱配置的入参（Password 为空表示保留原密码）。
type EmailInput struct {
	Enabled     bool
	Host        string
	Port        int
	Username    string
	FromAddress string
	FromName    string
	TLSMode     string
	Password    string
}

// EmailRuntime 是运行时解析结果（含解密后密码，仅进程内使用）。
type EmailRuntime struct {
	Enabled     bool
	Host        string
	Port        int
	Username    string
	Password    string
	FromAddress string
	FromName    string
	TLSMode     string
}

// SMSConfig 是短信网关管理面视图（密钥掩码）。
type SMSConfig struct {
	Enabled      bool   `json:"enabled"`
	Provider     string `json:"provider"`
	Endpoint     string `json:"endpoint"`
	SignName     string `json:"sign_name"`
	TemplateCode string `json:"template_code"`
	AccessKeyID  string `json:"access_key_id"`
	HasSecret    bool   `json:"has_secret"`
	SecretMasked string `json:"secret_masked"`
	Version      int    `json:"version"`
	UpdatedAt    int64  `json:"updated_at"`
}

// SMSInput 是保存短信网关配置的入参（AccessKeySecret 为空表示保留原密钥）。
type SMSInput struct {
	Enabled         bool
	Provider        string
	Endpoint        string
	SignName        string
	TemplateCode    string
	AccessKeyID     string
	AccessKeySecret string
}

// Store 管理消息服务配置的持久化与运行时解析。
type Store interface {
	GetEmail(ctx context.Context, tenantID string) (*EmailConfig, error)
	SaveEmail(ctx context.Context, tenantID string, in EmailInput) error
	ResolveEmail(ctx context.Context, tenantID string) (*EmailRuntime, error)
	ResolveSender(ctx context.Context, tenantID string) (mail.Sender, error)
	GetSMS(ctx context.Context, tenantID string) (*SMSConfig, error)
	SaveSMS(ctx context.Context, tenantID string, in SMSInput) error
}

// emailConfigJSON 是存储于 config 的非敏感字段。
type emailConfigJSON struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	FromAddress string `json:"from_address"`
	FromName    string `json:"from_name"`
	TLSMode     string `json:"tls_mode"`
}

type emailCredsJSON struct {
	Password string `json:"password"`
}

type smsConfigJSON struct {
	Provider     string `json:"provider"`
	Endpoint     string `json:"endpoint"`
	SignName     string `json:"sign_name"`
	TemplateCode string `json:"template_code"`
	AccessKeyID  string `json:"access_key_id"`
}

type smsCredsJSON struct {
	AccessKeySecret string `json:"access_key_secret"`
}

func channelAAD(tenantID string, ch Channel) []byte {
	return []byte("ppts:message:" + tenantID + ":" + string(ch))
}

// MaskSecret 掩码敏感串为 "前3****末3"（短串全掩码）。
func MaskSecret(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "****"
	}
	return s[:3] + "****" + s[len(s)-3:]
}

// ValidateEmail 校验发件箱配置的必填项与取值范围。
func ValidateEmail(in EmailInput) error {
	if strings.TrimSpace(in.Host) == "" {
		return errors.New("smtp host is required")
	}
	if in.Port != 0 && (in.Port < 1 || in.Port > 65535) {
		return errors.New("smtp port must be between 1 and 65535")
	}
	if strings.TrimSpace(in.FromAddress) == "" {
		return errors.New("from address is required")
	}
	if !strings.Contains(in.FromAddress, "@") {
		return errors.New("from address is not a valid email")
	}
	switch strings.ToLower(strings.TrimSpace(in.TLSMode)) {
	case "", "starttls", "tls", "none":
	default:
		return errors.New("tls mode must be starttls, tls or none")
	}
	return nil
}

func encodeCreds(cipher CredentialCipher, tenantID string, ch Channel, payload any) ([]byte, error) {
	if cipher == nil {
		return nil, errors.New("messaging: cipher not configured")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return cipher.Encrypt(raw, channelAAD(tenantID, ch))
}

func decodeCreds(cipher CredentialCipher, tenantID string, ch Channel, enc []byte, dst any) error {
	if len(enc) == 0 {
		return nil
	}
	if cipher == nil {
		return errors.New("messaging: cipher not configured")
	}
	raw, err := cipher.Decrypt(enc, channelAAD(tenantID, ch))
	if err != nil {
		return fmt.Errorf("messaging: decrypt creds: %w", err)
	}
	return json.Unmarshal(raw, dst)
}
