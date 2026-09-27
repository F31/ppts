package api

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 认证端点的限流与登录失败锁定（P2-B1）。
//
// 计数后端可替换（见 ratelimit_shared.go）：多副本下走共享存储，共享存储不可用时
// 退回进程内计数并置 ppts_ratelimit_degraded=1。这是刻意的分层 —— 限流绝不能因为
// 存储故障而变成"不限流"，也不能在存储故障时装作一切正常。
//
// 注意 failureGuard 仍是进程内的（见其注释）：账号维度的失败计数与锁定跨副本共享
// 需要额外的"成功即清零"语义，留作后续项，不冒充已完成。

// failureGuard 记录账号维度的连续失败，达到阈值后在 lockFor 内拒绝登录。
//
// 已知残留（P2-B1 未覆盖，不冒充已完成）：它仍是**进程内**的，多副本下攻击者把
// 请求均匀打在各副本上即可获得 N 倍尝试次数。未纳入本轮的原因不是"不重要"，而是
// 它的语义比计数窗口更复杂 —— 需要跨副本共享"连续失败次数"并在登录成功时清零，
// 且清零必须是原子的（否则并发成功登录会把锁定状态留在库里误伤真实用户）。
// 当前由 IP 维度限流兜住成本，本项单列待办。
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
//
// 计数后端由外部注入（ratelimitBackend(pool, logger)）：单副本与多副本共用同一份
// 限额配置，差别只在计数落在哪。这样"限额"这个产品决策只有一处定义。
type authLimits struct {
	register  *sharedLimiter
	login     *sharedLimiter
	forgot    *sharedLimiter
	resend    *sharedLimiter
	loginFail *failureGuard
}

// newAuthLimits 按环境可覆盖的档位构建（默认值见下）。
func newAuthLimits(cfg authRateConfig, backend limiterBackend) *authLimits {
	return &authLimits{
		register:  newSharedLimiter(backend, "auth.register", time.Hour, cfg.registerPerHour),
		login:     newSharedLimiter(backend, "auth.login", 15*time.Minute, cfg.loginPer15Min),
		forgot:    newSharedLimiter(backend, "auth.forgot", time.Hour, cfg.forgotPerHour),
		resend:    newSharedLimiter(backend, "auth.resend", time.Hour, cfg.resendPerHour),
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
