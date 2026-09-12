package objectstore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// signerVersion 是令牌格式版本。变更令牌格式必须升级版本号，旧版本令牌在兼容期仍可验证。
const signerVersion = "v1"

// Signer 生成与校验对象操作签名令牌（V4.0 §12.4"预签名链接生成必须校验租户上下文与键前缀"）。
//
// 令牌绑定 (tenant, key, operation, expire_at) 四个维度，HMAC-SHA256 签名。
// Sign 阶段强制校验 authorizedTenant 与 key 的租户前缀一致，越权直接拒绝签发。
type Signer struct {
	secret []byte
}

// NewSigner 以服务端密钥创建签名器。secret 不应为空；空值会让 SignedURL 显式不支持。
func NewSigner(secret []byte) *Signer {
	return &Signer{secret: secret}
}

// payload 是待签名的规范化字符串，顺序固定，不得改动。
func signPayload(tenant, key, op string, exp int64) string {
	return tenant + "\n" + key + "\n" + op + "\n" + strconv.FormatInt(exp, 10)
}

// Sign 为 key 签发操作令牌。authorizedTenant 必须是服务端已授权的租户；
// 与 key.TenantID 不一致时返回 ErrTenantMismatch。
func (s *Signer) Sign(authorizedTenant string, key ObjectKey, op Operation, ttl time.Duration) (string, error) {
	if len(s.secret) == 0 {
		return "", fmt.Errorf("%w: signer secret not configured", ErrOperationNotSupported)
	}
	if err := key.Validate(); err != nil {
		return "", err
	}
	if err := key.EnsureTenant(authorizedTenant); err != nil {
		return "", err
	}
	exp := time.Now().Add(ttl).Unix()
	payload := signPayload(key.TenantID, key.String(), string(op), exp)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	token := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signerVersion + ":" + token, nil
}

// Verify 校验令牌并返回其绑定的键与操作。校验维度：版本、签名、过期、键与操作一致性。
func (s *Signer) Verify(token string) (ObjectKey, Operation, error) {
	if len(s.secret) == 0 {
		return ObjectKey{}, "", fmt.Errorf("%w: signer secret not configured", ErrOperationNotSupported)
	}
	parts := strings.SplitN(token, ":", 2)
	if len(parts) != 2 || parts[0] != signerVersion {
		return ObjectKey{}, "", ErrSignatureInvalid
	}
	body := parts[1]
	dot := strings.LastIndexByte(body, '.')
	if dot <= 0 {
		return ObjectKey{}, "", ErrSignatureInvalid
	}
	payloadB64, sigB64 := body[:dot], body[dot+1:]
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return ObjectKey{}, "", ErrSignatureInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return ObjectKey{}, "", ErrSignatureInvalid
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return ObjectKey{}, "", ErrSignatureInvalid
	}
	// payload 为规范字符串，用 "\n" 切分为 4 段。
	lines := strings.Split(string(payload), "\n")
	if len(lines) != 4 {
		return ObjectKey{}, "", ErrSignatureInvalid
	}
	tenant, keyStr, opStr, expStr := lines[0], lines[1], lines[2], lines[3]
	key, err := Parse(keyStr)
	if err != nil {
		return ObjectKey{}, "", ErrSignatureInvalid
	}
	if key.TenantID != tenant {
		return ObjectKey{}, "", ErrSignatureInvalid
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return ObjectKey{}, "", ErrSignatureInvalid
	}
	if time.Now().Unix() > exp {
		return ObjectKey{}, "", fmt.Errorf("%w (expired at %d)", ErrTokenExpired, exp)
	}
	op := Operation(opStr)
	if op != OpRead && op != OpWrite && op != OpDelete {
		return ObjectKey{}, "", ErrSignatureInvalid
	}
	return key, op, nil
}

// EnsureOp 断言令牌允许的操作与要求的操作一致，否则返回 ErrSignatureInvalid。
func EnsureOp(got, want Operation) error {
	if got != want {
		return errors.Join(ErrSignatureInvalid, fmt.Errorf("operation mismatch: want %s got %s", want, got))
	}
	return nil
}
