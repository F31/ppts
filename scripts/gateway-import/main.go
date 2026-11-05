// Command gateway-import 把一份 SQLite 库里已配置的模型网关（含 AES 加密的 API Key）
// 迁移到另一份库，并可改为平台共享租户。
//
// 为什么不能 INSERT ... SELECT：凭据以 AES-GCM 加密，AAD 绑定
// "ppts:gateway:<tenantID>:<name>:<kind>"。换实例（AES 密钥不同）或换租户都会让
// 旧密文永久解不开；直接搬行会得到「有配置但 hasKey=false」的假状态，运行时
// Resolve 才报解密失败。本工具用源密钥解密、目标密钥重新加密，明文只在进程内。
//
// 用法（密钥全部走环境变量，不出现在命令行参数或输出里）：
//
//	SRC_DB=/path/src.db DST_DB=/path/dst.db \
//	SRC_AES_KEY=<base64> DST_AES_KEY=<base64> \
//	go run ./scripts/gateway-import
//
// 可选：SRC_TENANT_ID（默认开发租户）、DST_TENANT_ID（默认平台共享租户）、DRY_RUN=1。
// 幂等：目标库已存在同名 (kind,name) 时跳过；只迁移各 kind 的默认行（即运行时生效的那条）。
package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/tenant"
	_ "modernc.org/sqlite"
)

const devTenantID = "00000000-0000-0000-0000-000000000001"

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func cipherFrom(b64 string) (tenant.CredentialCipher, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("decode base64 AES key: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("AES key must decode to 32 bytes, got %d", len(raw))
	}
	return tenant.NewAESGCMCipher(raw)
}

func main() {
	srcDBPath := env("SRC_DB", "")
	dstDBPath := env("DST_DB", "")
	srcKey := env("SRC_AES_KEY", "")
	dstKey := env("DST_AES_KEY", "")
	if srcDBPath == "" || dstDBPath == "" || srcKey == "" || dstKey == "" {
		log.Fatal("需要 SRC_DB / DST_DB / SRC_AES_KEY / DST_AES_KEY 四个环境变量")
	}
	srcTenant := env("SRC_TENANT_ID", devTenantID)
	dstTenant := env("DST_TENANT_ID", gateway.PlatformTenantID)
	dryRun := env("DRY_RUN", "") != ""

	srcCipher, err := cipherFrom(srcKey)
	if err != nil {
		log.Fatalf("源密钥无效: %v", err)
	}
	dstCipher, err := cipherFrom(dstKey)
	if err != nil {
		log.Fatalf("目标密钥无效: %v", err)
	}

	srcDB, err := sql.Open("sqlite", srcDBPath)
	if err != nil {
		log.Fatalf("打开源库: %v", err)
	}
	defer srcDB.Close()
	dstDB, err := sql.Open("sqlite", dstDBPath)
	if err != nil {
		log.Fatalf("打开目标库: %v", err)
	}
	defer dstDB.Close()

	ctx := context.Background()
	srcStore := gateway.NewSQLiteStore(srcDB, srcCipher)
	dstStore := gateway.NewSQLiteStore(dstDB, dstCipher)

	prefix := "imported"
	if dryRun {
		prefix = "would import"
	}
	count := 0
	for _, kind := range []gateway.Kind{gateway.KindTTS, gateway.KindLLM} {
		cfg, err := srcStore.Resolve(ctx, srcTenant, kind)
		if errors.Is(err, gateway.ErrNotFound) {
			fmt.Printf("skip %s: 源库没有默认配置\n", kind)
			continue
		}
		if err != nil {
			log.Fatalf("读取源库 %s 默认网关失败（源 AES 密钥是否正确？）: %v", kind, err)
		}
		if _, err := dstStore.Get(ctx, dstTenant, cfg.Name, kind); err == nil {
			fmt.Printf("skip %s/%s: 目标库已存在（幂等）\n", kind, cfg.Name)
			continue
		} else if !errors.Is(err, gateway.ErrNotFound) {
			log.Fatalf("检查目标库 %s/%s 失败: %v", kind, cfg.Name, err)
		}
		// 只输出非敏感元数据；明文 key 仅以长度示意，绝不回显。
		fmt.Printf("%s %s/%s -> %s (baseURL=%s, model=%s, keyBytes=%d)\n",
			prefix, kind, cfg.Name, dstTenant, cfg.BaseURL, cfg.Model, len(cfg.APIKey))
		if dryRun {
			count++
			continue
		}
		gw := &gateway.Gateway{
			TenantID: dstTenant, Name: cfg.Name, Kind: cfg.Kind,
			Provider: "openai_compatible", BaseURL: cfg.BaseURL,
			Model: cfg.Model, VisionModel: cfg.VisionModel, Voice: cfg.Voice,
			SampleRate: cfg.SampleRate, IsDefault: true, Enabled: true,
		}
		if err := dstStore.Create(ctx, gw, cfg.APIKey); err != nil {
			log.Fatalf("写入目标库 %s/%s 失败: %v", kind, cfg.Name, err)
		}
		count++
	}
	fmt.Printf("done: %d 条（dstTenant=%s, dryRun=%v）\n", count, dstTenant, dryRun)
}
