package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/lifecycle"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	cfg := Config{
		Endpoint:  os.Getenv("S3_ENDPOINT"),
		Bucket:    os.Getenv("S3_BUCKET"),
		AccessKey: os.Getenv("S3_ACCESS_KEY"),
		SecretKey: os.Getenv("S3_SECRET_KEY"),
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "localhost:9000"
	}
	if cfg.Bucket == "" {
		cfg.Bucket = "ppts-test-bucket"
	}
	if cfg.AccessKey == "" {
		cfg.AccessKey = "pptsminio"
	}
	if cfg.SecretKey == "" {
		cfg.SecretKey = "pptsminio123"
	}
	return cfg
}

func setupS3(t *testing.T) *Store {
	t.Helper()
	if os.Getenv("S3_ENDPOINT") == "" {
		t.Skip("S3 adapter test requires S3_ENDPOINT (e.g. localhost:9000)")
	}
	cfg := testConfig(t)
	st, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	cli, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""), Secure: cfg.UseSSL,
	})
	if err != nil {
		t.Fatalf("minio client: %v", err)
	}
	if ok, err := cli.BucketExists(ctx, cfg.Bucket); err == nil && !ok {
		if err := cli.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{}); err != nil {
			t.Fatalf("MakeBucket: %v", err)
		}
	}
	return st
}

func s3Key() objectstore.ObjectKey {
	return objectstore.ObjectKey{TenantID: "t-1", ProjectID: "p-1", Revision: "src-01", AssetType: "audio", AssetID: "seg-1", Ext: "mp3"}
}

func TestS3PutGetDelete(t *testing.T) {
	st := setupS3(t)
	ctx := context.Background()
	k := s3Key()
	if err := st.Put(ctx, k, strings.NewReader("hello"), objectstore.ObjectMeta{
		ContentType: "audio/mpeg", ContentHash: "abc123", Size: 5,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, meta, err := st.Get(ctx, k)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "hello" || meta.ContentHash != "abc123" || meta.Size != 5 {
		t.Fatalf("Get mismatch: data=%q meta=%+v", data, meta)
	}
	if err := st.Delete(ctx, k); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := st.Get(ctx, k); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Fatalf("Get after delete: got %v, want ErrObjectNotFound", err)
	}
	// Delete 幂等。
	if err := st.Delete(ctx, k); err != nil {
		t.Fatalf("Delete idempotent: %v", err)
	}
}

func TestS3SignedURLRead(t *testing.T) {
	st := setupS3(t)
	ctx := context.Background()
	k := s3Key()
	body := "signed-body"
	if err := st.Put(ctx, k, strings.NewReader(body), objectstore.ObjectMeta{ContentType: "audio/mpeg", Size: int64(len(body))}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	raw, err := st.SignedURL(ctx, k, objectstore.OpRead, time.Minute)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	resp, err := http.Get(raw)
	if err != nil {
		t.Fatalf("GET signed url: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	if string(data) != body {
		t.Fatalf("signed read mismatch: %q", data)
	}
}

func TestS3SignedURLWriteAndDeleteUnsupported(t *testing.T) {
	st := setupS3(t)
	ctx := context.Background()
	k := s3Key()
	raw, err := st.SignedURL(ctx, k, objectstore.OpWrite, time.Minute)
	if err != nil {
		t.Fatalf("SignedURL write: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPut, raw, bytes.NewBufferString("via-presign"))
	req.Header.Set("Content-Type", "audio/mpeg")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT signed url: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presign put status: %d", resp.StatusCode)
	}
	rc, _, err := st.Get(ctx, k)
	if err != nil {
		t.Fatalf("Get after presign put: %v", err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "via-presign" {
		t.Fatalf("presign put content: %q", data)
	}
	if _, err := st.SignedURL(ctx, k, objectstore.OpDelete, time.Minute); !errors.Is(err, objectstore.ErrOperationNotSupported) {
		t.Fatalf("delete presign should be unsupported, got %v", err)
	}
}

func TestS3LifecyclePolicy(t *testing.T) {
	st := setupS3(t)
	ctx := context.Background()
	// 过期删除是 S3 兼容后端通用能力；存储类别转型依赖后端远程 tier 配置
	// （MinIO 默认无 tier，拒绝 transform），转型翻译留待真实 S3 复验。
	policy := objectstore.LifecyclePolicy{
		Expiration: &objectstore.LifecycleExpiration{AfterDays: 365},
	}
	if err := st.ApplyLifecyclePolicy(ctx, st.BucketName(), policy); err != nil {
		t.Fatalf("ApplyLifecyclePolicy: %v", err)
	}
	cli, _ := minio.New(testConfig(t).Endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(testConfig(t).AccessKey, testConfig(t).SecretKey, ""), Secure: false,
	})
	cfg, err := cli.GetBucketLifecycle(ctx, st.BucketName())
	if err != nil {
		t.Fatalf("GetBucketLifecycle: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("lifecycle rules: got %d, want 1", len(cfg.Rules))
	}
	// 清理规则，避免影响后续用例；请求级幂等。
	if err := cli.SetBucketLifecycle(ctx, st.BucketName(), &lifecycle.Configuration{}); err != nil {
		t.Fatalf("clear lifecycle: %v", err)
	}
}
