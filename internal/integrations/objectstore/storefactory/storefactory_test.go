package storefactory

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

func TestNewRequiresRegisteredDefault(t *testing.T) {
	_, err := New(Config{Default: "s3", LocalRoot: t.TempDir()}, nil)
	if !errors.Is(err, objectstore.ErrBackendNotFound) {
		t.Fatalf("New missing default = %v, want ErrBackendNotFound", err)
	}
}

func TestFromEnvLocalRoundTrip(t *testing.T) {
	t.Setenv("PPTS_OBJECT_BACKEND", "")
	t.Setenv("PPTS_OBJECT_ROOT", t.TempDir())
	t.Setenv("PPTS_OBJECT_SECRET", "test-secret")
	t.Setenv("PPTS_S3_ENDPOINT", "")

	registry, err := FromEnv(nil)
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	key := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "src", AssetType: "source", AssetID: "a", Ext: "pptx"}
	if err := registry.Put(context.Background(), key, bytes.NewReader([]byte("data")), objectstore.ObjectMeta{Size: 4}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, _, err := registry.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(rc); err != nil {
		t.Fatalf("read: %v", err)
	}
	if buf.String() != "data" {
		t.Fatalf("got %q want data", buf.String())
	}
}

func TestFromEnvRegistersS3(t *testing.T) {
	t.Setenv("PPTS_OBJECT_BACKEND", "s3")
	t.Setenv("PPTS_OBJECT_ROOT", "")
	t.Setenv("PPTS_OBJECT_SECRET", "")
	t.Setenv("PPTS_S3_ENDPOINT", "localhost:9000")
	t.Setenv("PPTS_S3_BUCKET", "ppts-test")
	t.Setenv("PPTS_S3_ACCESS_KEY", "key")
	t.Setenv("PPTS_S3_SECRET_KEY", "secret")
	t.Setenv("PPTS_S3_USE_SSL", "false")

	if _, err := FromEnv(nil); err != nil {
		t.Fatalf("FromEnv s3: %v", err)
	}
}

func TestFromEnvRejectsBadUseSSL(t *testing.T) {
	t.Setenv("PPTS_OBJECT_BACKEND", "s3")
	t.Setenv("PPTS_S3_ENDPOINT", "localhost:9000")
	t.Setenv("PPTS_S3_USE_SSL", "not-a-bool")

	if _, err := FromEnv(nil); err == nil {
		t.Fatalf("FromEnv should reject invalid PPTS_S3_USE_SSL")
	}
}

func TestWithEnvelopeEncryptionFromEnvRejectsBadKey(t *testing.T) {
	t.Setenv("PPTS_OBJECT_ENCRYPTION_KEY_BASE64", "not-base64")
	if _, err := WithEnvelopeEncryptionFromEnv(objectstore.NewLocal(t.TempDir(), nil), nil); err == nil {
		t.Fatalf("WithEnvelopeEncryptionFromEnv should reject bad key")
	}
}
