package gateway

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/tenant"
	"github.com/F31/ppts/migrations"
)

func newSQLiteGatewayStore(t *testing.T) *SQLiteStore {
	t.Helper()
	ctx := context.Background()
	sqldb, err := db.OpenSQLite(ctx, filepath.Join(t.TempDir(), "ppts.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	fsys, _ := migrations.SQLite()
	if _, err := db.MigrateSQLite(ctx, sqldb, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.EnsureLocalIdentity(ctx, sqldb); err != nil {
		t.Fatalf("identity: %v", err)
	}
	key := sha256.Sum256([]byte("phase0.5-sqlite-roundtrip"))
	cipher, err := tenant.NewAESGCMCipher(key[:])
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	return NewSQLiteStore(sqldb, cipher)
}

func newTestGateway(tenant string, enabled bool) *Gateway {
	return &Gateway{
		TenantID:    tenant,
		Name:        "gw-main",
		Kind:        KindTTS,
		Provider:    "openai_compatible",
		BaseURL:     "https://api.example.com/v1",
		Model:       "tts-model",
		VisionModel: "vision-model",
		Voice:       "zh-CN-female",
		SampleRate:  24000,
		IsDefault:   true,
		Enabled:     enabled,
		Version:     1,
	}
}

// TestSQLiteGatewayRoundTrip 守护 sqScanRow 的 16 个 Scan 目标与 sqColumnsSQL 顺序一致。
//
// 这个列表比其它包更容易漂移，因为它**不是纯列名**：末尾两列是
// `CAST(strftime('%s', created_at) AS INTEGER)` 这样的表达式，中间还有两个 INTEGER 布尔列。
// 一旦有人往中间插一列，is_default/enabled 就会吃掉别的列的值——
// "默认网关"与"是否启用"读反，在 UI 上表现为"选了却没生效"，很难联想到 Scan 顺序。
func TestSQLiteGatewayRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteGatewayStore(t)
	const tenantID = db.LocalTenantID
	const apiKey = "sk-roundtrip-secret"

	in := newTestGateway(tenantID, true)
	if err := store.Create(ctx, in, apiKey); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := store.Get(ctx, tenantID, "gw-main", KindTTS)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	assertGatewayFields(t, "Get", got, in)
	if got.HasKey != true {
		t.Fatalf("Get: HasKey = false（凭据列或 apiKey 未往返）")
	}
	if got.KeyMasked == apiKey {
		t.Fatalf("Get: KeyMasked 应是掩码而非明文：%q", got.KeyMasked)
	}

	list, err := store.List(ctx, tenantID, KindTTS)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d want 1", len(list))
	}
	assertGatewayFields(t, "List", list[0], in)
}

func assertGatewayFields(t *testing.T, path string, got, want *Gateway) {
	t.Helper()
	if got.TenantID != want.TenantID {
		t.Fatalf("%s: TenantID = %q want %q", path, got.TenantID, want.TenantID)
	}
	if got.Name != want.Name {
		t.Fatalf("%s: Name = %q want %q", path, got.Name, want.Name)
	}
	if got.Kind != want.Kind {
		t.Fatalf("%s: Kind = %q want %q", path, got.Kind, want.Kind)
	}
	if got.Provider != want.Provider {
		t.Fatalf("%s: Provider = %q want %q", path, got.Provider, want.Provider)
	}
	if got.BaseURL != want.BaseURL {
		t.Fatalf("%s: BaseURL = %q want %q", path, got.BaseURL, want.BaseURL)
	}
	if got.Model != want.Model {
		t.Fatalf("%s: Model = %q want %q（model/vision_model 相邻，最易串）", path, got.Model, want.Model)
	}
	if got.VisionModel != want.VisionModel {
		t.Fatalf("%s: VisionModel = %q want %q", path, got.VisionModel, want.VisionModel)
	}
	if got.Voice != want.Voice {
		t.Fatalf("%s: Voice = %q want %q", path, got.Voice, want.Voice)
	}
	if got.SampleRate != want.SampleRate {
		t.Fatalf("%s: SampleRate = %d want %d", path, got.SampleRate, want.SampleRate)
	}
	if got.IsDefault != want.IsDefault {
		t.Fatalf("%s: IsDefault = %v want %v（布尔列错位）", path, got.IsDefault, want.IsDefault)
	}
	if got.Enabled != want.Enabled {
		t.Fatalf("%s: Enabled = %v want %v（布尔列错位）", path, got.Enabled, want.Enabled)
	}
	if got.Version != want.Version {
		t.Fatalf("%s: Version = %d want %d", path, got.Version, want.Version)
	}
	// 时间列经 CAST(strftime('%s', …)) 转成 unix 秒，读不出来会退化成 0。
	if got.CreatedAt == 0 || got.UpdatedAt == 0 {
		t.Fatalf("%s: 时间戳未解析 CreatedAt=%d UpdatedAt=%d", path, got.CreatedAt, got.UpdatedAt)
	}
}

// TestSQLiteGatewayResolveReturnsAPIKey 覆盖解析路径：管理面（Get）必须只给掩码，
// 而运行时解析（Resolve/ResolveNamed）必须给出**可用**的明文 key——
// 反了会导致"配置看着全对，实际调用必然失败"，且失败发生在供应商侧而非本地。
func TestSQLiteGatewayResolveReturnsAPIKey(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteGatewayStore(t)
	const tenantID = db.LocalTenantID
	const apiKey = "sk-resolve-secret"

	if err := store.Create(ctx, newTestGateway(tenantID, true), apiKey); err != nil {
		t.Fatalf("create: %v", err)
	}

	cfg, err := store.ResolveNamed(ctx, tenantID, "gw-main", KindTTS)
	if err != nil {
		t.Fatalf("resolve named: %v", err)
	}
	if cfg.APIKey != apiKey {
		t.Fatalf("ResolveNamed APIKey = %q want 明文 key", cfg.APIKey)
	}
	if cfg.BaseURL != "https://api.example.com/v1" || cfg.SampleRate != 24000 {
		t.Fatalf("Config 字段错位：%+v", cfg)
	}

	resolved, err := store.Resolve(ctx, tenantID, KindTTS)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.APIKey != apiKey {
		t.Fatalf("Resolve APIKey = %q want 明文 key", resolved.APIKey)
	}

	// Update 走乐观并发：落库版本 = 传入版本 + 1，并把新版本回写进传入对象
	// （调用方据此续做下一次更新，也是冲突检测的依据）。
	updated := newTestGateway(tenantID, true)
	updated.Version = 2
	prevVersion := updated.Version
	if err := store.Update(ctx, updated, "sk-rotated-secret"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Version != prevVersion+1 {
		t.Fatalf("Update 未回写新版本：%d want %d", updated.Version, prevVersion+1)
	}
	after, err := store.Resolve(ctx, tenantID, KindTTS)
	if err != nil {
		t.Fatalf("resolve after update: %v", err)
	}
	if after.APIKey != "sk-rotated-secret" {
		t.Fatalf("轮换后仍读到旧 key：%q", after.APIKey)
	}
	if after.Version != prevVersion+1 {
		t.Fatalf("轮换后 Version = %d want %d（乐观并发未生效）", after.Version, prevVersion+1)
	}
}

// TestSQLiteGatewayDeleteAndNotFound 覆盖删除与"不存在"的判定。
func TestSQLiteGatewayDeleteAndNotFound(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteGatewayStore(t)
	const tenantID = db.LocalTenantID

	if err := store.Create(ctx, newTestGateway(tenantID, true), "sk-x"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.Get(ctx, tenantID, "gw-missing", KindTTS); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的网关 err = %v want ErrNotFound", err)
	}
	if err := store.Update(ctx, newTestGateway("tenant-foreign", true), "sk-y"); err == nil {
		t.Fatal("跨租户 Update 应返回错误")
	}
	if err := store.Delete(ctx, tenantID, "gw-main", KindTTS); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(ctx, tenantID, "gw-main", KindTTS); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后 err = %v want ErrNotFound", err)
	}
	if err := store.Delete(ctx, tenantID, "gw-main", KindTTS); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除 err = %v want ErrNotFound", err)
	}
}
