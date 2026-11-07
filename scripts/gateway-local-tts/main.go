// Command gateway-local-tts 幂等地把「本机 OpenAI 兼容语音模型」写入模型网关表。
//
// 为什么需要工具而不是 INSERT：model_gateways.encrypted_creds 是 AES-GCM 密文，
// AAD 绑定 "ppts:gateway:<tenantID>:<name>:<kind>"。裸 SQL 插一行会得到
// 「有配置但凭据不可读」的假状态；用目标实例自己的密钥写，运行时才解得开。
// 本机服务（Qwen3-TTS 一类）不校验 API Key，所以本工具写入空凭据，
// 管理面如实显示「本机服务免鉴权」，而不是伪造一个 key。
//
// 支持两种目标库；不同实例的 AES 密钥往往不同，必须各自用自己的密钥写：
//
//	# SQLite（预览实例）
//	DST_DB=~/ppts-preview/data/ppts.db PPTS_CONFIG=~/ppts-preview/config.env \
//		go run ./scripts/gateway-local-tts
//
//	# Postgres（systemd 主实例；密钥与 DSN 都从 config.env 读，不进命令行）
//	DST_DRIVER=postgres PPTS_CONFIG=/etc/ppts/config.env \
//		go run ./scripts/gateway-local-tts
//
//	# 或显式给密钥/DSN（只走环境变量，不出现在输出里）
//	DST_DRIVER=postgres DST_DSN=<dsn> DST_AES_KEY=<base64> go run ./scripts/gateway-local-tts
//
// 可选：PPTS_LOCAL_TTS_NAME（默认 local_qwen_tts）、PPTS_LOCAL_TTS_BASE_URL
// （默认 http://127.0.0.1:8000）、PPTS_LOCAL_TTS_MODEL、PPTS_LOCAL_TTS_VOICE、
// PPTS_LOCAL_TTS_PROVIDER（默认 openai_compatible；本地 CosyVoice 运行时填 cosyvoice）、
// PPTS_LOCAL_TTS_SET_DEFAULT=0（不抢占现有默认网关）、DRY_RUN=1（只打印计划）。
// 变量名一律带前缀：裸 NAME / MODEL 会和宿主机的系统环境变量撞车
// （实测 WSL 里 NAME=<hostname>），曾把网关行名预演成主机名。
// 幂等：同名 (kind,name) 已存在时只更新配置字段并保留原凭据密文。
// 写入后用目标实例密钥回读解密，解不开就报错，避免留下「有行但不可用」的假状态。
package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/tenant"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite"
)

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// configValue 从 config.env 读取一个值，避免把密钥或 DSN 放到命令行里。
// 逐行解析而不是 source：config.env 里有 NO_PROXY=<local> 这类 shell 不友好的值。
func configValue(path, key string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	prefix := key + "="
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, prefix)), `"'`), nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s 里没有 %s", path, key)
}

func keyFromConfig(path string) (string, error) {
	return configValue(path, "PPTS_GATEWAY_AES_KEY_BASE64")
}

// gatewayWriter 是 SQLiteStore / PGStore 共用的「写入 + 回读」子集。
type gatewayWriter interface {
	Get(ctx context.Context, tenantID, name string, kind gateway.Kind) (*gateway.Gateway, error)
	Create(ctx context.Context, gw *gateway.Gateway, apiKey string) error
	Update(ctx context.Context, gw *gateway.Gateway, apiKey string) error
	SetDefault(ctx context.Context, tenantID, name string, kind gateway.Kind) error
	ResolveNamed(ctx context.Context, tenantID, name string, kind gateway.Kind) (*gateway.Config, error)
}

func openSQLiteStore(ctx context.Context, cipher tenant.CredentialCipher) (gatewayWriter, string, error) {
	dbPath := env("DST_DB", "")
	if dbPath == "" {
		return nil, "", fmt.Errorf("SQLite 目标需要 DST_DB（库文件路径）")
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, "", err
	}
	// 调用方在进程退出时关闭：这里把 *sql.DB 交给 store 后无法再取回，
	// 所以用 Ping 先确认可连，关闭交给进程生命周期（短命 CLI）。
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, "", err
	}
	return gateway.NewSQLiteStore(db, cipher), "sqlite:" + dbPath, nil
}

func openPGStore(ctx context.Context, cipher tenant.CredentialCipher) (gatewayWriter, string, error) {
	dsn := env("DST_DSN", "")
	if dsn == "" {
		cfgPath := env("PPTS_CONFIG", "")
		if cfgPath == "" {
			return nil, "", fmt.Errorf("Postgres 目标需要 DST_DSN 或 PPTS_CONFIG（含 PPTS_DATABASE_URL）")
		}
		var err error
		dsn, err = configValue(cfgPath, "PPTS_DATABASE_URL")
		if err != nil {
			return nil, "", err
		}
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, "", fmt.Errorf("连接 Postgres 失败: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, "", fmt.Errorf("Ping Postgres 失败: %w", err)
	}
	// 同上：短命 CLI，pool 在进程退出时释放；不打印 DSN（含凭据）。
	return gateway.NewPGStore(pool, cipher), "postgres(config)", nil
}

func main() {
	ctx := context.Background()

	keyB64 := env("DST_AES_KEY", "")
	if keyB64 == "" {
		cfgPath := env("PPTS_CONFIG", "")
		if cfgPath == "" {
			log.Fatal("需要 DST_AES_KEY 或 PPTS_CONFIG（含 PPTS_GATEWAY_AES_KEY_BASE64 的 config.env）")
		}
		var err error
		keyB64, err = keyFromConfig(cfgPath)
		if err != nil {
			log.Fatal(err)
		}
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keyB64))
	if err != nil || len(raw) != 32 {
		log.Fatalf("AES 密钥必须解出 32 字节：decode=%v len=%d", err, len(raw))
	}
	cipher, err := tenant.NewAESGCMCipher(raw)
	if err != nil {
		log.Fatal(err)
	}

	driver := strings.ToLower(env("DST_DRIVER", "sqlite"))
	var store gatewayWriter
	var target string
	switch driver {
	case "sqlite":
		store, target, err = openSQLiteStore(ctx, cipher)
	case "postgres":
		store, target, err = openPGStore(ctx, cipher)
	default:
		log.Fatalf("不支持的 DST_DRIVER=%q（只接受 sqlite / postgres）", driver)
	}
	if err != nil {
		log.Fatalf("打开目标库失败: %v", err)
	}

	name := env("PPTS_LOCAL_TTS_NAME", "local_qwen_tts")
	baseURL := strings.TrimRight(env("PPTS_LOCAL_TTS_BASE_URL", "http://127.0.0.1:8000"), "/")
	model := env("PPTS_LOCAL_TTS_MODEL", "qwen3-tts-1.7b-customvoice")
	voice := env("PPTS_LOCAL_TTS_VOICE", "vivian")
	provider := env("PPTS_LOCAL_TTS_PROVIDER", "openai_compatible")
	setDefault := env("PPTS_LOCAL_TTS_SET_DEFAULT", "1") != "0"
	dryRun := env("DRY_RUN", "") != ""

	// 两处防呆：行名必须像标识符（否则宿主环境变量/主机名可能混进来），
	// 空凭据只允许指向本机回环，避免把「忘了配 key 的远程供应商」写成假可用网关。
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(name) {
		log.Fatalf("网关名 %q 不合法，只允许字母数字下划线连字符（检查 PPTS_LOCAL_TTS_NAME 是否被系统变量污染）", name)
	}
	if !tts.IsLoopbackBaseURL(baseURL) {
		log.Fatalf("本机免鉴权网关只接受 http 回环地址，实际 %q；远程供应商请走管理面配置 API Key", baseURL)
	}

	gw := &gateway.Gateway{
		TenantID:  gateway.PlatformTenantID,
		Name:      name,
		Kind:      gateway.KindTTS,
		Provider:  provider,
		BaseURL:   baseURL,
		Model:     model,
		Voice:     voice,
		Enabled:   true,
		IsDefault: setDefault,
	}
	action := "create"
	existing, err := store.Get(ctx, gateway.PlatformTenantID, name, gateway.KindTTS)
	switch {
	case err == nil:
		action = "update"
		gw.Version = existing.Version
		gw.IsDefault = setDefault || existing.IsDefault
	case errors.Is(err, gateway.ErrNotFound):
		// 新建
	default:
		log.Fatalf("读取同名网关失败: %v", err)
	}

	fmt.Printf("gateway-local-tts: %s target=%s tenant=%s name=%s baseURL=%s model=%s voice=%s isDefault=%v hasKey=false(本机免鉴权)\n",
		action, target, gw.TenantID, gw.Name, gw.BaseURL, gw.Model, gw.Voice, gw.IsDefault)
	if dryRun {
		fmt.Println("DRY_RUN=1，未写入")
		return
	}
	if action == "update" {
		// apiKey 为空 = 保留原密文；本机行本来就没有凭据。
		if err := store.Update(ctx, gw, ""); err != nil {
			log.Fatalf("更新网关失败: %v", err)
		}
	} else {
		if err := store.Create(ctx, gw, ""); err != nil {
			log.Fatalf("创建网关失败: %v", err)
		}
	}
	if setDefault {
		if err := store.SetDefault(ctx, gateway.PlatformTenantID, name, gateway.KindTTS); err != nil {
			log.Fatalf("设为平台默认失败: %v", err)
		}
	}

	// 回读解密：目标实例 AES 密钥不对时，行写进去了但运行时解不开，必须当场失败。
	resolved, err := store.ResolveNamed(ctx, gateway.PlatformTenantID, name, gateway.KindTTS)
	if err != nil {
		log.Fatalf("写入后回读失败（目标实例的 AES 密钥是否与 config.env 一致？）: %v", err)
	}
	if resolved.BaseURL != baseURL || resolved.Model != model {
		log.Fatalf("写入后回读不一致: baseURL=%q model=%q", resolved.BaseURL, resolved.Model)
	}
	fmt.Printf("gateway-local-tts: 已写入 %s（平台共享，凭据为空）；回读解密成功 keyBytes=%d\n", target, len(resolved.APIKey))
}
