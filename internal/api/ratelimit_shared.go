package api

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/F31/ppts/internal/observability"
	"github.com/jackc/pgx/v5/pgxpool"
)

// errBackendUnavailable 表示后端本身不可用（区别于"超限"）。
// 它只对 failoverBackend 有意义：见此错误即切换到降级路径。
var errBackendUnavailable = errors.New("rate limit backend unavailable")

// 分布式限流计数（P2-B1）。
//
// 为什么必须挪出进程内存：限流计数放在进程里时，实际放行量 = 单副本限额 × 副本数。
// 这不只是"限得松一点"—— 承受撞库/批量注册时的第一反应恰恰是扩副本，于是防护强度
// 随扩容线性稀释，而看板上什么都不会变（每个副本都认为自己守住了配额）。
//
// 因此计数必须落在所有副本共享的存储上。但共享存储本身会挂，所以这里保留
// 进程内实现作为降级路径：**降级不是取消限流，而是退回"按副本计数"的弱限流**，
// 并且必须可观测（ppts_ratelimit_degraded=1 + ppts_ratelimit_backend=memory），
// 否则"配额被稀释"这件事会与"配额正常"长得一模一样（承 A26：降级不得与正常态同外观）。
//
// 窗口语义的一点变化（必须知道）：进程内版本是"首次命中起 window 时长"，
// 分布式版本改为 **epoch 对齐的固定窗口**（now.UTC().Truncate(window)）。
// 理由：窗口起点必须各副本算出同一个值，否则每个副本都在自己的窗口里从头计数。
// 代价是窗口边界可能出现突刺（边界前后各打满一次限额），这是固定窗口的固有取舍；
// 认证类端点限额本身较低，可接受。

// limiterBackend 是限流计数后端。实现必须是原子的：并发调用不得超发。
type limiterBackend interface {
	// Allow 给 (scope, key) 在当前窗口内 +1，返回是否未超限。
	// err 非空表示后端不可用（调用方据此降级）；不得因为"超限"返回 error。
	Allow(ctx context.Context, scope, key string, window time.Duration, limit int) (bool, error)
}

// purgeable 是可清理过期窗口行的后端（进程内实现靠 map 淘汰，无需清理）。
type purgeable interface {
	Purge(ctx context.Context, olderThan time.Time) (int64, error)
}

// countSlack 是计数帽子相对限额的余量：超限后继续累加到 limit+slack 即停。
//
// 保留余量的目的是**事后能看出攻击强度**（被拒了多少次），完全不累加则无从判断；
// 完全不封顶则撞库会把单行撑到无界（每次尝试都是一次写放大）。
const countSlack = 1000

// primaryTimeout 是共享后端的单次调用上限。
// 限流在请求关键路径上，DB 抖动时不能把请求拖到客户端超时 —— 宁可快速降级到进程内计数。
const primaryTimeout = 2 * time.Second

// retryAfter 是降级后再次尝试共享后端的间隔（惰性探测恢复）。
const retryAfter = 15 * time.Second

// bucketStart 计算窗口起点：UTC 上按 window 向 epoch 对齐，各副本得到同一值。
func bucketStart(now time.Time, window time.Duration) time.Time {
	if window <= 0 {
		return now.UTC()
	}
	return now.UTC().Truncate(window)
}

// ─── 进程内后端（单副本 / 降级路径）────────────────────────────────────────

// memoryBackend 是进程内固定窗口计数。语义与共享后端一致（同样的窗口对齐与计数帽子），
// 差别只在可见范围：仅本进程。
type memoryBackend struct {
	mu      sync.Mutex
	entries map[string]*rateEntry
}

type rateEntry struct {
	windowStart time.Time
	count       int64
}

func newMemoryBackend() *memoryBackend {
	return &memoryBackend{entries: make(map[string]*rateEntry)}
}

func (b *memoryBackend) Allow(_ context.Context, scope, key string, window time.Duration, limit int) (bool, error) {
	start := bucketStart(time.Now(), window)
	k := scope + "\x00" + key
	b.mu.Lock()
	defer b.mu.Unlock()
	// map 膨胀到阈值才扫：桶数取决于活跃 IP 数，正常量级远低于阈值。
	// 清理判据是"窗口起点早于当前窗口"—— 旧窗口的行永远不会再被命中。
	if len(b.entries) > 50000 {
		for kk, e := range b.entries {
			if e.windowStart.Before(start) {
				delete(b.entries, kk)
			}
		}
	}
	e := b.entries[k]
	if e == nil || !e.windowStart.Equal(start) {
		b.entries[k] = &rateEntry{windowStart: start, count: 1}
		return true, nil
	}
	if e.count < int64(limit)+countSlack {
		e.count++
	}
	return e.count <= int64(limit), nil
}

// ─── 共享后端（PG）────────────────────────────────────────────────────────

// rateLimitUpsertPG 原子地给一个桶 +1 并返回新计数。
//
// 三个要点，缺一不可：
//  1. ON CONFLICT … DO UPDATE：并发下不能有"读—判断—写"的窗口，否则两个副本同时读到
//     count=limit-1 就都放行 —— 正是要修的那个缺陷；
//  2. window_start <> EXCLUDED.window_start 时重置为 1：窗口切换由 UPSERT 顺带完成，
//     不需要任何后台任务翻窗口；
//  3. RETURNING count：计数在库内算完再回传，避免多一次往返造成判断失效。
const rateLimitUpsertPG = `INSERT INTO ppts_rate_limit (scope, bucket_key, window_start, count)
VALUES ($1, $2, $3, 1)
ON CONFLICT (scope, bucket_key) DO UPDATE SET
    window_start = EXCLUDED.window_start,
    count = CASE
        WHEN ppts_rate_limit.window_start <> EXCLUDED.window_start THEN 1
        WHEN ppts_rate_limit.count >= $4 THEN ppts_rate_limit.count
        ELSE ppts_rate_limit.count + 1
    END,
    updated_at = now()
RETURNING count`

const rateLimitPurgePG = `DELETE FROM ppts_rate_limit WHERE updated_at < $1`

type pgBackend struct {
	pool *pgxpool.Pool
}

func newPGBackend(pool *pgxpool.Pool) *pgBackend { return &pgBackend{pool: pool} }

func (b *pgBackend) Allow(ctx context.Context, scope, key string, window time.Duration, limit int) (bool, error) {
	if b == nil || b.pool == nil {
		return false, errBackendUnavailable
	}
	var count int64
	start := bucketStart(time.Now(), window)
	if err := b.pool.QueryRow(ctx, rateLimitUpsertPG,
		scope, key, start, int64(limit)+countSlack).Scan(&count); err != nil {
		return false, err
	}
	return count <= int64(limit), nil
}

func (b *pgBackend) Purge(ctx context.Context, olderThan time.Time) (int64, error) {
	if b == nil || b.pool == nil {
		return 0, errBackendUnavailable
	}
	tag, err := b.pool.Exec(ctx, rateLimitPurgePG, olderThan)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ─── 降级包装 ─────────────────────────────────────────────────────────────

// failoverBackend 在共享后端不可用时退回进程内计数，并周期性探测共享后端是否恢复。
//
// 降级后**继续限流**（不是放行）：弱限流仍显著抬高撞库成本，放行则等于裸奔。
// 恢复是惰性的：降级期间每隔 retryAfter 才试一次共享后端，避免每个请求都去撞一个
// 已经坏掉的连接（那会把 DB 故障放大成所有认证请求的额外 2s 超时）。
type failoverBackend struct {
	primary  limiterBackend
	fallback limiterBackend
	log      *log.Logger
	now      func() time.Time

	mu        sync.Mutex
	degraded  bool
	nextRetry time.Time
}

func newFailoverBackend(primary, fallback limiterBackend, logger *log.Logger) *failoverBackend {
	return &failoverBackend{primary: primary, fallback: fallback, log: logger, now: time.Now}
}

func (b *failoverBackend) Allow(ctx context.Context, scope, key string, window time.Duration, limit int) (bool, error) {
	// 未降级：走共享后端。已降级但探测期到：也试一次，成功即恢复。
	tryPrimary := !b.degradedNow() || b.retryDue()

	if tryPrimary {
		pctx, cancel := context.WithTimeout(ctx, primaryTimeout)
		allowed, err := b.primary.Allow(pctx, scope, key, window, limit)
		cancel()
		if err == nil {
			if b.degradedNow() {
				b.markRecovered()
			}
			return allowed, nil
		}
		b.markDegraded(scope, err)
	}
	return b.fallback.Allow(ctx, scope, key, window, limit)
}

func (b *failoverBackend) degradedNow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.degraded
}

func (b *failoverBackend) retryDue() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.degraded && !b.now().Before(b.nextRetry)
}

func (b *failoverBackend) markDegraded(scope string, err error) {
	b.mu.Lock()
	wasDegraded := b.degraded
	shouldLog := !wasDegraded || !b.now().Before(b.nextRetry)
	b.degraded = true
	b.nextRetry = b.now().Add(retryAfter)
	b.mu.Unlock()

	observability.AddRateLimitDegrade(scope)
	if !wasDegraded {
		observability.SetRateLimitDegraded(true)
		observability.SetRateLimitBackend("memory")
	}
	// 日志节流：持续故障时每 retryAfter 一条，而不是每个请求一条。
	if shouldLog && b.log != nil {
		b.log.Printf("ratelimit: shared backend unavailable, falling back to per-process counters (scope=%s): %v", scope, err)
	}
}

func (b *failoverBackend) markRecovered() {
	b.mu.Lock()
	b.degraded = false
	b.nextRetry = time.Time{}
	b.mu.Unlock()
	observability.SetRateLimitDegraded(false)
	observability.SetRateLimitBackend("shared")
	if b.log != nil {
		b.log.Printf("ratelimit: shared backend recovered")
	}
}

// ─── 端点限流器 ───────────────────────────────────────────────────────────

// sharedLimiter 把"端点 + 维度 + 窗口 + 限额"绑成一个可调用的限流器。
// 后端可替换，因此同一个端点定义在单副本与多副本下走同一套限额配置。
type sharedLimiter struct {
	backend limiterBackend
	scope   string
	window  time.Duration
	limit   int
}

func newSharedLimiter(backend limiterBackend, scope string, window time.Duration, limit int) *sharedLimiter {
	return &sharedLimiter{backend: backend, scope: scope, window: window, limit: limit}
}

// allow 报告 key 在窗口内是否未超限。
//
// 后端彻底不可用（共享挂了且进程内也失败）时返回 false 而非 true：
// 限流是防护设施，故障方向必须是 fail-closed —— "限流器坏了所以放行"等于在最需要它的时候撤掉它。
func (l *sharedLimiter) allow(ctx context.Context, key string) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	allowed, err := l.backend.Allow(ctx, l.scope, key, l.window, l.limit)
	if err != nil {
		return false
	}
	return allowed
}

// ─── 后端选择 ─────────────────────────────────────────────────────────────

// rateLimitBackend 按部署形态挑后端：
//   - pool == nil（SQLite 单租户 profile）：单进程，进程内计数即正确，无需共享；
//   - PPTS_RATELIMIT_BACKEND=memory：显式退回进程内（用于共享存储维护窗口）；
//   - 其余：共享后端 + 进程内降级。
func rateLimitBackend(pool *pgxpool.Pool, logger *log.Logger) limiterBackend {
	if pool == nil || rateLimitBackendName() == "memory" {
		observability.SetRateLimitBackend("memory")
		observability.SetRateLimitDegraded(false)
		return newMemoryBackend()
	}
	observability.SetRateLimitBackend("shared")
	observability.SetRateLimitDegraded(false)
	return newFailoverBackend(newPGBackend(pool), newMemoryBackend(), logger)
}

// rateLimitBackendName 读取期望的后端类型（shared 默认，memory 为显式退回进程内）。
// 与 PPTS_AUTH_DEV_HEADERS 同构：只允许显式取值，未识别的取值按默认处理而不是静默降级。
func rateLimitBackendName() string {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("PPTS_RATELIMIT_BACKEND"))); v {
	case "memory", "local", "process":
		return "memory"
	default:
		return "shared"
	}
}

// ─── 过期窗口清理 ─────────────────────────────────────────────────────────

// rateLimitPurgeInterval 是清理周期；rateLimitPurgeOlder 是保留时长。
// 保留时长取 24h：长于任何一个认证窗口（最长 24h 的每日注册上限），
// 因此"还在用的窗口"绝不会被误删。
const (
	rateLimitPurgeInterval = time.Hour
	rateLimitPurgeOlder    = 24 * time.Hour
)

// startRateLimitPurge 为可清理的后端起一个后台清理协程（进程生命周期）。
// 仅共享后端实现 purgeable；进程内后端靠 map 淘汰，因此测试里构造 handler 不会起协程。
func startRateLimitPurge(backend limiterBackend, logger *log.Logger) {
	p, ok := backend.(purgeable)
	if !ok {
		return
	}
	go func() {
		ticker := time.NewTicker(rateLimitPurgeInterval)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			n, err := p.Purge(ctx, time.Now().Add(-rateLimitPurgeOlder))
			cancel()
			if err != nil {
				if logger != nil {
					logger.Printf("ratelimit: purge failed: %v", err)
				}
				continue
			}
			if n > 0 && logger != nil {
				logger.Printf("ratelimit: purged %d expired buckets", n)
			}
		}
	}()
}
