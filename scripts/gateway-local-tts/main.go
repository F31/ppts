// Command gateway-local-tts 幂等地把「本机 OpenAI 兼容语音模型」写入模型网关表。
//
// 为什么需要工具而不是 INSERT：model_gateways.encrypted_creds 是 AES-GCM 密文，
// AAD 绑定 "ppts:gateway:<tenantID>:<name>:<kind>"。裸 SQL 插一行会得到
// 「有配置但凭据不可读」的假状态；用目标实例自己的密钥写，运行时才解得开。
// 本机服务（Qwen3-TTS / liteaig 一类）不校验 API Key，所以本工具写入空凭据，
// 管理面如实显示「本机服务免鉴权」，而不是伪造一个 key。
//
// 用法（密钥只走环境变量或 config.env，不出现在命令行参数与输出里）：
//
//	DST_DB=~/ppts-preview/data/ppts.db PPTS_CONFIG=~/ppts-preview/config.env \
//		go run ./scripts/gateway-local-tts
//
//	# 或显式给密钥（base64，32 字节）
//	DST_DB=/path/ppts.db DST_AES_KEY=<base64> go run ./scripts/gateway-local-tts
//
// 可选：PPTS_LOCAL_TTS_NAME（默认 local_qwen_tts）、PPTS_LOCAL_TTS_BASE_URL
// （默认 http://127.0.0.1:8000）、PPTS_LOCAL_TTS_MODEL、PPTS_LOCAL_TTS_VOICE、
// PPTS_LOCAL_TTS_SET_DEFAULT=0（不抢占现有默认网关）、DRY_RUN=1（只打印计划）。
// 变量名一律带前缀：裸 NAME / MODEL 会和宿主机的系统环境变量撞车
// （实测 WSL 里 NAME=<hostname>），曾把网关行名预演成主机名。
// 幂等：同名 (kind,name) 已存在时只更新配置字段并保留原凭据密文。
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
	_ "modernc.org/sqlite"
)

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// keyFromConfig 从 config.env 读取 AES 密钥，避免把密钥放到命令行里。
func keyFromConfig(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "PPTS_GATEWAY_AES_KEY_BASE64=") {
			continue
		}
		return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "PPTS_GATEWAY_AES_KEY_BASE64=")), `"'`), nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s 里没有 PPTS_GATEWAY_AES_KEY_BASE64", path)
}

func main() {
	dbPath := env("DST_DB", "")
	if dbPath == "" {
		log.Fatal("需要 DST_DB（目标 SQLite 库路径）")
	}
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

	name := env("PPTS_LOCAL_TTS_NAME", "local_qwen_tts")
	baseURL := strings.TrimRight(env("PPTS_LOCAL_TTS_BASE_URL", "http://127.0.0.1:8000"), "/")
	model := env("PPTS_LOCAL_TTS_MODEL", "qwen3-tts-1.7b-customvoice")
	voice := env("PPTS_LOCAL_TTS_VOICE", "vivian")
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

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	store := gateway.NewSQLiteStore(db, cipher)
	ctx := context.Background()

	gw := &gateway.Gateway{
		TenantID:  gateway.PlatformTenantID,
		Name:      name,
		Kind:      gateway.KindTTS,
		Provider:  "openai_compatible",
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

	fmt.Printf("gateway-local-tts: %s tenant=%s name=%s baseURL=%s model=%s voice=%s isDefault=%v hasKey=false(本机免鉴权)\n",
		action, gw.TenantID, gw.Name, gw.BaseURL, gw.Model, gw.Voice, gw.IsDefault)
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
	fmt.Printf("gateway-local-tts: 已写入 %s（平台共享，凭据为空）\n", dbPath)
}
