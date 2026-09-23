package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/mail"
)

// authDeps 是认证路由的依赖集合。
type authDeps struct {
	pool       *pgxpool.Pool
	store      *authStore
	jwtSecret  string
	pepper     string
	mailer     mail.Sender
	baseURL    string // 邮件链接基址（PPTS_PUBLIC_BASE_URL）；空则按请求推导
	limits     *authLimits
	trustProxy bool
	// requireEmailVerified 为 true 时，未验证邮箱的账号登录被拒（默认 false，软提示）。
	requireEmailVerified bool
	logger               *log.Logger
	dummy                string
}

// registerAuthRoutes 挂载邮箱/手机自助注册、登录、验证、重置、登出端点。
// 所有写端点均带 IP 限流；登录另有账号失败锁定。
func registerAuthRoutes(mux *http.ServeMux, d *authDeps) {
	mux.HandleFunc("POST /auth/register", d.handleRegister)
	mux.HandleFunc("POST /auth/email-login", d.handleLogin)
	mux.HandleFunc("POST /auth/logout", d.handleLogout)
	mux.HandleFunc("POST /auth/verify-email", d.handleVerifyEmail)
	mux.HandleFunc("POST /auth/resend-verification", d.handleResendVerification)
	mux.HandleFunc("POST /auth/forgot-password", d.handleForgotPassword)
	mux.HandleFunc("POST /auth/reset-password", d.handleResetPassword)
	mux.HandleFunc("GET /auth/config", d.handleAuthConfig)
}

// ---------------------------------------------------------------------------
// 通用工具
// ---------------------------------------------------------------------------

func (d *authDeps) ip(r *http.Request) string { return clientIP(r, d.trustProxy) }

func (d *authDeps) tooMany(w http.ResponseWriter) {
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"code": "rate_limited", "message": "too many attempts, please try again later",
	})
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(dst)
}

// setSessionCookie 写入 HttpOnly 会话 Cookie。Secure 标志在 HTTPS（或可信代理声明的
// X-Forwarded-Proto=https）时置位；SameSite=Lax 兼容 OAuth 回跳。
func (d *authDeps) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r, d.trustProxy),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(jwtTTL.Seconds()),
	})
}

func (d *authDeps) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func requestIsHTTPS(r *http.Request, trustProxy bool) bool {
	if r.TLS != nil {
		return true
	}
	if trustProxy {
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			return strings.EqualFold(strings.TrimSpace(proto), "https")
		}
	}
	return strings.EqualFold(strings.TrimSpace(os.Getenv("PPTS_COOKIE_SECURE")), "true")
}

func (d *authDeps) publicBase(r *http.Request) string {
	if d.baseURL != "" {
		return strings.TrimSuffix(d.baseURL, "/")
	}
	scheme := "http"
	if requestIsHTTPS(r, d.trustProxy) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (d *authDeps) sendVerifyEmail(ctx context.Context, to, token, base string) {
	if d.mailer == nil || to == "" {
		if d.logger != nil {
			d.logger.Printf("auth: mailer not configured; email verification link for %s: %s/verify-email?token=%s", to, base, token)
		}
		return
	}
	link := base + "/verify-email?token=" + token
	msg := mail.Message{
		To:      to,
		Subject: "验证你的 PPTS 账号邮箱",
		Text:    "欢迎使用 PPTS。请点击下面的链接完成邮箱验证（48 小时内有效）：\n\n" + link + "\n\n如果这不是你本人的操作，请忽略本邮件。",
	}
	if err := d.mailer.Send(ctx, msg); err != nil && d.logger != nil {
		d.logger.Printf("auth: send verify email to %s failed: %v", to, err)
	}
}

func (d *authDeps) sendResetEmail(ctx context.Context, to, token, base string) {
	if d.mailer == nil || to == "" {
		if d.logger != nil {
			d.logger.Printf("auth: mailer not configured; password reset link for %s: %s/reset-password?token=%s", to, base, token)
		}
		return
	}
	link := base + "/reset-password?token=" + token
	msg := mail.Message{
		To:      to,
		Subject: "重置你的 PPTS 账号密码",
		Text:    "我们收到了重置密码的请求。请点击下面的链接设置新密码（1 小时内有效）：\n\n" + link + "\n\n如果这不是你本人的操作，请忽略本邮件，你的密码不会改变。",
	}
	if err := d.mailer.Send(ctx, msg); err != nil && d.logger != nil {
		d.logger.Printf("auth: send reset email to %s failed: %v", to, err)
	}
}

// ---------------------------------------------------------------------------
// 注册
// ---------------------------------------------------------------------------

func (d *authDeps) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !d.limits.register.allow(d.ip(r)) {
		d.tooMany(w)
		return
	}
	var body struct {
		Account     string `json:"account"`
		Email       string `json:"email"`
		Password    string `json:"password"`
		AccountType string `json:"account_type"`
		OrgName     string `json:"org_name"`
		Username    string `json:"username"`
		FullName    string `json:"full_name"`
		Gender      string `json:"gender"`
		BirthDate   string `json:"birth_date"`
		Phone       string `json:"phone"`
	}
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	rawAccount := body.Account
	if strings.TrimSpace(rawAccount) == "" {
		rawAccount = body.Email
	}
	acct, err := classifyAccount(rawAccount)
	if err != nil {
		http.Error(w, "invalid account", http.StatusBadRequest)
		return
	}
	if err := validatePassword(body.Password, accountLocalPart(acct)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "weak_password", "message": err.Error()})
		return
	}
	accountType := strings.TrimSpace(body.AccountType)
	if accountType != accountTypePersonal && accountType != accountTypeOrganization {
		http.Error(w, "account_type must be personal or organization", http.StatusBadRequest)
		return
	}
	tenantName := defaultPersonalTenantName(acct.String())
	if accountType == accountTypeOrganization {
		if err := validateOrgName(strings.TrimSpace(body.OrgName)); err != nil {
			http.Error(w, "invalid org_name: "+err.Error(), http.StatusBadRequest)
			return
		}
		tenantName = strings.TrimSpace(body.OrgName)
	}
	if d.jwtSecret == "" {
		http.Error(w, "email registration is not enabled", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()

	exists, err := d.store.accountExists(ctx, acct)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if exists {
		writeJSON(w, http.StatusConflict, map[string]any{"code": "registration_failed", "message": "registration failed"})
		return
	}

	hash, err := hashPassword(body.Password, d.pepper)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	tenantID := uuid.New().String()
	userID := uuid.New().String()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, `INSERT INTO tenants(id, name, status, type) VALUES($1,$2,'active',$3)`, tenantID, tenantName, accountType); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var emailArg, phoneArg *string
	if acct.Kind == accountKindEmail {
		emailArg = &acct.Email
	} else {
		phoneArg = &acct.Phone
	}
	if _, err := tx.Exec(ctx, `INSERT INTO users(id, email, phone) VALUES($1,$2,$3)`, userID, emailArg, phoneArg); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	profilePhone := body.Phone
	if acct.Kind == accountKindPhone {
		profilePhone = acct.Phone
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO user_profiles (user_id, username, full_name, gender, birth_date, phone)
		 VALUES ($1, NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, '')::date, NULLIF($6, ''))
		 ON CONFLICT (user_id) DO NOTHING`,
		userID, body.Username, body.FullName, body.Gender, body.BirthDate, profilePhone); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(ctx, `INSERT INTO tenant_members(tenant_id, user_id, role) VALUES($1,$2,'owner')`, tenantID, userID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO credentials(tenant_id, user_id, email, phone, password_hash, algo) VALUES($1,$2,$3,$4,$5,'bcrypt')`,
		tenantID, userID, emailArg, phoneArg, hash); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(context.Background()); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// 邮箱账号：签发验证令牌并发信（发信失败不影响注册成功，用户可稍后重发）。
	if acct.Kind == accountKindEmail {
		if token, err := d.store.issueToken(ctx, userID, purposeEmailVerify, emailVerifyTTL); err == nil {
			d.sendVerifyEmail(ctx, acct.Email, token, d.publicBase(r))
		} else if d.logger != nil {
			d.logger.Printf("auth: issue verify token for %s failed: %v", userID, err)
		}
	}

	token, err := issueToken(tenantID, userID, 0, d.jwtSecret)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	d.setSessionCookie(w, r, token)
	writeJSON(w, http.StatusCreated, map[string]any{
		"access_token":   token,
		"tenant_id":      tenantID,
		"tenant_name":    tenantName,
		"tenant_type":    accountType,
		"user_id":        userID,
		"account":        acct.String(),
		"account_kind":   accountKindName(acct.Kind),
		"email_verified": acct.Kind != accountKindEmail,
	})
}

func accountKindName(k accountKind) string {
	if k == accountKindPhone {
		return "phone"
	}
	return "email"
}

// ---------------------------------------------------------------------------
// 登录 / 登出
// ---------------------------------------------------------------------------

func (d *authDeps) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !d.limits.login.allow(d.ip(r)) {
		d.tooMany(w)
		return
	}
	var body struct {
		Account  string `json:"account"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	raw := body.Account
	if strings.TrimSpace(raw) == "" {
		raw = body.Email
	}
	acct, err := classifyAccount(raw)
	if err != nil {
		http.Error(w, "invalid account", http.StatusBadRequest)
		return
	}
	if d.jwtSecret == "" {
		http.Error(w, "email authentication is not enabled", http.StatusServiceUnavailable)
		return
	}
	key := acct.String()
	if d.limits.loginFail.locked(key) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"code": "account_locked", "message": "too many failed attempts, please try again later",
		})
		return
	}
	ctx := r.Context()
	row, err := d.store.lookupLogin(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = verifyPassword(d.dummy, body.Password, d.pepper)
		d.limits.loginFail.fail(key)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "invalid_credentials", "message": "invalid account or password"})
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !verifyPassword(row.Hash, body.Password, d.pepper) {
		d.limits.loginFail.fail(key)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "invalid_credentials", "message": "invalid account or password"})
		return
	}
	if row.userAccount.Status != "active" {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "account_disabled", "message": "account is disabled"})
		return
	}
	emailVerified := row.userAccount.EmailVerified
	isEmailAccount := row.userAccount.Email != ""
	if isEmailAccount && !emailVerified && d.requireEmailVerified {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"code": "email_unverified", "message": "please verify your email before signing in",
		})
		return
	}
	d.limits.loginFail.reset(key)

	token, err := issueToken(row.TenantID, row.UserID, row.userAccount.TokenVersion, d.jwtSecret)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	d.setSessionCookie(w, r, token)
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":   token,
		"tenant_id":      row.TenantID,
		"tenant_name":    row.TenantName,
		"tenant_type":    row.TenantType,
		"user_id":        row.UserID,
		"account":        key,
		"account_kind":   accountKindName(acct.Kind),
		"email_verified": !isEmailAccount || emailVerified,
	})
}

func (d *authDeps) handleLogout(w http.ResponseWriter, _ *http.Request) {
	// 无状态 JWT：清除 Cookie 即登出当前会话。全端登出/改密通过提升 token_version 实现。
	d.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// 邮箱验证
// ---------------------------------------------------------------------------

func (d *authDeps) handleVerifyEmail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.Token) == "" {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	userID, err := d.store.consumeToken(r.Context(), strings.TrimSpace(body.Token), purposeEmailVerify)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_token", "message": "verification link is invalid or expired"})
		return
	}
	if err := d.store.markEmailVerified(r.Context(), userID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"email_verified": true})
}

func (d *authDeps) handleResendVerification(w http.ResponseWriter, r *http.Request) {
	if !d.limits.resend.allow(d.ip(r)) {
		d.tooMany(w)
		return
	}
	var body struct {
		Account string `json:"account"`
		Email   string `json:"email"`
	}
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	raw := body.Account
	if strings.TrimSpace(raw) == "" {
		raw = body.Email
	}
	acct, err := classifyAccount(raw)
	if err != nil || acct.Kind != accountKindEmail {
		// 统一 200，不泄露账号是否存在（R-16）。
		writeJSON(w, http.StatusOK, map[string]any{"sent": true})
		return
	}
	ctx := r.Context()
	var userID string
	var verified *time.Time
	err = d.pool.QueryRow(ctx, `SELECT id, email_verified_at FROM users WHERE email=$1`, acct.Email).Scan(&userID, &verified)
	if err == nil && verified == nil {
		if token, ierr := d.store.issueToken(ctx, userID, purposeEmailVerify, emailVerifyTTL); ierr == nil {
			d.sendVerifyEmail(ctx, acct.Email, token, d.publicBase(r))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true})
}

// ---------------------------------------------------------------------------
// 密码重置
// ---------------------------------------------------------------------------

func (d *authDeps) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	if !d.limits.forgot.allow(d.ip(r)) {
		d.tooMany(w)
		return
	}
	var body struct {
		Account string `json:"account"`
		Email   string `json:"email"`
	}
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	raw := body.Account
	if strings.TrimSpace(raw) == "" {
		raw = body.Email
	}
	// 无论账号是否存在都返回相同响应（R-16 防枚举）。
	defer writeJSON(w, http.StatusOK, map[string]any{"sent": true})
	acct, err := classifyAccount(raw)
	if err != nil || acct.Kind != accountKindEmail {
		return // 手机号暂无短信通道，无法自助重置
	}
	ctx := r.Context()
	var userID string
	if err := d.pool.QueryRow(ctx, `SELECT id FROM users WHERE email=$1`, acct.Email).Scan(&userID); err != nil {
		return
	}
	if token, ierr := d.store.issueToken(ctx, userID, purposePasswordReset, passwordResetTTL); ierr == nil {
		d.sendResetEmail(ctx, acct.Email, token, d.publicBase(r))
	}
}

func (d *authDeps) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	if !d.limits.forgot.allow(d.ip(r)) {
		d.tooMany(w)
		return
	}
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.Token) == "" {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	userID, err := d.store.consumeToken(ctx, strings.TrimSpace(body.Token), purposePasswordReset)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_token", "message": "reset link is invalid or expired"})
		return
	}
	u, err := d.store.userByID(ctx, userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	local := u.Email
	if at := strings.IndexByte(local, '@'); at > 0 {
		local = local[:at]
	}
	if err := validatePassword(body.Password, local); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "weak_password", "message": err.Error()})
		return
	}
	hash, err := hashPassword(body.Password, d.pepper)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := d.store.updatePassword(ctx, userID, hash); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// 提升会话代次：重置后所有旧会话失效，需重新登录。
	_ = d.store.bumpTokenVersion(ctx, userID)
	d.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"reset": true})
}

// ---------------------------------------------------------------------------
// 能力探测
// ---------------------------------------------------------------------------

func (d *authDeps) handleAuthConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"email_password":         d.jwtSecret != "",
		"mail_configured":        d.mailer != nil,
		"require_email_verified": d.requireEmailVerified,
	})
}
