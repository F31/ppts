package storefactory

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/F31/ppts/internal/tenant"
)

type fakeBYOSCreds struct{ calls int }

func (f *fakeBYOSCreds) GetBYOSCredential(context.Context, string, string, tenant.CredentialCipher) (*tenant.BYOSCredential, error) {
	f.calls++
	return &tenant.BYOSCredential{
		TenantID: "tenant-1", CredentialID: "main", Backend: "s3",
		Config: tenant.BYOSConfig{Endpoint: "localhost:9000", Bucket: "bucket", AccessKey: "ak", SecretKey: "sk"},
	}, nil
}

func TestBYOSResolverBuildsAndCachesStore(t *testing.T) {
	cipher, err := tenant.NewAESGCMCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	creds := &fakeBYOSCreds{}
	r := NewBYOSResolver(creds, cipher)
	first, err := r.ObjectStoreForBackend(context.Background(), "tenant-1", "byos:main")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := r.ObjectStoreForBackend(context.Background(), "tenant-1", "byos:main")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first == nil || second == nil || creds.calls != 1 {
		t.Fatalf("store/cache invalid first=%v second=%v calls=%d", first, second, creds.calls)
	}
}

func TestFromEnvBYOSRequiresCredentialReader(t *testing.T) {
	t.Setenv("PPTS_OBJECT_ROOT", t.TempDir())
	t.Setenv("PPTS_S3_ENDPOINT", "")
	t.Setenv("PPTS_BYOS_AES_KEY_BASE64", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))

	if _, err := FromEnv(nil); err == nil {
		t.Fatalf("FromEnv should reject BYOS key without credential reader")
	}
}
