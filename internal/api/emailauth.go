package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// B5-M4 账号自助注册（邮箱或手机号，决策 ①A/②A/③A）：
//   - ①A 自注册创建个人租户，首个用户为 owner，无邀请流程；
//   - ②A 注册即信任，不做邮件验证（当前无邮件能力）；
//   - ③A HS256 自签名 JWT（环境变量 PPTS_JWT_SECRET），与 OIDC 并存。
//
// 密码存储：bcrypt（自带盐）+ 全局 pepper（环境变量 PPTS_PASSWORD_PEPPER，不落库）。
// 反枚举（R-16）：登录统一 401 文案、未命中也做一次 bcrypt 比对以恒定耗时。

const (
	jwtIssuer     = "ppts"
	jwtAudience   = "ppts-console"
	dummyPassword = "dummy-password-for-timing-only"
)

// jwtTTL 是自签名会话令牌有效期，可由 PPTS_JWT_TTL 覆盖（默认 24h，较原 7 天显著缩短）。
// 以包级变量（非 const）便于 env 注入与测试覆盖。
var jwtTTL = 24 * time.Hour

// 账号类型：注册时一次性选定，之后不变（不做个人→组织升级）。
//   - personal：个人账号 = 只有一个成员的租户，前端隐藏成员管理/邀请协作者入口；
//   - organization：组织账号，可邀请成员并分配角色。
//
// 后端权限模型不按类型分叉（隔离统一按 tenant_id），类型仅驱动前端入口显隐。
const (
	accountTypePersonal     = "personal"
	accountTypeOrganization = "organization"

	// 组织名称长度按字符（非字节）计；规则与前端 web/src/pages/Login.tsx 的 orgNameError 一致。
	minOrgName = 2
	maxOrgName = 40

	// 个人租户默认显示名后缀："{账号@前} 的空间"。
	personalTenantSuffix = " 的空间"
)

// validateOrgName 校验组织名称：长度 2~40 个字符，允许中英文/数字/空格/-_&.。
func validateOrgName(name string) error {
	if n := utf8.RuneCountInString(name); n < minOrgName || n > maxOrgName {
		return fmt.Errorf("org name must be %d-%d characters", minOrgName, maxOrgName)
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		switch r {
		case ' ', '-', '_', '&', '.':
			continue
		}
		return errors.New("org name contains invalid characters")
	}
	return nil
}

// defaultPersonalTenantName 由登录账号生成个人租户默认显示名："{@前部分} 的空间"。
// 账号为手机号（无 @）时取整个账号。
func defaultPersonalTenantName(account string) string {
	local := account
	if at := strings.IndexByte(account, '@'); at >= 0 {
		local = account[:at]
	}
	if local == "" {
		local = account
	}
	return local + personalTenantSuffix
}

// ---------------------------------------------------------------------------
// HS256 JWT（手动实现，避免引入新依赖；仅支持 HS256，显式拒绝其他 alg）
// ---------------------------------------------------------------------------

func b64urlEncode(b []byte) string          { return base64.RawURLEncoding.EncodeToString(b) }
func b64urlDecode(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

type jwtClaims struct {
	Sub string `json:"sub"` // user_id
	Tid string `json:"tid"` // tenant_id
	Tv  int    `json:"tv"`  // 会话代次（users.token_version）；改密/全端登出后旧令牌失效
	Iss string `json:"iss"`
	Aud string `json:"aud"`
	Exp int64  `json:"exp"`
	Iat int64  `json:"iat"`
}

func signHS256(signingInput string, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingInput))
	return b64urlEncode(mac.Sum(nil))
}

// issueToken 签发 HS256 JWT（sub=user_id, tid=tenant_id, tv=会话代次）。
func issueToken(tenantID, userID string, tokenVersion int, secret string) (string, error) {
	if secret == "" {
		return "", errors.New("jwt: secret not configured")
	}
	now := time.Now()
	claims := jwtClaims{
		Sub: userID,
		Tid: tenantID,
		Tv:  tokenVersion,
		Iss: jwtIssuer,
		Aud: jwtAudience,
		Exp: now.Add(jwtTTL).Unix(),
		Iat: now.Unix(),
	}
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := b64urlEncode(header) + "." + b64urlEncode(payload)
	return input + "." + signHS256(input, []byte(secret)), nil
}

// verifyToken 校验 HS256 JWT；任何不匹配（签名/alg/iss/aud/exp）均返回 false。
func verifyToken(token, secret string) (jwtClaims, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtClaims{}, false
	}
	// 拒绝算法混淆：显式要求 HS256。
	var hdr struct {
		Alg string `json:"alg"`
	}
	if raw, err := b64urlDecode(parts[0]); err != nil {
		return jwtClaims{}, false
	} else if err := json.Unmarshal(raw, &hdr); err != nil || hdr.Alg != "HS256" {
		return jwtClaims{}, false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	expected := b64urlEncode(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return jwtClaims{}, false
	}
	raw, err := b64urlDecode(parts[1])
	if err != nil {
		return jwtClaims{}, false
	}
	var claims jwtClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return jwtClaims{}, false
	}
	if claims.Iss != jwtIssuer || claims.Aud != jwtAudience {
		return jwtClaims{}, false
	}
	if claims.Exp > 0 && time.Now().Unix() > claims.Exp {
		return jwtClaims{}, false
	}
	return claims, true
}

// SessionValidator 校验会话仍有效（users.status=active 且 token_version 未被提升）。
// 由 PG 实现；未注入时跳过（开发/测试/单机）。
type SessionValidator interface {
	ValidateSession(ctx context.Context, userID string, tokenVersion int) (bool, error)
}

// SessionCookieName 是浏览器会话 Cookie 名（HttpOnly）。
const SessionCookieName = "ppts_session"

// JWTAuthenticator 校验本系统自签名的 HS256 JWT；非本系统令牌返回 (false,nil) 以回退 OIDC。
// 令牌来源优先 HttpOnly Cookie（浏览器），其次 Authorization: Bearer（API 客户端/OIDC 回退）。
type JWTAuthenticator struct {
	secret   []byte
	sessions SessionValidator
}

func NewJWTAuthenticator(secret string) *JWTAuthenticator {
	return &JWTAuthenticator{secret: []byte(secret)}
}

// WithSessionValidator 注入会话有效性校验（改密/全端登出后旧令牌立即失效）。
func (a *JWTAuthenticator) WithSessionValidator(v SessionValidator) *JWTAuthenticator {
	a.sessions = v
	return a
}

func (a *JWTAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, bool, error) {
	raw := tokenFromRequest(r)
	if raw == "" {
		return Principal{}, false, nil
	}
	claims, ok := verifyToken(raw, string(a.secret))
	if !ok {
		return Principal{}, false, nil
	}
	if claims.Sub == "" || claims.Tid == "" {
		return Principal{}, false, nil
	}
	if a.sessions != nil {
		ok, err := a.sessions.ValidateSession(ctx, claims.Sub, claims.Tv)
		if err != nil {
			return Principal{}, false, err
		}
		if !ok {
			return Principal{}, false, nil
		}
	}
	return Principal{TenantID: claims.Tid, UserID: claims.Sub}, true, nil
}

// tokenFromRequest 取令牌：优先 HttpOnly Cookie，其次 Authorization: Bearer。
func tokenFromRequest(r *http.Request) string {
	if c, err := r.Cookie(SessionCookieName); err == nil && strings.TrimSpace(c.Value) != "" {
		return strings.TrimSpace(c.Value)
	}
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	parts := strings.Fields(raw)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	return ""
}

// CombinedAuthenticator 优先校验自签名 JWT，失败再回退 OIDC（external IdP bearer）。
type CombinedAuthenticator struct {
	oidc *OIDCAuthenticator
	jwt  *JWTAuthenticator
}

func NewCombinedAuthenticator(oidc *OIDCAuthenticator, jwt *JWTAuthenticator) *CombinedAuthenticator {
	return &CombinedAuthenticator{oidc: oidc, jwt: jwt}
}

func (c *CombinedAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, bool, error) {
	if c.jwt != nil {
		if p, ok, _ := c.jwt.Authenticate(ctx, r); ok {
			return p, true, nil
		}
	}
	if c.oidc != nil {
		return c.oidc.Authenticate(ctx, r)
	}
	return Principal{}, false, nil
}

// ---------------------------------------------------------------------------
// 密码哈希（bcrypt + 全局 pepper）
// ---------------------------------------------------------------------------

func hashPassword(password, pepper string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password+pepper), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

func verifyPassword(hash, password, pepper string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password+pepper)) == nil
}

func precomputeDummyHash() string {
	h, _ := bcrypt.GenerateFromPassword([]byte(dummyPassword), bcrypt.DefaultCost)
	return string(h)
}

// SetJWTTTL 设置自签名会话有效期（cmd/ppts 从 PPTS_JWT_TTL 注入）。
func SetJWTTTL(d time.Duration) {
	if d > 0 {
		jwtTTL = d
	}
}
