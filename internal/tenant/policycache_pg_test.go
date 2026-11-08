//go:build pg

package tenant

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func newPGStoreBench(tb testing.TB) (*PGStore, string) {
	tb.Helper()
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		tb.Skip("PPTS_TEST_DATABASE not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		tb.Fatalf("pool: %v", err)
	}
	tb.Cleanup(pool.Close)
	const id = "00000000-0000-0000-0000-0000000000e1"
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name,status,policy) VALUES
		 ($1::uuid,'bench','active','{"storage_backend":"local","storage_region":"ap-guangzhou","source_retention_days":30,"envelope_encryption":true}'::jsonb)
		 ON CONFLICT (id) DO UPDATE SET policy=EXCLUDED.policy, updated_at=now()`, id); err != nil {
		tb.Fatalf("seed: %v", err)
	}
	tb.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1::uuid`, id)
	})
	return NewPGStore(pool), id
}

// PG 侧的写后失效：跨进程只能靠 TTL，但同进程内的 SetPolicy 必须立刻生效。
func TestPGSetPolicyInvalidatesCache(t *testing.T) {
	s, id := newPGStoreBench(t)
	s = s.WithPolicyCache(time.Hour)
	ctx := context.Background()

	if err := s.SetPolicy(ctx, id, Policy{StorageBackend: "local", SourceRetentionDays: 7}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if p, err := s.GetPolicy(ctx, id); err != nil || p.SourceRetentionDays != 7 {
		t.Fatalf("first read: p=%+v err=%v", p, err)
	}
	if err := s.SetPolicy(ctx, id, Policy{StorageBackend: "s3", SourceRetentionDays: 30}); err != nil {
		t.Fatalf("set: %v", err)
	}
	p, err := s.GetPolicy(ctx, id)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if p.SourceRetentionDays != 30 || p.StorageBackend != "s3" {
		t.Fatalf("改策略后仍读到旧值: %+v（SetPolicy 未失效缓存）", p)
	}
}

func benchmarkGetPolicyPG(b *testing.B, ttl time.Duration) {
	s, id := newPGStoreBench(b)
	s = s.WithPolicyCache(ttl)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.GetPolicy(ctx, id); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetPolicyPGUncached(b *testing.B) { benchmarkGetPolicyPG(b, 0) }
func BenchmarkGetPolicyPGCached(b *testing.B)   { benchmarkGetPolicyPG(b, time.Hour) }
