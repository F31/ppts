package gateway

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"testing"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/tenant"
	"github.com/F31/ppts/migrations"
)

// 跨实例搬迁 / 密钥轮换的真实故障：DB 行还在、密文也在，但用当前
// PPTS_GATEWAY_AES_KEY_BASE64 解不开。修复前 List/Get 把 decodeCreds 的错误丢掉，
// 管理面显示「未配置 key」，用户会反复重填凭据，直到运行时 Resolve 才炸。
// 这里锁住「不可读」与「真的没配」必须是两个不同状态。
func newGatewayStoreOnFile(t *testing.T, path, keySeed string) *SQLiteStore {
	t.Helper()
	ctx := context.Background()
	sqldb, err := db.OpenSQLite(ctx, path)
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
	key := sha256.Sum256([]byte(keySeed))
	cipher, err := tenant.NewAESGCMCipher(key[:])
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	return NewSQLiteStore(sqldb, cipher)
}

func TestGatewayListGetMarkUnreadableCreds(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ppts.db")
	writer := newGatewayStoreOnFile(t, path, "key-written-with")
	const tenantID = db.LocalTenantID

	if err := writer.Create(ctx, newTestGateway(tenantID, true), "sk-secret"); err != nil {
		t.Fatalf("create: %v", err)
	}

	// 同一个 DB、另一把密钥：等价于把网关行搬到另一个实例却没重新加密。
	reader := newGatewayStoreOnFile(t, path, "key-read-with")

	got, err := reader.Get(ctx, tenantID, "gw-main", KindTTS)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.KeyUnreadable || got.HasKey || got.KeyMasked != "" {
		t.Fatalf("Get 应标记密钥不可读，得到 %+v", got)
	}

	rows, err := reader.List(ctx, tenantID, KindTTS)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || !rows[0].KeyUnreadable || rows[0].HasKey {
		t.Fatalf("List 应标记密钥不可读，得到 %+v", rows)
	}
}

func TestGatewayEmptyKeyIsNotUnreadable(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteGatewayStore(t)
	const tenantID = db.LocalTenantID

	// 允许建一条没有 API Key 的行（例如占位配置）。这不是解密失败。
	if err := store.Create(ctx, newTestGateway(tenantID, true), ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := store.Get(ctx, tenantID, "gw-main", KindTTS)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.KeyUnreadable || got.HasKey {
		t.Fatalf("空密钥应只是「未配置」，得到 %+v", got)
	}
}
