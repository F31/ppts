package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/F31/ppts/internal/api"
	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/observability"
	"github.com/F31/ppts/internal/pricing"
	"github.com/F31/ppts/web"
)

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

	authenticator, err := authFromEnv(ctx)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	gatewayStore := gatewayStoreFromEnv(ctx, logger, stores)

	// SQLite 单租户模式：本地固定身份、无登录；不挂载 auth/public 路由（pool 传 nil）。
	localPrincipal := stores.localPrincipalFor()
	handlerPool := stores.pg // sqlite 下为 nil → api 跳过 auth/public 路由
	scriptSources := stores.scriptSourceStore()
	voiceSettings := stores.voiceSettingsStore()

	server := &http.Server{
		Addr: addr,
		Handler: observability.RequestLogger(
			api.NewHandler(stores.projects, stores.uploads, stores.scripts, stores.jobs,
				stores.artifacts, stores.objects, handlerPool,
				api.Options{
					Quota: stores.usage, Usage: stores.usage, Policy: stores.tenant,
					Audit: stores.audit, Members: stores.members,
					Lifecycle: stores.tenant, Storage: stores.tenant,
					Archive: stores.tenant, TenantStatus: stores.tenant,
					Auth:           authenticator,
					DevHeaders:     os.Getenv("PPTS_AUTH_DEV_HEADERS") == "true",
					LocalPrincipal: localPrincipal,
					Pronunciation:  stores.pronunciation, Gateway: gatewayStore, ScriptSources: scriptSources, VoiceSettings: voiceSettings,
					JWTSecret: os.Getenv("PPTS_JWT_SECRET"), PasswordPepper: os.Getenv("PPTS_PASSWORD_PEPPER"),
					WebRoot: os.Getenv("PPTS_WEB_ROOT"), WebFS: web.DistFS(),
				}),
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

// authFromEnv 组合 OIDC（external IdP bearer）与自签名 JWT（邮箱注册，HS256）。
// 两者皆未配置时返回 nil（退化为开发头，行为同前）。
func authFromEnv(ctx context.Context) (api.Authenticator, error) {
	oidc, err := oidcAuthenticatorFromEnv(ctx)
	if err != nil {
		return nil, err
	}
	secret := os.Getenv("PPTS_JWT_SECRET")
	var jwtAuth *api.JWTAuthenticator
	if secret != "" {
		jwtAuth = api.NewJWTAuthenticator(secret)
	}
	if oidc == nil && jwtAuth == nil {
		return nil, nil
	}
	return api.NewCombinedAuthenticator(oidc, jwtAuth), nil
}
