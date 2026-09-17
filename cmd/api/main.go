package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/F31/ppts/internal/api"
	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/objectstore/storefactory"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/migrate"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/observability"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/pricing"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/pronunciation"
	"github.com/F31/ppts/internal/tenant"
	"github.com/F31/ppts/internal/upload"
	"github.com/F31/ppts/internal/usage"
	"github.com/F31/ppts/migrations"
	"github.com/F31/ppts/web"
	"github.com/jackc/pgx/v5/pgxpool"
)

// version 由发布构建经 -ldflags "-X main.version=..." 注入；源码构建为 dev。
var version = "dev"

func main() {
	log.SetFlags(0)
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "migrate":
			if err := runMigrate(); err != nil {
				log.Fatal(err)
			}
			return
		case "version":
			fmt.Printf("ppts-api %s\n", version)
			return
		}
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// runMigrate 应用全部内嵌 SQL 迁移（幂等）。迁移含建角色/扩展，需要 superuser 连接：
// 优先取 PPTS_MIGRATE_DATABASE_URL，回退 PPTS_DATABASE_URL。
func runMigrate() error {
	dsn := os.Getenv("PPTS_MIGRATE_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("PPTS_DATABASE_URL")
	}
	if dsn == "" {
		return errors.New("PPTS_MIGRATE_DATABASE_URL or PPTS_DATABASE_URL is required")
	}
	applied, err := migrate.Apply(context.Background(), dsn, migrations.FS)
	if err != nil {
		return err
	}
	for _, name := range applied {
		log.Printf("migrate: applied %s", name)
	}
	log.Printf("migrate: ok, %d applied, schema up to date", len(applied))
	return nil
}

func run() error {
	dsn := os.Getenv("PPTS_DATABASE_URL")
	if dsn == "" {
		return errors.New("PPTS_DATABASE_URL is required")
	}
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
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return err
	}
	authenticator, err := authFromEnv(ctx)
	if err != nil {
		return err
	}
	jobs, err := pipeline.NewPGStore(ctx, dsn)
	if err != nil {
		return err
	}
	defer jobs.Close()

	policyStore := tenant.NewPGStore(pool)
	registry, err := storefactory.FromEnv(policyStore)
	if err != nil {
		return err
	}
	objects := objectstore.WithInventory(registry, policyStore)
	objects, err = storefactory.WithEnvelopeEncryptionFromEnv(objects, policyStore)
	if err != nil {
		return err
	}
	priceBook, err := pricing.FromEnv()
	if err != nil {
		return err
	}
	usageStore := usage.NewPGStore(pool).WithPriceBook(priceBook)
	auditStore := audit.NewPGStore(pool)
	membersStore := membership.NewPGStore(pool)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	gatewayStore := gatewayStoreFromEnv(ctx, logger, pool)
	server := &http.Server{
		Addr: addr,
		Handler: observability.RequestLogger(
			api.NewHandler(project.NewPGProjectStore(pool), upload.NewPGUploadStore(pool),
				narration.NewPGStore(pool), jobs, artifact.NewPGStore(pool),
				objects, pool,
				api.Options{Quota: usageStore, Usage: usageStore, Policy: policyStore, Audit: auditStore, Members: membersStore, Lifecycle: policyStore, Storage: policyStore, Archive: policyStore, TenantStatus: policyStore, Auth: authenticator, DevHeaders: os.Getenv("PPTS_AUTH_DEV_HEADERS") == "true", Pronunciation: pronunciation.NewPGStore(pool), Gateway: gatewayStore, JWTSecret: os.Getenv("PPTS_JWT_SECRET"), PasswordPepper: os.Getenv("PPTS_PASSWORD_PEPPER"), WebRoot: os.Getenv("PPTS_WEB_ROOT"), WebFS: web.DistFS()}),
			logger),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Printf("api %s listening on %s", version, addr)
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

func gatewayStoreFromEnv(ctx context.Context, logger *slog.Logger, pool *pgxpool.Pool) gateway.StoreResolver {
	cipher, err := gateway.CipherFromEnv()
	if err != nil {
		logger.Info("model gateway disabled (no AES key)")
		return nil
	}
	store := gateway.NewPGStore(pool, cipher)
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
