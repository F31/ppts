package s3

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

// RegionResolver resolves the storage region configured for a tenant.
type RegionResolver interface {
	StorageRegion(ctx context.Context, tenantID string) (string, error)
}

// RegionRouter 按租户存储区域把对象路由到 `{base-bucket}-{region}` 桶（G3-6 按租户区域/桶路由）。
// 区域为空时回退到基础桶；区域桶懒创建并缓存。预签名/生命周期等仍基于同一 S3 兼容端点。
type RegionRouter struct {
	base     *Store
	resolver RegionResolver
	mu       sync.Mutex
	clients  map[string]*Store
}

// NewRegionRouter 创建区域路由包装器。
func NewRegionRouter(base *Store, resolver RegionResolver) *RegionRouter {
	return &RegionRouter{base: base, resolver: resolver, clients: map[string]*Store{}}
}

func (r *RegionRouter) storeFor(ctx context.Context, tenantID string) (*Store, error) {
	region, err := r.resolver.StorageRegion(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(region) == "" {
		return r.base, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if st, ok := r.clients[region]; ok {
		return st, nil
	}
	bucket := regionBucket(r.base.BucketName(), region)
	st := &Store{client: r.base.client, bucket: bucket}
	if err := ensureBucketExists(ctx, st.client, bucket); err != nil {
		return nil, err
	}
	r.clients[region] = st
	return st, nil
}

// regionBucket 把区域规范化为合法桶名后缀：小写、下划线转连字符、丢弃其余非法字符。
func regionBucket(base, region string) string {
	var b strings.Builder
	b.WriteString(base)
	b.WriteByte('-')
	for _, r := range strings.ToLower(region) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-':
			b.WriteRune(r)
		case r == '_' || r == '.':
			b.WriteRune('-')
		}
	}
	if b.Len() == len(base)+1 {
		return base
	}
	return b.String()
}

func ensureBucketExists(ctx context.Context, client *minio.Client, bucket string) error {
	ok, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{})
}

func (r *RegionRouter) Put(ctx context.Context, key objectstore.ObjectKey, reader io.Reader, meta objectstore.ObjectMeta) error {
	st, err := r.storeFor(ctx, key.TenantID)
	if err != nil {
		return err
	}
	return st.Put(ctx, key, reader, meta)
}

func (r *RegionRouter) Get(ctx context.Context, key objectstore.ObjectKey) (io.ReadCloser, objectstore.ObjectMeta, error) {
	st, err := r.storeFor(ctx, key.TenantID)
	if err != nil {
		return nil, objectstore.ObjectMeta{}, err
	}
	return st.Get(ctx, key)
}

func (r *RegionRouter) SignedURL(ctx context.Context, key objectstore.ObjectKey, op objectstore.Operation, ttl time.Duration) (string, error) {
	st, err := r.storeFor(ctx, key.TenantID)
	if err != nil {
		return "", err
	}
	return st.SignedURL(ctx, key, op, ttl)
}

func (r *RegionRouter) Delete(ctx context.Context, key objectstore.ObjectKey) error {
	st, err := r.storeFor(ctx, key.TenantID)
	if err != nil {
		return err
	}
	return st.Delete(ctx, key)
}

func (r *RegionRouter) ApplyLifecyclePolicy(ctx context.Context, bucket string, policy objectstore.LifecyclePolicy) error {
	return r.base.ApplyLifecyclePolicy(ctx, bucket, policy)
}

// ApplyTenantLifecyclePolicy 把生命周期规则下发到租户区域对应的桶。
func (r *RegionRouter) ApplyTenantLifecyclePolicy(ctx context.Context, tenantID, bucket string, policy objectstore.LifecyclePolicy) error {
	st, err := r.storeFor(ctx, tenantID)
	if err != nil {
		return err
	}
	return st.ApplyLifecyclePolicy(ctx, bucket, policy)
}
