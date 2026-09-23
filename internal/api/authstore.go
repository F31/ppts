package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// authStore 封装邮箱验证/密码重置令牌与会话有效性的数据库访问。
// 这些表（users / auth_tokens）是全局控制面数据，不受 RLS 约束；应用仅通过本类型的
// 定型查询访问，不暴露任意 SQL 入口。
type authStore struct {
	pool *pgxpool.Pool
}

func newAuthStore(pool *pgxpool.Pool) *authStore { return &authStore{pool: pool} }

// errTokenInvalid 表示令牌不存在/已用过/已过期。
var errTokenInvalid = errors.New("auth: token invalid or expired")

const (
	purposeEmailVerify   = "email_verify"
	purposePasswordReset = "password_reset"

	emailVerifyTTL   = 48 * time.Hour
	passwordResetTTL = 1 * time.Hour
)

// randomToken 生成 32 字节随机令牌（base64url，无填充），只在响应/邮件中出现一次。
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// issueToken 生成一次性令牌并入库（只存 SHA-256 哈希）；同一用户同一用途旧令牌先删除。
func (s *authStore) issueToken(ctx context.Context, userID, purpose string, ttl time.Duration) (string, error) {
	raw, err := randomToken()
	if err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM auth_tokens WHERE user_id=$1 AND purpose=$2`, userID, purpose); err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO auth_tokens(user_id, purpose, token_hash, expires_at)
		 VALUES($1,$2,$3, now() + make_interval(secs => $4))`,
		userID, purpose, hashToken(raw), int(ttl.Seconds())); err != nil {
		return "", err
	}
	return raw, nil
}

// consumeToken 原子校验并消费令牌（单次使用），返回 user_id。
func (s *authStore) consumeToken(ctx context.Context, raw, purpose string) (string, error) {
	var userID string
	err := s.pool.QueryRow(ctx,
		`UPDATE auth_tokens SET used_at = now()
		 WHERE token_hash=$1 AND purpose=$2 AND used_at IS NULL AND expires_at > now()
		 RETURNING user_id`, hashToken(raw), purpose).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errTokenInvalid
	}
	if err != nil {
		return "", err
	}
	return userID, nil
}

// userAccount 是登录/验证所需的最小用户视图。
type userAccount struct {
	UserID        string
	Email         string
	Phone         string
	EmailVerified bool
	TokenVersion  int
	Status        string
}

// bumpTokenVersion 提升会话代次，使该用户此前签发的所有 JWT 立即失效（改密/全端登出）。
func (s *authStore) bumpTokenVersion(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET token_version = token_version + 1, updated_at = now() WHERE id=$1`, userID)
	return err
}

// markEmailVerified 标记邮箱已验证。
func (s *authStore) markEmailVerified(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET email_verified_at = COALESCE(email_verified_at, now()), updated_at = now() WHERE id=$1`, userID)
	return err
}

// updatePassword 更新该用户在 credentials 中的密码哈希。
// credentials 受 RLS 保护，按 user_id 直改会被拒，故经由 SECURITY DEFINER 函数执行。
func (s *authStore) updatePassword(ctx context.Context, userID, passwordHash string) error {
	var affected int
	if err := s.pool.QueryRow(ctx, `SELECT auth_set_password($1, $2)`, userID, passwordHash).Scan(&affected); err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("auth: credential not found for user")
	}
	return nil
}

// userByID 读取用户账号视图（无 RLS）。
func (s *authStore) userByID(ctx context.Context, userID string) (*userAccount, error) {
	var u userAccount
	var verified *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT id, COALESCE(email,''), COALESCE(phone,''), email_verified_at, token_version, status
		 FROM users WHERE id=$1`, userID).
		Scan(&u.UserID, &u.Email, &u.Phone, &verified, &u.TokenVersion, &u.Status)
	if err != nil {
		return nil, err
	}
	u.EmailVerified = verified != nil
	return &u, nil
}

// pgSessionValidator 实现 SessionValidator：校验 users.status=active 且 token_version 未变。
type pgSessionValidator struct {
	pool *pgxpool.Pool
}

func newPGSessionValidator(pool *pgxpool.Pool) *pgSessionValidator {
	return &pgSessionValidator{pool: pool}
}

func (v *pgSessionValidator) ValidateSession(ctx context.Context, userID string, tokenVersion int) (bool, error) {
	var tv int
	var status string
	err := v.pool.QueryRow(ctx, `SELECT token_version, status FROM users WHERE id=$1`, userID).Scan(&tv, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == "active" && tv == tokenVersion, nil
}

// errUserDisabled 表示账号被停用。
var errUserDisabled = errors.New("auth: account disabled")

// loginRow 是登录定位查询的结果。
type loginRow struct {
	TenantID   string
	UserID     string
	Hash       string
	TenantName string
	TenantType string
	userAccount
}

// lookupLogin 按归一化账号（邮箱或手机号）跨租户定位凭证并 JOIN 用户状态。
func (s *authStore) lookupLogin(ctx context.Context, account string) (*loginRow, error) {
	var row loginRow
	var verified *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT c.tenant_id, c.user_id, c.password_hash, t.name, t.type,
		        u.id, COALESCE(u.email,''), COALESCE(u.phone,''), u.email_verified_at, u.token_version, u.status
		 FROM auth_lookup_credential($1) c
		 JOIN tenants t ON t.id = c.tenant_id
		 JOIN users u ON u.id = c.user_id`,
		account).
		Scan(&row.TenantID, &row.UserID, &row.Hash, &row.TenantName, &row.TenantType,
			&row.userAccount.UserID, &row.userAccount.Email, &row.userAccount.Phone, &verified,
			&row.userAccount.TokenVersion, &row.userAccount.Status)
	if err != nil {
		return nil, err
	}
	row.userAccount.EmailVerified = verified != nil
	return &row, nil
}

// accountExists 判断邮箱/手机号是否已注册（users 全局表）。
func (s *authStore) accountExists(ctx context.Context, acct account) (bool, error) {
	var exists bool
	var q string
	var arg string
	if acct.Kind == accountKindPhone {
		q, arg = `SELECT EXISTS(SELECT 1 FROM users WHERE phone=$1)`, acct.Phone
	} else {
		q, arg = `SELECT EXISTS(SELECT 1 FROM users WHERE email=$1)`, acct.Email
	}
	err := s.pool.QueryRow(ctx, q, arg).Scan(&exists)
	return exists, err
}

// NewPGSessionValidator 导出构造（cmd/ppts 装配用）。
func NewPGSessionValidator(pool *pgxpool.Pool) SessionValidator {
	return newPGSessionValidator(pool)
}
