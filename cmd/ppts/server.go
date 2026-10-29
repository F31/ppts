package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/api"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/mail"
	"github.com/F31/ppts/internal/messaging"
	"github.com/F31/ppts/internal/observability"
	"github.com/F31/ppts/internal/pricing"
	"github.com/F31/ppts/internal/render"
	"github.com/F31/ppts/web"
)

// animationEnabled 决定动画通道是否开启，api 与 worker 共用这一份判定。
//
// 本地 profile（SQLite 单租户）默认开、生产默认关，但**显式设置永远优先**：
// 之前 api 写的是 `isSQLite() || env=="true"`，worker 写的是 `env=="true"`，
// 两边不一致的结果是"API 说能出动画、worker 却只出静态版"——用户看到的是静默降级，
// 而不是任何一条提示。更要紧的是，本地 profile 因此没有任何办法关掉动画，
// 验收里"关掉开关再导出"这一步根本做不出来。
func animationEnabled(isSQLite bool) bool {
	return envBool("PPTS_ANIMATION_ENABLED", isSQLite)
}

// animationProbeFromEnv 构造导出选项端点的渲染服务探测。
//
// 与 worker 各自读同一对 env（PPTS_ANIMATION_ENABLED / PPTS_RENDERER_URL）：两个进程
// 本来就是分开部署的，共享一份"配置"只能靠同一份 compose/env_file。这里刻意不跨进程
// 传能力——API 探不通就说明此刻那个地址对它不可达，而这正是"能不能出动画"要回答的问题。
//
// 返回 nil 表示这个部署没有渲染服务：端点会返回 renderer_not_configured，
// 前端据此置灰并说明，而不是让用户勾一个必然降级的选项。
func animationProbeFromEnv(logger *slog.Logger, enabled bool) api.AnimationProbe {
	if !enabled {
		return nil
	}
	base := strings.TrimSpace(os.Getenv("PPTS_RENDERER_URL"))
	if base == "" {
		logger.Warn("animation: PPTS_ANIMATION_ENABLED is set but PPTS_RENDERER_URL is empty; export options will report unavailable")
		return nil
	}
	client, err := render.NewClient(render.ClientConfig{
		BaseURL: base,
		// 探测要快：导出对话框打开时才问，等 30s 等于对话框卡死。
		Timeout: 3 * time.Second,
		Retries: 1,
	})
	if err != nil {
		logger.Warn("animation: renderer probe disabled", "error", err)
		return nil
	}
	return client
}

// animationPreviewFromEnv 构造单页预览的两个硬依赖：渲染客户端与页图投递。
//
// 缺任一就整块返回 nil（= 不注册预览路由）。预览刻意不允许"半开"：
// 只配了渲染地址却没配资产目录时，提交能成功而 baseImage 永远找不到，
// 用户看到的是"预览失败"而不是"这个部署没开预览"——前者会被当成 bug 反复重试。
//
// 会话预算（默认 3s，PPTS_ANIMATION_PREVIEW_BUDGET 可调）在 handler 里，
// 客户端超时给得比它宽：让 ctx 先到期，这样超时被归因为"这一页渲得慢"，
// 而不是"渲染服务连不上"。
func animationPreviewFromEnv(logger *slog.Logger, enabled bool) (*render.Client, app.AssetPublisher) {
	if !enabled {
		return nil, nil
	}
	base := strings.TrimSpace(os.Getenv("PPTS_RENDERER_URL"))
	dir := strings.TrimSpace(os.Getenv("PPTS_ANIMATION_ASSET_DIR"))
	if base == "" || dir == "" {
		logger.Info("animation: preview disabled (needs both PPTS_RENDERER_URL and PPTS_ANIMATION_ASSET_DIR)")
		return nil, nil
	}
	client, err := render.NewClient(render.ClientConfig{BaseURL: base, Timeout: 10 * time.Second, Retries: 1})
	if err != nil {
		logger.Warn("animation: preview disabled", "error", err)
		return nil, nil
	}
	assets, err := app.NewLocalAssetPublisher(dir)
	if err != nil {
		logger.Warn("animation: preview disabled", "error", err)
		return nil, nil
	}
	return client, assets
}

// previewBudgetFromEnv 读取单页预览预算 PPTS_ANIMATION_PREVIEW_BUDGET（Go duration，如 "8s"）。
//
// 非法或缺失一律回落到 0（= 用 handler 的默认 3s）而不是报错退出：预览是增强能力，
// 一个写错的环境变量不该让整个 API 起不来。上下限夹紧交给 api.ClampPreviewBudget，
// 保证服务端预算始终小于前端 90s 的兜底超时（否则超时会被误判成网络故障）。
func previewBudgetFromEnv(logger *slog.Logger) time.Duration {
	raw := strings.TrimSpace(os.Getenv("PPTS_ANIMATION_PREVIEW_BUDGET"))
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		logger.Warn("animation: invalid PPTS_ANIMATION_PREVIEW_BUDGET, falling back to default",
			"value", raw, "error", err)
		return 0
	}
	clamped := api.ClampPreviewBudget(d)
	if clamped != d {
		logger.Warn("animation: PPTS_ANIMATION_PREVIEW_BUDGET clamped",
			"requested", d.String(), "applied", clamped.String())
	}
	return clamped
}

// runServer 启动 API + 前端控制台（原 cmd/api 的 run）。
// 数据库由 PPTS_DB_DRIVER 选择：sqlite（默认，单租户本地模式，无登录）或 postgres（多租户全功能）。
func runServer() error {
	addr := os.Getenv("PPTS_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	traceShutdown, err := observability.InitTracing()
	if err != nil {
		return err
	}
	defer func() { _ = traceShutdown(ctx) }()

	stores, err := openStores(ctx)
	if err != nil {
		return err
	}
	defer stores.close()

	priceBook, err := pricing.FromEnv()
	if err != nil {
		return err
	}
	stores.applyPriceBook(priceBook)

	configureJWTTTLFromEnv()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	stdLogger := slog.NewLogLogger(logger.Handler(), slog.LevelInfo)
	authenticator, err := authFromEnv(ctx, stores.pg)
	if err != nil {
		return err
	}
	mailer, err := mail.FromEnv(stdLogger)
	if err != nil {
		return err
	}
	if mailer == nil {
		logger.Info("mail disabled: verification/reset links will be logged, not emailed")
	}
	gatewayStore := gatewayStoreFromEnv(ctx, logger, stores)
	messagingStore := messagingStoreFromEnv(logger, stores)

	// SQLite 单租户模式：本地固定身份、无登录；不挂载 auth/public 路由（pool 传 nil）。
	localPrincipal := stores.localPrincipalFor()
	handlerPool := stores.pg // sqlite 下为 nil → api 跳过 auth/public 路由
	scriptSources := stores.scriptSourceStore()
	voiceSettings := stores.voiceSettingsStore()

	// 动画开关只解析一次：api 的三个入口（写路径、探测、预览）必须同进同退，
	// 各自读一遍环境变量就会出现"对话框说能用、预览端点却没注册"这种半开状态。
	animationOn := animationEnabled(stores.isSQLite())
	previewRenderer, previewAssets := animationPreviewFromEnv(logger, animationOn)

	server := &http.Server{
		Addr: addr,
		Handler: observability.RequestLogger(
			api.SecurityHeaders(
				api.NewHandler(stores.projects, stores.uploads, stores.scripts, stores.jobs,
					stores.artifacts, stores.objects, handlerPool,
					api.Options{
						Quota: stores.usage, UsageStore: stores.usage, Usage: stores.usage, Policy: stores.tenant,
						Audit: stores.audit, Members: stores.members,
						Lifecycle: stores.tenant, Storage: stores.tenant,
						Archive: stores.tenant, TenantStatus: stores.tenant,
						Auth:           authenticator,
						DevHeaders:     os.Getenv("PPTS_AUTH_DEV_HEADERS") == "true",
						LocalPrincipal: localPrincipal,
						Pronunciation:  stores.pronunciation, ContextRule: stores.contextRule, Gateway: gatewayStore, ScriptSources: scriptSources, VoiceSettings: voiceSettings,
						// 动画规格写路径：本地 profile 默认开启；生产需显式设置 PPTS_ANIMATION_ENABLED=true。
						Animation: stores.animation, AnimationEnabled: animationOn,
						AnimationProbe:    animationProbeFromEnv(logger, animationOn),
						AnimationRenderer: previewRenderer, AnimationAssets: previewAssets,
						// 预览预算按部署硬件可调（默认 3s）：纯 CPU 渲染器上一页的耗时
						// 取决于核数与字号，固定值会让慢机器上的预览恒定超时。
						AnimationPreviewBudget: previewBudgetFromEnv(logger),
						JWTSecret:              os.Getenv("PPTS_JWT_SECRET"), PasswordPepper: os.Getenv("PPTS_PASSWORD_PEPPER"),
						Mailer:               mailer,
						TrustProxy:           os.Getenv("PPTS_TRUST_PROXY") == "true",
						RequireEmailVerified: os.Getenv("PPTS_REQUIRE_EMAIL_VERIFIED") == "true",
						Logger:               stdLogger,
						PriceVersion:         priceBook.Version,
						Messages:             messagingStore,
						WebRoot:              os.Getenv("PPTS_WEB_ROOT"), WebFS: web.DistFS(),
					}),
			),
			logger),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("ppts server %s listening on %s (driver=%s)", version, addr, stores.cfg.Driver)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// gatewayStoreFromEnv 按驱动构建模型网关存储；AES 密钥未配置时返回 nil（回退 env 供应商）。
func gatewayStoreFromEnv(ctx context.Context, logger *slog.Logger, stores *storeSet) gateway.StoreResolver {
	cipher, err := gateway.CipherFromEnv()
	if err != nil {
		logger.Info("model gateway disabled (no AES key); using env providers")
		return nil
	}
	store := stores.gatewayStore(cipher)
	if err := gateway.SeedFromEnv(ctx, store, gateway.EnvFromEnv()); err != nil {
		logger.Warn("gateway seed from env failed", "error", err)
	}
	return store
}

// messagingStoreFromEnv 构建消息服务（发件箱/短信）存储；AES 密钥未配置或非 PG 时返回 nil
// （相关端点返回 503，认证邮件回退 env/log mailer）。
func messagingStoreFromEnv(logger *slog.Logger, stores *storeSet) messaging.Store {
	if stores.pg == nil {
		return nil
	}
	cipher, err := gateway.CipherFromEnv()
	if err != nil {
		logger.Info("message service disabled (no AES key)")
		return nil
	}
	return messaging.NewPGStore(stores.pg, cipher)
}

func oidcAuthenticatorFromEnv(ctx context.Context) (*api.OIDCAuthenticator, error) {
	issuer := os.Getenv("PPTS_OIDC_ISSUER")
	if issuer == "" {
		return nil, nil
	}
	return api.NewOIDCAuthenticator(ctx, api.OIDCConfig{
		Issuer:      issuer,
		ClientID:    os.Getenv("PPTS_OIDC_CLIENT_ID"),
		TenantClaim: os.Getenv("PPTS_OIDC_TENANT_CLAIM"),
		UserClaim:   os.Getenv("PPTS_OIDC_USER_CLAIM"),
	})
}

// authFromEnv 组合 OIDC（external IdP bearer）与自签名 JWT（邮箱/手机注册，HS256）。
// 两者皆未配置时返回 nil（退化为开发头，行为同前）。pool 非空时为 JWT 注入会话校验
// （改密/全端登出后旧令牌立即失效）。
func authFromEnv(ctx context.Context, pool *pgxpool.Pool) (api.Authenticator, error) {
	oidc, err := oidcAuthenticatorFromEnv(ctx)
	if err != nil {
		return nil, err
	}
	secret := os.Getenv("PPTS_JWT_SECRET")
	var jwtAuth *api.JWTAuthenticator
	if secret != "" {
		jwtAuth = api.NewJWTAuthenticator(secret)
		if pool != nil {
			jwtAuth.WithSessionValidator(api.NewPGSessionValidator(pool))
		}
	}
	if oidc == nil && jwtAuth == nil {
		// fail-closed：没有任何真实认证手段时**不允许**静默放行开发头。
		// 此前这里直接 return nil,nil，下游 (api/server.go) 又以 auth==nil 为由自动开启
		// dev headers，于是生产环境漏配 PPTS_JWT_SECRET 时，任意请求带上
		// X-PPTS-Tenant-ID + X-PPTS-User-ID 即可冒充任意租户的任意用户，且无任何告警。
		if !envBool("PPTS_AUTH_DEV_HEADERS", false) {
			return nil, errors.New("no authenticator configured: set PPTS_JWT_SECRET (or PPTS_OIDC_ISSUER); " +
				"set PPTS_AUTH_DEV_HEADERS=true only for local development (it accepts unsigned tenant/user headers)")
		}
		return nil, nil
	}
	return api.NewCombinedAuthenticator(oidc, jwtAuth), nil
}

// configureJWTTTLFromEnv 从 PPTS_JWT_TTL 解析会话有效期（如 "24h"）；非法/缺失用默认 24h。
func configureJWTTTLFromEnv() {
	raw := strings.TrimSpace(os.Getenv("PPTS_JWT_TTL"))
	if raw == "" {
		return
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		api.SetJWTTTL(d)
	}
}
