package api

import (
	"context"
	"database/sql"
	"errors"
	"expvar"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ─── 假共享后端 ───────────────────────────────────────────────────────────

// fakeSharedBackend 模拟"所有副本共用的计数存储"（库 semantics 与 pgBackend 一致）。
// 用它可以在没有 PG 的情况下验证"两个副本是否真的共享计数"。
type fakeSharedBackend struct {
	mu     sync.Mutex
	counts map[string]int64
	starts map[string]time.Time
	err    error // 非空时模拟存储不可用
	calls  int
}

func newFakeShared() *fakeSharedBackend {
	return &fakeSharedBackend{counts: map[string]int64{}, starts: map[string]time.Time{}}
}

func (f *fakeSharedBackend) Allow(_ context.Context, scope, key string, window time.Duration, limit int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	k := scope + "\x00" + key
	start := bucketStart(time.Now(), window)
	if s, ok := f.starts[k]; !ok || !s.Equal(start) {
		f.starts[k] = start
		f.counts[k] = 1
		return true, nil
	}
	if f.counts[k] < int64(limit)+countSlack {
		f.counts[k]++
	}
	return f.counts[k] <= int64(limit), nil
}

// ─── 反向验证：多副本必须共享计数 ─────────────────────────────────────────

// TestSharedBackendCountsAcrossInstances 是 P2-B1 的核心判据。
//
// 反向验证方式：两个 limiter 实例 = 两个副本。它们共用一个共享后端时，限额必须**整体**生效
// （4 次里只放行 3 次）。若把后端换回各自独立的进程内计数（旧实现），每个实例都能放行 3 次
// —— 实际放行量变成限额 × 副本数，本用例会翻红。
//
// 第二个子测试把"旧行为"显式固化成断言：任何改回进程内计数的改动都应立即暴露。
func TestSharedBackendCountsAcrossInstances(t *testing.T) {
	ctx := context.Background()

	t.Run("shared backend enforces one limit across instances", func(t *testing.T) {
		shared := newFakeShared()
		// 两个"副本"：各自构造 limiter，但共享同一个后端。
		a := newSharedLimiter(shared, "auth.login", time.Minute, 3)
		b := newSharedLimiter(shared, "auth.login", time.Minute, 3)

		allowed := 0
		for i := 0; i < 4; i++ {
			// 交替打在两个副本上，模拟负载均衡轮询。
			if (i%2 == 0 && a.allow(ctx, "1.2.3.4")) || (i%2 == 1 && b.allow(ctx, "1.2.3.4")) {
				allowed++
			}
		}
		if allowed != 3 {
			t.Fatalf("shared backend across 2 instances: allowed = %d, want 3 (limit must be global)", allowed)
		}
	})

	t.Run("per-process counters dilute the limit (old behaviour)", func(t *testing.T) {
		a := newSharedLimiter(newMemoryBackend(), "auth.login", time.Minute, 3)
		b := newSharedLimiter(newMemoryBackend(), "auth.login", time.Minute, 3)

		allowed := 0
		for i := 0; i < 6; i++ {
			if (i%2 == 0 && a.allow(ctx, "1.2.3.4")) || (i%2 == 1 && b.allow(ctx, "1.2.3.4")) {
				allowed++
			}
		}
		// 这就是要修的缺陷：两个副本各自放行 3 次 = 6 次，限额被稀释一倍。
		if allowed != 6 {
			t.Fatalf("per-process counters: allowed = %d, want 6 (each instance counts alone)", allowed)
		}
	})
}

// ─── 降级与恢复 ───────────────────────────────────────────────────────────

func degradeGauge() *expvar.Int { return expvar.Get("ppts_ratelimit_degraded").(*expvar.Int) }
func backendVar() *expvar.String {
	return expvar.Get("ppts_ratelimit_backend").(*expvar.String)
}

// TestFailoverBackendDegradesToMemory 校验共享存储挂掉时：
// ① 仍继续限流（不是放行）；② 降级状态被显式暴露（不得与正常态同外观）。
//
// 反向验证：若去掉降级路径（Allow 直接把 err 传给 sharedLimiter，后者 fail-closed 返回 false），
// 第一次调用就会被拒 → 用例翻红。
func TestFailoverBackendDegradesToMemory(t *testing.T) {
	defer resetRateLimitMetrics()

	shared := newFakeShared()
	shared.err = errors.New("connection refused")
	fb := newFailoverBackend(shared, newMemoryBackend(), log.New(os.Stderr, "", 0))

	ctx := context.Background()
	if ok, err := fb.Allow(ctx, "auth.login", "1.2.3.4", time.Minute, 3); err != nil || !ok {
		t.Fatalf("degraded backend should still allow within limit (ok=%v err=%v): not fail-open, not fail-closed-to-zero", ok, err)
	}
	if degradeGauge().Value() != 1 {
		t.Fatalf("ppts_ratelimit_degraded = %d, want 1 (degradation must be observable)", degradeGauge().Value())
	}
	if got := backendVar().Value(); got != "memory" {
		t.Fatalf("ppts_ratelimit_backend = %q, want %q", got, "memory")
	}

	// 降级后仍按限额工作（进程内计数）。
	for i := 0; i < 2; i++ {
		if ok, _ := fb.Allow(ctx, "auth.login", "1.2.3.4", time.Minute, 3); !ok {
			t.Fatalf("allow #%d within limit should pass while degraded", i+2)
		}
	}
	if ok, _ := fb.Allow(ctx, "auth.login", "1.2.3.4", time.Minute, 3); ok {
		t.Fatal("4th allow should be rejected while degraded")
	}
}

// TestFailoverRecoversWhenSharedReturns 校验共享存储恢复后自动切回（惰性探测）。
//
// 反向验证：若没有恢复逻辑，第二个断言的 backend 会停在 memory → 用例翻红。
func TestFailoverRecoversWhenSharedReturns(t *testing.T) {
	defer resetRateLimitMetrics()

	shared := newFakeShared()
	shared.err = errors.New("down")
	now := time.Now()
	fb := newFailoverBackend(shared, newMemoryBackend(), nil)
	fb.now = func() time.Time { return now }

	ctx := context.Background()
	_, _ = fb.Allow(ctx, "auth.login", "1.2.3.4", time.Minute, 3)
	if !fb.degradedNow() {
		t.Fatal("should be degraded after primary error")
	}
	// 探测期未到：不再打扰已经坏掉的共享后端（避免每请求多一次超时）。
	callsBefore := shared.calls
	_, _ = fb.Allow(ctx, "auth.login", "1.2.3.4", time.Minute, 3)
	if shared.calls != callsBefore {
		t.Fatalf("primary probed %d extra times before retry window", shared.calls-callsBefore)
	}

	shared.err = nil
	now = now.Add(retryAfter + time.Second)
	if _, err := fb.Allow(ctx, "auth.login", "1.2.3.4", time.Minute, 3); err != nil {
		t.Fatalf("allow after recovery: %v", err)
	}
	if fb.degradedNow() {
		t.Fatal("should have recovered once primary succeeds")
	}
	if got := backendVar().Value(); got != "shared" {
		t.Fatalf("ppts_ratelimit_backend = %q, want %q", got, "shared")
	}
	if degradeGauge().Value() != 0 {
		t.Fatalf("ppts_ratelimit_degraded = %d, want 0 after recovery", degradeGauge().Value())
	}
}

func resetRateLimitMetrics() {
	degradeGauge().Set(0)
	backendVar().Set("shared")
}

// ─── 窗口对齐 ─────────────────────────────────────────────────────────────

// TestBucketStartAlignedAcrossInstances 校验窗口起点是各实例算出同一个值 —— 这是
// "计数能共享"的前提。若改回"首次命中起算"，两个实例在同一分钟的起点不同 → 用例翻红。
func TestBucketStartAlignedAcrossInstances(t *testing.T) {
	base := time.Date(2026, 9, 27, 10, 37, 42, 123456789, time.UTC)
	a := bucketStart(base, 15*time.Minute)
	b := bucketStart(base.Add(3*time.Minute), 15*time.Minute) // 同一窗口内的另一个时刻
	if !a.Equal(b) {
		t.Fatalf("bucket start not aligned: %v vs %v", a, b)
	}
	if got := a; got.Minute()%15 != 0 || got.Second() != 0 || got.Nanosecond() != 0 {
		t.Fatalf("bucket start %v is not on a 15-minute boundary", got)
	}
	// 跨边界后必须换窗口。
	c := bucketStart(base.Add(20*time.Minute), 15*time.Minute)
	if c.Equal(a) {
		t.Fatal("window should roll over after 15 minutes")
	}
	// 非零窗口（0 表示不设限）不应 panic。
	_ = bucketStart(base, 0)
}

// ─── SQL 语义（SQLite 内存库实跑）─────────────────────────────────────────

// rateLimitUpsertSQLite 与 rateLimitUpsertPG **同构**（占位符不同方言，语义逐条对应）。
// 本机无 PG，用 SQLite 实跑它来验证语义；再用 TestRateLimitPGSQLShape 把两条语句绑定起来，
// 防止只改一处（改了 PG 忘了同步语义 / 反之）。
const rateLimitUpsertSQLite = `INSERT INTO ppts_rate_limit (scope, bucket_key, window_start, count)
VALUES (?, ?, ?, 1)
ON CONFLICT (scope, bucket_key) DO UPDATE SET
    window_start = excluded.window_start,
    count = CASE
        WHEN ppts_rate_limit.window_start <> excluded.window_start THEN 1
        WHEN ppts_rate_limit.count >= ? THEN ppts_rate_limit.count
        ELSE ppts_rate_limit.count + 1
    END,
    updated_at = '2026-09-27T00:00:00Z'
RETURNING count`

const rateLimitDDLSQLite = `CREATE TABLE ppts_rate_limit (
    scope        TEXT NOT NULL,
    bucket_key   TEXT NOT NULL,
    window_start TEXT NOT NULL,
    count        INTEGER NOT NULL DEFAULT 0,
    updated_at   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (scope, bucket_key)
)`

// TestRateLimitSQLSemantics 在 SQLite 上真跑与生产同构的 UPSERT，验证三条语义：
// 累加、超限拒绝、窗口切换重置。
//
// 反向验证：把 CASE 里的窗口比较去掉（退化成永远累加）→ 第三个断言翻红；
// 把 >= cap 分支去掉 → 计数帽子失效（由 cap 断言守着）。
func TestRateLimitSQLSemantics(t *testing.T) {
	sqldb, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer sqldb.Close()
	sqldb.SetMaxOpenConns(1)
	if _, err := sqldb.Exec(rateLimitDDLSQLite); err != nil {
		t.Fatalf("ddl: %v", err)
	}

	start := "2026-09-27T10:00:00Z"
	limit := 3
	capCount := limit + countSlack
	var got int64
	incr := func(windowStart string) int64 {
		var n int64
		if err := sqldb.QueryRow(rateLimitUpsertSQLite,
			"auth.login", "1.2.3.4", windowStart, capCount).Scan(&n); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		return n
	}

	for i := 1; i <= limit; i++ {
		if got = incr(start); got != int64(i) {
			t.Fatalf("call #%d: count = %d, want %d", i, got, i)
		}
	}
	// 超限：计数仍返回，但 > limit（调用方据此拒绝）。
	if got = incr(start); got <= int64(limit) {
		t.Fatalf("over-limit call: count = %d, want > %d", got, limit)
	}
	// 计数帽子：撞库时不能把单行撑到无界。
	for i := 0; i < 5; i++ {
		incr(start)
	}
	if got = incr(start); got > int64(capCount) {
		t.Fatalf("count = %d exceeds cap %d", got, capCount)
	}
	// 窗口切换：重置为 1。
	if got = incr("2026-09-27T10:15:00Z"); got != 1 {
		t.Fatalf("new window: count = %d, want 1", got)
	}
	// 不同 key / 不同 scope 互不干扰。
	var n int64
	if err := sqldb.QueryRow(rateLimitUpsertSQLite,
		"auth.login", "5.6.7.8", start, capCount).Scan(&n); err != nil || n != 1 {
		t.Fatalf("other key: count = %d err = %v, want 1", n, err)
	}
	if err := sqldb.QueryRow(rateLimitUpsertSQLite,
		"auth.register", "1.2.3.4", start, capCount).Scan(&n); err != nil || n != 1 {
		t.Fatalf("other scope: count = %d err = %v, want 1", n, err)
	}
}

// TestRateLimitPGSQLShape 把生产的 PG 语句钉住：改动必须有意识地改，并同步 SQLite 同构语句。
// 它守的是"漏改"——两条语句必须同时具备 UPSERT、窗口重置、RETURNING 三个要素。
func TestRateLimitPGSQLShape(t *testing.T) {
	for _, s := range []string{rateLimitUpsertPG, rateLimitUpsertSQLite} {
		if !strings.Contains(s, "ON CONFLICT (scope, bucket_key) DO UPDATE") {
			t.Errorf("missing atomic upsert clause:\n%s", s)
		}
		if !strings.Contains(s, "window_start <> EXCLUDED.window_start") &&
			!strings.Contains(s, "window_start <> excluded.window_start") {
			t.Errorf("missing window rollover condition:\n%s", s)
		}
		if !strings.Contains(s, "RETURNING count") {
			t.Errorf("missing RETURNING count:\n%s", s)
		}
	}
	if !strings.Contains(rateLimitUpsertPG, "ppts_rate_limit") {
		t.Error("PG statement must target ppts_rate_limit")
	}
}

// ─── 后端选择 ─────────────────────────────────────────────────────────────

func TestRateLimitBackendName(t *testing.T) {
	cases := map[string]string{
		"":         "shared",
		"shared":   "shared",
		"MEMORY":   "memory",
		" memory ": "memory",
		"redis":    "shared", // 未识别的取值按默认（shared），不静默降级
	}
	for in, want := range cases {
		t.Setenv("PPTS_RATELIMIT_BACKEND", in)
		if got := rateLimitBackendName(); got != want {
			t.Errorf("rateLimitBackendName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRateLimitBackendNilPoolUsesMemory 校验单租户 profile（无 PG）走进程内后端，
// 且**不**被标记为降级 —— "单副本本就用进程内"与"共享存储挂了"必须能区分开。
func TestRateLimitBackendNilPoolUsesMemory(t *testing.T) {
	defer resetRateLimitMetrics()
	b := rateLimitBackend(nil, nil)
	if _, ok := b.(*memoryBackend); !ok {
		t.Fatalf("backend = %T, want *memoryBackend", b)
	}
	if degradeGauge().Value() != 0 {
		t.Fatalf("ppts_ratelimit_degraded = %d, want 0 (single process is not a degradation)", degradeGauge().Value())
	}
	if _, ok := b.(purgeable); ok {
		t.Fatal("memory backend should not need purging (no goroutine spawned)")
	}
}

// TestPurgeRemovesOnlyExpiredWindows 校验清理只删过期窗口、不碰正在使用的桶。
func TestPurgeRemovesOnlyExpiredWindows(t *testing.T) {
	sqldb, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer sqldb.Close()
	sqldb.SetMaxOpenConns(1)
	if _, err := sqldb.Exec(rateLimitDDLSQLite); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	now := time.Now().UTC()
	fresh := now.Format(time.RFC3339)
	stale := now.Add(-48 * time.Hour).Format(time.RFC3339)
	for _, tc := range []struct{ key, ts string }{
		{"fresh", fresh},
		{"stale", stale},
	} {
		if _, err := sqldb.Exec(`INSERT INTO ppts_rate_limit (scope, bucket_key, window_start, count, updated_at)
			VALUES ('auth.login', ?, ?, 1, ?)`, tc.key, fresh, tc.ts); err != nil {
			t.Fatalf("seed %s: %v", tc.key, err)
		}
	}
	res, err := sqldb.Exec(`DELETE FROM ppts_rate_limit WHERE updated_at < ?`,
		now.Add(-rateLimitPurgeOlder).Format(time.RFC3339))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		t.Fatalf("purged %d rows, want 1", n)
	}
	var remaining string
	if err := sqldb.QueryRow(`SELECT bucket_key FROM ppts_rate_limit`).Scan(&remaining); err != nil {
		t.Fatalf("remaining row: %v", err)
	}
	if remaining != "fresh" {
		t.Fatalf("remaining = %q, want %q (active window must survive purge)", remaining, "fresh")
	}
}
