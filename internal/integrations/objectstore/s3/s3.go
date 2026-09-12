// Package s3 以 minio-go 实现 S3 兼容多后端适配器（V4.0 §12.4/ADR-014）。
//
// 业务代码只依赖父包 objectstore.ObjectStore 端口；本包是适配实现之一
// （AWS S3/MinIO/阿里云 OSS/腾讯云 COS 等 S3 兼容协议）。键结构、租户前缀校验
// 与签名安全意识在父包端口即已保证，本包忠实执行 ObjectKey 映射。
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/lifecycle"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

// Config s3 适配器配置。
type Config struct {
	Endpoint  string // host:port（不含 scheme）
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
	Region    string
}

// Store 以 minio-go 实现 objectstore.ObjectStore。
type Store struct {
	client *minio.Client
	bucket string
}

// New 创建适配器。bucket 不存在时自动创建（后端已授权创建即不返回错误）。
func New(cfg Config) (*Store, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("s3: endpoint and bucket are required")
	}
	cli, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3: init client: %w", err)
	}
	return &Store{client: cli, bucket: cfg.Bucket}, nil
}

// BucketName 暴露当前桶名（生命周期策略等管理用途）。
func (s *Store) BucketName() string { return s.bucket }

func (s *Store) Put(ctx context.Context, key objectstore.ObjectKey, r io.Reader, meta objectstore.ObjectMeta) error {
	if err := key.Validate(); err != nil {
		return err
	}
	opts := minio.PutObjectOptions{ContentType: meta.ContentType}
	if meta.ContentHash != "" {
		opts.UserMetadata = map[string]string{"ppts-sha256": meta.ContentHash}
	}
	size := meta.Size
	if size <= 0 {
		size = -1 // 未知流
	}
	if _, err := s.client.PutObject(ctx, s.bucket, key.String(), r, size, opts); err != nil {
		return fmt.Errorf("s3: put %s: %w", key.String(), err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, key objectstore.ObjectKey) (io.ReadCloser, objectstore.ObjectMeta, error) {
	if err := key.Validate(); err != nil {
		return nil, objectstore.ObjectMeta{}, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key.String(), minio.GetObjectOptions{})
	if err != nil {
		return nil, objectstore.ObjectMeta{}, err
	}
	st, err := obj.Stat()
	if err != nil {
		if isNotExist(err) {
			return nil, objectstore.ObjectMeta{}, objectstore.ErrObjectNotFound
		}
		return nil, objectstore.ObjectMeta{}, err
	}
	meta := objectstore.ObjectMeta{Size: st.Size, ContentType: st.ContentType, ETag: st.ETag}
	if v, ok := st.Metadata["X-Amz-Meta-Ppts-Sha256"]; ok && len(v) > 0 {
		meta.ContentHash = v[0]
	}
	return obj, meta, nil
}

// SignedURL 生成预签名下载/上传链接；删除操作不支持（显式返回不支持，不伪成功）。
func (s *Store) SignedURL(ctx context.Context, key objectstore.ObjectKey, op objectstore.Operation, ttl time.Duration) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	var u *url.URL
	var err error
	switch op {
	case objectstore.OpRead:
		u, err = s.client.PresignedGetObject(ctx, s.bucket, key.String(), ttl, nil)
	case objectstore.OpWrite:
		u, err = s.client.PresignedPutObject(ctx, s.bucket, key.String(), ttl)
	default:
		return "", fmt.Errorf("%w: presign delete is not supported by backend", objectstore.ErrOperationNotSupported)
	}
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (s *Store) Delete(ctx context.Context, key objectstore.ObjectKey) error {
	if err := key.Validate(); err != nil {
		return err
	}
	// RemoveObject 对不存在对象幂等，符合 Delete 语义。
	return s.client.RemoveObject(ctx, s.bucket, key.String(), minio.RemoveObjectOptions{})
}

// ApplyLifecyclePolicy 翻译生命周期分层规则到后端（V4.0 §12.5）。
func (s *Store) ApplyLifecyclePolicy(ctx context.Context, bucket string, policy objectstore.LifecyclePolicy) error {
	cfg := lifecycle.Configuration{}
	for i, tr := range policy.Transitions {
		cfg.Rules = append(cfg.Rules, lifecycle.Rule{
			ID:     "ppts-tier-" + strconv.Itoa(i),
			Status: "Enabled",
			Transition: lifecycle.Transition{
				Days:         lifecycle.ExpirationDays(tr.AfterDays),
				StorageClass: string(tr.To),
			},
		})
	}
	if policy.Expiration != nil {
		cfg.Rules = append(cfg.Rules, lifecycle.Rule{
			ID: "ppts-expire", Status: "Enabled",
			Expiration: lifecycle.Expiration{Days: lifecycle.ExpirationDays(policy.Expiration.AfterDays)},
		})
	}
	if bucket == "" {
		bucket = s.bucket
	}
	return s.client.SetBucketLifecycle(ctx, bucket, &cfg)
}

func isNotExist(err error) bool {
	var errResp minio.ErrorResponse
	if errors.As(err, &errResp) {
		if errResp.StatusCode == http.StatusNotFound {
			return true
		}
	}
	return minio.ToErrorResponse(err).StatusCode == http.StatusNotFound
}
