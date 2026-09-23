package api

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 认证端点的内存限流与登录失败锁定（单进程）。多实例部署需替换为共享存储（Redis），
// 但作为第一道防线已能显著抬高撞库/批量注册成本。
type fixedWindowLimiter struct {
	mu     sync.Mutex
	window time.Duration
	limit  int
	hits   map[string]*windowEntry
}

type windowEntry struct {
	count   int
	resetAt time.Time
}

func newFixedWindowLimiter(window time.Duration, limit int) *fixedWindowLimiter {
	return &fixedWindowLimiter{window: window, limit: limit, hits: make(map[string]*windowEntry)}
}

// allow 报告 key 在窗口内是否未超限；超限返回 false。
func (l *fixedWindowLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) > 50000 { // 防止 map 无界增长
		for k, e := range l.hits {
			if now.After(e.resetAt) {
				delete(l.hits, k)
			}
		}
	}
	e := l.hits[key]
	if e == nil || now.After(e.resetAt) {
		l.hits[key] = &windowEntry{count: 1, resetAt: now.Add(l.window)}
		return true
	}
	e.count++
	return e.count <= l.limit
}

// failureGuard 记录账号维度的连续失败，达到阈值后在 lockFor 内拒绝登录。
type failureGuard struct {
	mu       sync.Mutex
	max      int
	lockFor  time.Duration
	counters map[string]*failureEntry
}

type failureEntry struct {
	count    int
	lockTill time.Time
	lastAt   time.Time
}

func newFailureGuard(max int, lockFor time.Duration) *failureGuard {
	return &failureGuard{max: max, lockFor: lockFor, counters: make(map[string]*failureEntry)}
}

// locked 报告该账号当前是否处于锁定中。
func (g *failureGuard) locked(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := g.counters[key]
	if e == nil {
		return false
	}
	return time.Now().Before(e.lockTill)
}

// fail 记录一次失败；达到阈值则进入锁定。
func (g *failureGuard) fail(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	e := g.counters[key]
	if e == nil {
		e = &failureEntry{}
		g.counters[key] = e
	}
	// 距上次失败超过 15 分钟则重置计数。
	if !e.lastAt.IsZero() && now.Sub(e.lastAt) > 15*time.Minute {
		e.count = 0
	}
	e.count++
	e.lastAt = now
	if e.count >= g.max {
		e.lockTill = now.Add(g.lockFor)
		e.count = 0
	}
	if len(g.counters) > 50000 {
		for k, v := range g.counters {
			if now.After(v.lockTill) && now.Sub(v.lastAt) > 15*time.Minute {
				delete(g.counters, k)
			}
		}
	}
}

// reset 清除账号失败计数（登录成功）。
func (g *failureGuard) reset(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.counters, key)
}

// authLimits 汇总各认证端点的限流器。
type authLimits struct {
	register  *fixedWindowLimiter
	login     *fixedWindowLimiter
	forgot    *fixedWindowLimiter
	resend    *fixedWindowLimiter
	loginFail *failureGuard
}

// newAuthLimits 按环境可覆盖的档位构建（默认值见下）。
func newAuthLimits(cfg authRateConfig) *authLimits {
	return &authLimits{
		register:  newFixedWindowLimiter(time.Hour, cfg.registerPerHour),
		login:     newFixedWindowLimiter(15*time.Minute, cfg.loginPer15Min),
		forgot:    newFixedWindowLimiter(time.Hour, cfg.forgotPerHour),
		resend:    newFixedWindowLimiter(time.Hour, cfg.resendPerHour),
		loginFail: newFailureGuard(cfg.loginFailMax, cfg.loginLockFor),
	}
}

type authRateConfig struct {
	registerPerHour int
	loginPer15Min   int
	forgotPerHour   int
	resendPerHour   int
	loginFailMax    int
	loginLockFor    time.Duration
}

func defaultAuthRateConfig() authRateConfig {
	return authRateConfig{
		registerPerHour: 10,
		loginPer15Min:   30,
		forgotPerHour:   5,
		resendPerHour:   5,
		loginFailMax:    5,
		loginLockFor:    15 * time.Minute,
	}
}

// clientIP 取客户端 IP：信任代理时优先 X-Forwarded-For 首个地址（PPTS_TRUST_PROXY=true）。
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				return strings.TrimSpace(xff[:i])
			}
			return strings.TrimSpace(xff)
		}
		if rip := r.Header.Get("X-Real-IP"); rip != "" {
			return strings.TrimSpace(rip)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
