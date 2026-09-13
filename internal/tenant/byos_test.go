//go:build pg

package tenant

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const byosTenant = "00000000-0000-0000-0000-0000000000b8"

func byosStore(t *testing.T) *PGStore {
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
	ctx := context.Background()
	resetTenant(ctx, t, pool, byosTenant)
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name,status) VALUES ($1,'byos','active')`, byosTenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return NewPGStore(pool)
}

func TestBYOSCredentialEncryptedRoundTrip(t *testing.T) {
	s := byosStore(t)
	ctx := context.Background()
	cipher, err := NewAESGCMCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	cfg := BYOSConfig{
		Endpoint: "s3.example.com", Bucket: "customer-bucket", Region: "cn-north-1",
		AccessKey: "ak", SecretKey: "secret", UseSSL: true,
	}
	if err := s.SetBYOSCredential(ctx, byosTenant, "main", "s3", cfg, "kms-1", cipher); err != nil {
		t.Fatalf("SetBYOSCredential: %v", err)
	}

	// 明文密钥不能直接出现在数据库密文里。
	var encrypted []byte
	if err := Run(ctx, s.pool, byosTenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT encrypted_config FROM byos_credentials WHERE tenant_id=$1 AND credential_id='main'`, byosTenant).Scan(&encrypted)
	}); err != nil {
		t.Fatalf("query encrypted: %v", err)
	}
	if string(encrypted) == "secret" {
		t.Fatalf("credential stored in plaintext")
	}

	got, err := s.GetBYOSCredential(ctx, byosTenant, "main", cipher)
	if err != nil {
		t.Fatalf("GetBYOSCredential: %v", err)
	}
	if got.Backend != "s3" || got.KMSKeyID != "kms-1" || got.Config.SecretKey != "secret" || !got.Config.UseSSL {
		t.Fatalf("credential = %+v", got)
	}
	if err := s.RemoveBYOSCredential(ctx, byosTenant, "main"); err != nil {
		t.Fatalf("RemoveBYOSCredential: %v", err)
	}
	if _, err := s.GetBYOSCredential(ctx, byosTenant, "main", cipher); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("missing credential = %v want ErrCredentialNotFound", err)
	}
}

func TestAESGCMCipherRejectsWrongAAD(t *testing.T) {
	cipher, err := NewAESGCMCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	sealed, err := cipher.Encrypt([]byte("payload"), []byte("aad-1"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := cipher.Decrypt(sealed, []byte("aad-2")); err == nil {
		t.Fatalf("Decrypt with wrong AAD unexpectedly succeeded")
	}
}
