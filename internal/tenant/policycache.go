package tenant

import (
	"context"
	"sync"
	"time"
)

// DefaultPolicyCacheTTL 是租户策略进程内缓存的默认有效期。
//
// 30 秒的依据：策略读发生在每一次对象操作上（加密开关 + 后端路由各一次），是热路径；
// 而策略本身是**管理面**配置，改动由运营/管理员发起，容忍秒级延迟。TTL 越短越接近
// 直读，越长则「改策略后要等多久生效」越长——跨进程无法失效，只能靠这个上界兜住。
// 要排查「改了没生效」或回到直读，把 PPTS_TENANT_POLICY_CACHE 置 false。
const DefaultPolicyCacheTTL = 30 * time.Second

// policyEntry 是缓存项：值与过期时刻。policy 只读共享，见 policyCache.load 的说明。
type policyEntry struct {
	policy  *Policy
	expires time.Time
}

// policyFlight 表示一次正在进行的加载，同一租户的并发未命中在此合流。
type policyFlight struct {
	done   chan struct{}
	policy *Policy
	err    error
}

// policyCache 是租户策略的进程内读缓存。
//
// 职责单一：只回答「这个租户的策略在 TTL 内是否已经读过」。它不做 SQL、不解析 JSON、
// 不判断租户是否存在——那些仍由各自的 store 负责（PGStore/SQLiteStore 只在未命中时
// 调用自己的 fetch）。这样两套存储共用同一份缓存语义，也不必在两边各写一遍 TTL 逻辑。
type policyCache struct {
	ttl time.Duration
	// now 便于测试推进时间；生产恒为 time.Now。
	now func() time.Time

	mu       sync.Mutex
	entries  map[string]policyEntry
	inflight map[string]*policyFlight
	hits     int
	misses   int
}

func newPolicyCache(ttl time.Duration) *policyCache {
	return &policyCache{
		ttl:      ttl,
		now:      time.Now,
		entries:  make(map[string]policyEntry),
		inflight: make(map[string]*policyFlight),
	}
}

// load 返回租户策略：TTL 内直接给缓存值，否则调用 fetch 读一次库。
//
// 三条约束：
//  1. 返回的 *Policy 由缓存共享（不是每次拷贝）。调用方只读；要改策略必须构造新值走
//     SetPolicy——就地改字段会污染缓存、并与其他请求的读并发冲突。
//  2. 错误不缓存。租户不存在、数据库抖动都应在下一次调用重新判定，否则一次瞬时故障
//     会被放大成 TTL 长度的持续失败。
//  3. 同一租户的并发未命中只放一个请求进 fetch，其余等它的结果。跟随者拿到 leader 的
//     错误时会自己重试一次：leader 的 ctx 取消（比如客户端断开）不代表跟随者的请求
//     也该失败，把 leader 的取消当结论会让一批无关请求报错。
func (c *policyCache) load(ctx context.Context, tenantID string, fetch func(context.Context, string) (*Policy, error)) (*Policy, error) {
	if c == nil || c.ttl <= 0 {
		return fetch(ctx, tenantID)
	}
	retried := false
	for {
		now := c.now()
		c.mu.Lock()
		if e, ok := c.entries[tenantID]; ok && now.Before(e.expires) {
			c.hits++
			c.mu.Unlock()
			return e.policy, nil
		}
		if f, ok := c.inflight[tenantID]; ok {
			c.mu.Unlock()
			select {
			case <-f.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if f.err == nil {
				return f.policy, nil
			}
			if retried {
				return nil, f.err
			}
			// 跟随者自己的 ctx 仍然有效：不继承 leader 的失败，重新走一轮（此次无在途，
			// 自己成为 leader）。
			retried = true
			continue
		}
		f := &policyFlight{done: make(chan struct{})}
		c.inflight[tenantID] = f
		c.misses++
		c.mu.Unlock()

		p, err := fetch(ctx, tenantID)
		f.policy, f.err = p, err

		c.mu.Lock()
		delete(c.inflight, tenantID)
		if err == nil && p != nil {
			c.entries[tenantID] = policyEntry{policy: p, expires: c.now().Add(c.ttl)}
		}
		c.mu.Unlock()
		// 必须先写 f.policy/f.err、再 close(f.done)：channel 关闭是发布点，跟随者看到
		// 关闭之后才去读结果。反过来写就是数据竞争。
		close(f.done)
		return p, err
	}
}

// invalidate 丢弃某租户的缓存项。写路径（SetPolicy）成功后必须调用。
func (c *policyCache) invalidate(tenantID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, tenantID)
	c.mu.Unlock()
}

// stats 返回命中/未命中计数（未命中含合流掉的并发请求的 leader 计数）。
func (c *policyCache) stats() (hits, misses int) {
	if c == nil {
		return 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses
}
