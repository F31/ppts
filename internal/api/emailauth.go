package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	jwtTTL        = 7 * 24 * time.Hour
	minPassword   = 8
	dummyPassword = "dummy-password-for-timing-only"
)

// ---------------------------------------------------------------------------
// HS256 JWT（手动实现，避免引入新依赖；仅支持 HS256，显式拒绝其他 alg）
// ---------------------------------------------------------------------------

func b64urlEncode(b []byte) string  { return base64.RawURLEncoding.EncodeToString(b) }
func b64urlDecode(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

type jwtClaims struct {
	Sub string `json:"sub"` // user_id
	Tid string `json:"tid"` // tenant_id
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

// issueToken 签发 HS256 JWT（sub=user_id, tid=tenant_id）。
func issueToken(tenantID, userID, secret string) (string, error) {
	if secret == "" {
		return "", errors.New("jwt: secret not configured")
	}
	now := time.Now()
	claims := jwtClaims{
		Sub: userID,
		Tid: tenantID,
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

// JWTAuthenticator 校验本系统自签名的 HS256 JWT；非本系统令牌返回 (false,nil) 以回退 OIDC。
type JWTAuthenticator struct {
	secret []byte
}

func NewJWTAuthenticator(secret string) *JWTAuthenticator {
	return &JWTAuthenticator{secret: []byte(secret)}
}

func (a *JWTAuthenticator) Authenticate(_ context.Context, r *http.Request) (Principal, bool, error) {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if raw == "" {
		return Principal{}, false, nil
	}
	parts := strings.Fields(raw)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return Principal{}, false, nil
	}
	claims, ok := verifyToken(parts[1], string(a.secret))
	if !ok {
		return Principal{}, false, nil
	}
	if claims.Sub == "" || claims.Tid == "" {
		return Principal{}, false, nil
	}
	return Principal{TenantID: claims.Tid, UserID: claims.Sub}, true, nil
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

// ---------------------------------------------------------------------------
// 路由注册（无认证端点；能力未配置时自降级为 503）
// ---------------------------------------------------------------------------

func registerAuthRoutes(mux *http.ServeMux, pool *pgxpool.Pool, jwtSecret, pepper string) {
	dummy := precomputeDummyHash()
	mux.HandleFunc("POST /auth/register", func(w http.ResponseWriter, r *http.Request) {
		authRegister(w, r, pool, jwtSecret, pepper)
	})
	mux.HandleFunc("POST /auth/email-login", func(w http.ResponseWriter, r *http.Request) {
		authEmailLogin(w, r, pool, jwtSecret, pepper, dummy)
	})
	mux.HandleFunc("GET /auth/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"email_password": jwtSecret != ""})
	})
}

// ---------------------------------------------------------------------------
// 注册端点
// ---------------------------------------------------------------------------

func authRegister(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, jwtSecret, pepper string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Email     string `json:"email"`
		Password  string `json:"password"`
		Username  string `json:"username"`
		FullName  string `json:"full_name"`
		Gender    string `json:"gender"`
		BirthDate string `json:"birth_date"`
		Phone     string `json:"phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(strings.ToLower(body.Email))
	if !validAccount(email) {
		http.Error(w, "invalid account", http.StatusBadRequest)
		return
	}
	if len(body.Password) < minPassword {
		http.Error(w, "password too short", http.StatusBadRequest)
		return
	}
	if jwtSecret == "" {
		http.Error(w, "email registration is not enabled", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()

	// 重复账号检查（users 无 RLS，可直接查）。
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT true FROM users WHERE email=$1`, email).Scan(&exists); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if exists {
		// 统一响应，不泄露账号是否已注册（R-16）。
		writeJSON(w, http.StatusConflict, map[string]any{"code": "registration_failed", "message": "registration failed"})
		return
	}

	hash, err := hashPassword(body.Password, pepper)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// 事务：创建个人租户 + owner 成员 + 用户 + 凭证。
	tenantID := uuid.New().String()
	userID := uuid.New().String()
	tx, err := pool.Begin(ctx)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, `INSERT INTO tenants(id, name, status) VALUES($1,$2,'active')`, tenantID, email); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// 设置事务局部租户上下文，满足 tenant_members / credentials 的 RLS WITH CHECK。
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(ctx, `INSERT INTO users(id, email) VALUES($1,$2)`, userID, email); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// 成员档案（0030）：注册时一并采集可选档案字段；空值以 NULL 存入。
	if _, err := tx.Exec(ctx,
		`INSERT INTO user_profiles (user_id, username, full_name, gender, birth_date, phone)
		 VALUES ($1, NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, '')::date, NULLIF($6, ''))
		 ON CONFLICT (user_id) DO NOTHING`,
		userID, body.Username, body.FullName, body.Gender, body.BirthDate, body.Phone); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tenant_members(tenant_id, user_id, role) VALUES($1,$2,'owner')`, tenantID, userID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO credentials(tenant_id, user_id, email, password_hash, algo) VALUES($1,$2,$3,$4,'bcrypt')`,
		tenantID, userID, email, hash); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(context.Background()); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	token, err := issueToken(tenantID, userID, jwtSecret)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"access_token": token,
		"tenant_id":    tenantID,
		"tenant_name":  email,
		"user_id":      userID,
		"account":      email,
	})
}

// authEmailLogin 账号登录（邮箱或手机号）：auth_lookup_credential 跨租户定位凭证（SECURITY DEFINER 绕过 RLS）。
// 未命中也做一次 bcrypt 比对以恒定耗时；所有失败统一 401 文案，不区分用户是否存在（R-16）。
func authEmailLogin(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, jwtSecret, pepper, dummyHash string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Email     string `json:"email"`
		Password  string `json:"password"`
		Username  string `json:"username"`
		FullName  string `json:"full_name"`
		Gender    string `json:"gender"`
		BirthDate string `json:"birth_date"`
		Phone     string `json:"phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(strings.ToLower(body.Email))
	if !validAccount(email) {
		http.Error(w, "invalid account", http.StatusBadRequest)
		return
	}
	if jwtSecret == "" {
		http.Error(w, "email authentication is not enabled", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()
	var tenantID, userID, hash, tenantName string
	err := pool.QueryRow(ctx,
		`SELECT tenant_id, user_id, password_hash, (SELECT name FROM tenants WHERE id = tenant_id) FROM auth_lookup_credential($1)`,
		email).
		Scan(&tenantID, &userID, &hash, &tenantName)
	if errors.Is(err, pgx.ErrNoRows) {
		// 未命中：仍做一次 bcrypt 比对以恒定耗时，防止通过响应时间/状态枚举账号（R-16）。
		_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(body.Password+pepper))
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "invalid_credentials", "message": "invalid email or password"})
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !verifyPassword(hash, body.Password, pepper) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "invalid_credentials", "message": "invalid email or password"})
		return
	}
	token, err := issueToken(tenantID, userID, jwtSecret)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"tenant_id":    tenantID,
		"tenant_name":  tenantName,
		"user_id":      userID,
		"account":      email,
	})
}

// validAccount 校验登录账号：邮箱（含 @）或手机号（可选 + 前缀，5-15 位数字）。
// 存储上复用 users.email / credentials.email 列作为账号列，两种形态同列共存。
func validAccount(account string) bool {
	if len(account) > 254 || strings.Contains(account, " ") {
		return false
	}
	if strings.Contains(account, "@") {
		return len(account) > 3 && !strings.Contains(account, "..")
	}
	digits := strings.TrimPrefix(account, "+")
	if len(digits) < 5 || len(digits) > 15 {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
