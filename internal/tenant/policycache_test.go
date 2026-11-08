package tenant

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/migrations"
)

// countingFetch 返回一个记录调用次数的 fetch 函数。
func countingFetch(calls *int32, p *Policy, err error) func(context.Context, string) (*Policy, error) {
	return func(context.Context, string) (*Policy, error) {
		atomic.AddInt32(calls, 1)
		return p, err
	}
}

func TestPolicyCacheServesRepeatedReads(t *testing.T) {
	ctx := context.Background()
	var calls int32
	want := &Policy{StorageBackend: "s3"}
	cache := newPolicyCache(time.Minute)
	for i := 0; i < 50; i++ {
		got, err := cache.load(ctx, "t1", countingFetch(&calls, want, nil))
		if err != nil {
			t.Fatalf("load #%d: %v", i, err)
		}
		if got != want {
			t.Fatalf("load #%d: got %+v", i, got)
		}
	}
	if calls != 1 {
		t.Fatalf("fetch calls = %d, want 1（TTL 内应只查一次）", calls)
	}
	if hits, misses := cache.stats(); hits != 49 || misses != 1 {
		t.Fatalf("stats = (%d, %d), want (49, 1)", hits, misses)
	}
}

// 缓存与租户一一对应：不同租户互不串味。
func TestPolicyCacheIsolatesTenants(t *testing.T) {
	ctx := context.Background()
	cache := newPolicyCache(time.Minute)
	var calls int32
	fetch := func(_ context.Context, id string) (*Policy, error) {
		atomic.AddInt32(&calls, 1)
		return &Policy{StorageBackend: id}, nil
	}
	for _, id := range []string{"a", "b", "a", "b"} {
		p, err := cache.load(ctx, id, fetch)
		if err != nil {
			t.Fatalf("load %s: %v", id, err)
		}
		if p.StorageBackend != id {
			t.Fatalf("tenant %s got policy of %q", id, p.StorageBackend)
		}
	}
	if calls != 2 {
		t.Fatalf("fetch calls = %d, want 2", calls)
	}
}

// 并发未命中只放一个请求进 fetch：否则冷缓存瞬间 N 个请求会打出 N 次相同查询。
func TestPolicyCacheConcurrentSingleFetch(t *testing.T) {
	ctx := context.Background()
	cache := newPolicyCache(time.Minute)
	var calls int32
	var wg sync.WaitGroup
	want := &Policy{StorageBackend: "local"}
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := cache.load(ctx, "t1", countingFetch(&calls, want, nil))
			if err != nil || p != want {
				t.Errorf("load: p=%+v err=%v", p, err)
			}
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", calls)
	}
}

func TestPolicyCacheExpiry(t *testing.T) {
	ctx := context.Background()
	cache := newPolicyCache(time.Minute)
	now := time.Unix(1000, 0)
	cache.now = func() time.Time { return now }
	var calls int32
	fetch := countingFetch(&calls, &Policy{StorageBackend: "v1"}, nil)

	if _, err := cache.load(ctx, "t1", fetch); err != nil {
		t.Fatalf("load: %v", err)
	}
	now = now.Add(59 * time.Second)
	if _, err := cache.load(ctx, "t1", fetch); err != nil {
		t.Fatalf("load: %v", err)
	}
	if calls != 1 {
		t.Fatalf("TTL 内 fetch calls = %d, want 1", calls)
	}
	now = now.Add(2 * time.Second) // 越过 60s
	if _, err := cache.load(ctx, "t1", fetch); err != nil {
		t.Fatalf("load: %v", err)
	}
	if calls != 2 {
		t.Fatalf("过期后 fetch calls = %d, want 2", calls)
	}
}

// 错误不缓存：一次「租户不存在」或数据库抖动不该被放大成 TTL 长度的持续失败。
func TestPolicyCacheDoesNotCacheErrors(t *testing.T) {
	ctx := context.Background()
	cache := newPolicyCache(time.Minute)
	var calls int32
	fetch := countingFetch(&calls, nil, ErrTenantNotFound)
	for i := 0; i < 3; i++ {
		if _, err := cache.load(ctx, "missing", fetch); !errors.Is(err, ErrTenantNotFound) {
			t.Fatalf("load #%d: err = %v", i, err)
		}
	}
	if calls != 3 {
		t.Fatalf("fetch calls = %d, want 3（错误不缓存）", calls)
	}
	if p, err := cache.load(ctx, "missing", countingFetch(&calls, &Policy{StorageBackend: "ok"}, nil)); err != nil || p.StorageBackend != "ok" {
		t.Fatalf("后续成功读取应生效: p=%+v err=%v", p, err)
	}
}

// leader 的失败（典型的：客户端断开导致 ctx 取消）不能被跟随者继承——
// 跟随者自己的 ctx 还有效，它应当重试并拿到正确结果。
func TestPolicyCacheFollowerRetriesLeaderError(t *testing.T) {
	ctx := context.Background()
	cache := newPolicyCache(time.Minute)
	leaderStarted := make(chan struct{})
	release := make(chan struct{})
	var calls int32
	fetch := func(_ context.Context, _ string) (*Policy, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(leaderStarted)
			<-release
			return nil, context.Canceled
		}
		return &Policy{StorageBackend: "from-retry"}, nil
	}

	leaderErr := make(chan error, 1)
	go func() {
		_, err := cache.load(ctx, "t1", fetch)
		leaderErr <- err
	}()
	<-leaderStarted

	var follower *Policy
	var followerErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		follower, followerErr = cache.load(ctx, "t1", fetch)
	}()
	// 给跟随者挂到在途 flight 上的时间（它无法自行通知"已加入"）。
	time.Sleep(100 * time.Millisecond)
	close(release)
	<-done

	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader err = %v", err)
	}
	if followerErr != nil || follower == nil || follower.StorageBackend != "from-retry" {
		t.Fatalf("follower 应重试成功: p=%+v err=%v", follower, followerErr)
	}
}

func TestPolicyCacheDisabledByZeroTTL(t *testing.T) {
	ctx := context.Background()
	cache := newPolicyCache(0)
	var calls int32
	fetch := countingFetch(&calls, &Policy{StorageBackend: "local"}, nil)
	for i := 0; i < 3; i++ {
		if _, err := cache.load(ctx, "t1", fetch); err != nil {
			t.Fatalf("load: %v", err)
		}
	}
	if calls != 3 {
		t.Fatalf("ttl=0 时应直读: fetch calls = %d, want 3", calls)
	}
}

// 未启用缓存（cache 为 nil）时 load 必须退回直读，而不是 panic。
func TestPolicyCacheNilReceiverFallsBackToFetch(t *testing.T) {
	var cache *policyCache
	var calls int32
	if _, err := cache.load(context.Background(), "t1", countingFetch(&calls, &Policy{}, nil)); err != nil {
		t.Fatalf("load: %v", err)
	}
	if calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", calls)
	}
	cache.invalidate("t1") // 不得 panic
}

// 启用缓存后，「读 → 改 → 读」必须立刻看到新值：写路径失效是强一致的唯一保证，
// 少了它就只能等 TTL（本用例 TTL=1h，不失效就会拿到旧值）。
func TestSQLiteSetPolicyInvalidatesCache(t *testing.T) {
	ctx := context.Background()
	s := newTenantStore(t).WithPolicyCache(time.Hour)
	const tenant = db.LocalTenantID

	if err := s.SetPolicy(ctx, tenant, Policy{StorageBackend: "local", SourceRetentionDays: 7}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if p, err := s.GetPolicy(ctx, tenant); err != nil || p.SourceRetentionDays != 7 {
		t.Fatalf("first read: p=%+v err=%v", p, err)
	}
	if err := s.SetPolicy(ctx, tenant, Policy{StorageBackend: "s3", SourceRetentionDays: 30}); err != nil {
		t.Fatalf("set: %v", err)
	}
	p, err := s.GetPolicy(ctx, tenant)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if p.SourceRetentionDays != 30 || p.StorageBackend != "s3" {
		t.Fatalf("改策略后仍读到旧值: %+v（SetPolicy 未失效缓存）", p)
	}
	// 三个派生读取也必须走同一份缓存语义。
	if backend, _ := s.ObjectStoreBackend(ctx, tenant); backend != "s3" {
		t.Fatalf("ObjectStoreBackend = %q", backend)
	}
}

// newTenantStoreBench 是 newTenantStore 的 testing.TB 版本（benchmark 用）。
func newTenantStoreBench(tb testing.TB) *SQLiteStore {
	tb.Helper()
	ctx := context.Background()
	sqldb, err := db.OpenSQLite(ctx, filepath.Join(tb.TempDir(), "ppts.db"))
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	tb.Cleanup(func() { _ = sqldb.Close() })
	fsys, _ := migrations.SQLite()
	if _, err := db.MigrateSQLite(ctx, sqldb, fsys); err != nil {
		tb.Fatalf("migrate: %v", err)
	}
	if err := db.EnsureLocalIdentity(ctx, sqldb); err != nil {
		tb.Fatalf("identity: %v", err)
	}
	return NewSQLiteStore(sqldb)
}

// 基线/对比基准：同一个 store，只差缓存开关。
// P3 的出口判据是「前后数字」，所以两个基准都留在仓库里，改完能复测同一组。
func benchmarkGetPolicySQLite(b *testing.B, ttl time.Duration) {
	ctx := context.Background()
	s := newTenantStoreBench(b).WithPolicyCache(ttl)
	const tenant = db.LocalTenantID
	if err := s.SetPolicy(ctx, tenant, Policy{
		StorageBackend: "local", StorageRegion: "ap-guangzhou",
		SourceRetentionDays: 30, EnvelopeEncryption: true,
	}); err != nil {
		b.Fatalf("seed: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.GetPolicy(ctx, tenant); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetPolicyUncached(b *testing.B) { benchmarkGetPolicySQLite(b, 0) }
func BenchmarkGetPolicyCached(b *testing.B)   { benchmarkGetPolicySQLite(b, time.Hour) }
