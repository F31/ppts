//go:build pg

package gateway

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

const gwTenant = "00000000-0000-0000-0000-0000000000e1"

func gwStore(t *testing.T) *PGStore {
	t.Helper()
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		t.Skip("PPTS_TEST_DATABASE not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(),
		"TRUNCATE model_gateways, pronunciation_dictionaries, jobs, job_steps, source_revisions, projects, tenants CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		"INSERT INTO tenants(id,name) VALUES ($1,$2) ON CONFLICT DO NOTHING", gwTenant, "gw"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	cipher, err := tenant.NewAESGCMCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	return NewPGStore(pool, cipher)
}

func TestGatewayCRUDAndResolve(t *testing.T) {
	s := gwStore(t)
	ctx := context.Background()

	// 平台默认行。
	platform := &Gateway{
		TenantID: PlatformTenantID, Name: "platform", Kind: KindTTS,
		Provider: "openai_compatible", BaseURL: "https://api.siliconflow.cn",
		Model: "FunAudioLLM/CosyVoice2-0.5B", Voice: "default", Enabled: true,
	}
	if err := s.Create(ctx, platform, "sk-platform-key"); err != nil {
		t.Fatalf("create platform: %v", err)
	}
	if err := s.SetDefault(ctx, PlatformTenantID, "platform", KindTTS); err != nil {
		t.Fatalf("set default platform: %v", err)
	}

	// 无租户配置 → 回退平台默认。
	cfg, err := s.Resolve(ctx, gwTenant, KindTTS)
	if err != nil {
		t.Fatalf("resolve fallback: %v", err)
	}
	if cfg.Model != "FunAudioLLM/CosyVoice2-0.5B" || cfg.APIKey != "sk-platform-key" {
		t.Fatalf("fallback cfg = %+v", cfg)
	}

	// 租户行覆盖平台。
	tenantGW := &Gateway{
		TenantID: gwTenant, Name: "tenant-tts", Kind: KindTTS,
		Provider: "openai_compatible", BaseURL: "https://custom.example",
		Model: "tenant-model", Enabled: true,
	}
	if err := s.Create(ctx, tenantGW, "sk-tenant-key"); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if err := s.SetDefault(ctx, gwTenant, "tenant-tts", KindTTS); err != nil {
		t.Fatalf("set default tenant: %v", err)
	}
	cfg, err = s.Resolve(ctx, gwTenant, KindTTS)
	if err != nil {
		t.Fatalf("resolve tenant: %v", err)
	}
	if cfg.Model != "tenant-model" || cfg.APIKey != "sk-tenant-key" {
		t.Fatalf("tenant cfg = %+v", cfg)
	}

	// List 只返回本租户行，且 key 掩码。
	gws, err := s.List(ctx, gwTenant, KindTTS)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(gws) != 1 || !gws[0].HasKey || gws[0].KeyMasked == "sk-tenant-key" {
		t.Fatalf("list = %+v", gws)
	}

	// Update 保留 key（apiKey 空）且 version 递增。
	cur, err := s.Get(ctx, gwTenant, "tenant-tts", KindTTS)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	cur.Model = "tenant-model-v2"
	if err := s.Update(ctx, cur, ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	cfg, err = s.Resolve(ctx, gwTenant, KindTTS)
	if err != nil {
		t.Fatalf("resolve after update: %v", err)
	}
	if cfg.Model != "tenant-model-v2" || cfg.APIKey != "sk-tenant-key" {
		t.Fatalf("key not preserved: %+v", cfg)
	}

	// Update 换 key。
	cur2, _ := s.Get(ctx, gwTenant, "tenant-tts", KindTTS)
	cur2.Model = "tenant-model-v3"
	if err := s.Update(ctx, cur2, "sk-new-key"); err != nil {
		t.Fatalf("update key: %v", err)
	}
	cfg, _ = s.Resolve(ctx, gwTenant, KindTTS)
	if cfg.APIKey != "sk-new-key" {
		t.Fatalf("key not rotated: %+v", cfg)
	}

	// ResolveNamed 供探活。
	ncfg, err := s.ResolveNamed(ctx, gwTenant, "tenant-tts", KindTTS)
	if err != nil || ncfg.APIKey != "sk-new-key" {
		t.Fatalf("resolve named = %+v err=%v", ncfg, err)
	}

	// 缓存失效：删行后 Invalidate 清缓存，Resolve 回退平台默认。
	if err := s.Delete(ctx, gwTenant, "tenant-tts", KindTTS); err != nil {
		t.Fatalf("delete: %v", err)
	}
	cfg, err = s.Resolve(ctx, gwTenant, KindTTS)
	if err != nil {
		t.Fatalf("resolve after tenant delete: %v", err)
	}
	if cfg.Model != "FunAudioLLM/CosyVoice2-0.5B" {
		t.Fatalf("fallback after delete = %+v", cfg)
	}

	// 平台行删除后彻底 NotFound。
	if err := s.Delete(ctx, PlatformTenantID, "platform", KindTTS); err != nil {
		t.Fatalf("delete platform: %v", err)
	}
	if _, err := s.Resolve(ctx, gwTenant, KindTTS); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGatewayDefaultUniqueness(t *testing.T) {
	s := gwStore(t)
	ctx := context.Background()
	// 先建两个非默认。
	for _, name := range []string{"a", "b"} {
		gw := &Gateway{
			TenantID: PlatformTenantID, Name: name, Kind: KindLLM,
			Provider: "openai_compatible", BaseURL: "https://x", Model: "m", Enabled: true,
		}
		if err := s.Create(ctx, gw, "k"); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	if err := s.SetDefault(ctx, PlatformTenantID, "a", KindLLM); err != nil {
		t.Fatalf("set a: %v", err)
	}
	// b 设为默认：a 自动取消。
	if err := s.SetDefault(ctx, PlatformTenantID, "b", KindLLM); err != nil {
		t.Fatalf("set b: %v", err)
	}
	a, _ := s.Get(ctx, PlatformTenantID, "a", KindLLM)
	b, _ := s.Get(ctx, PlatformTenantID, "b", KindLLM)
	if a.IsDefault || !b.IsDefault {
		t.Fatalf("default flags: a=%v b=%v", a.IsDefault, b.IsDefault)
	}
	cfg, err := s.Resolve(ctx, PlatformTenantID, KindLLM)
	if err != nil || cfg.Name != "b" {
		t.Fatalf("resolve = %+v err=%v", cfg, err)
	}
}

func TestGatewaySeedFromEnv(t *testing.T) {
	s := gwStore(t)
	ctx := context.Background()
	env := Env{
		TTSProvider: "siliconflow", TTSAPIKey: "sk-env-tts",
		LLMProvider: "siliconflow", LLMAPIKey: "sk-env-llm",
	}
	if err := SeedFromEnv(ctx, s, env); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 幂等：重复 seed 不新增。
	if err := SeedFromEnv(ctx, s, env); err != nil {
		t.Fatalf("seed idempotent: %v", err)
	}
	ttsList, _ := s.List(ctx, PlatformTenantID, KindTTS)
	llmList, _ := s.List(ctx, PlatformTenantID, KindLLM)
	if len(ttsList) != 1 || len(llmList) != 1 {
		t.Fatalf("seed counts tts=%d llm=%d", len(ttsList), len(llmList))
	}
	if ttsList[0].Name != "env" || !ttsList[0].IsDefault {
		t.Fatalf("seeded tts = %+v", ttsList[0])
	}
	// 已有 DB 配置时 env 不再覆盖。
	existing := &Gateway{
		TenantID: PlatformTenantID, Name: "manual", Kind: KindTTS,
		Provider: "openai_compatible", BaseURL: "https://m", Model: "mm", Enabled: true,
	}
	if err := s.Create(ctx, existing, "sk-manual"); err != nil {
		t.Fatalf("create manual: %v", err)
	}
	env2 := Env{TTSProvider: "siliconflow", TTSAPIKey: "sk-other"}
	if err := SeedFromEnv(ctx, s, env2); err != nil {
		t.Fatalf("seed with existing: %v", err)
	}
	ttsList, _ = s.List(ctx, PlatformTenantID, KindTTS)
	if len(ttsList) != 2 {
		t.Fatalf("seed should not duplicate: %d", len(ttsList))
	}
}
