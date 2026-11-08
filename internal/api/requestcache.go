package api

import (
	"context"
	"sync"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/project"
)

// 本文件放**单次 HTTP 请求**范围内的只读对象缓存。
//
// 背景：批处理端点（apply-all / apply-batch）对每一页都要问一次
// AnimationPageResolver.Resolve，而它的实现每页都读并解码**整份** document.json，
// 再读一次配音时间轴。23 页的 deck 于是变成 23 次「整份文档读+解码」加 23 次
// 「2 条 SQL 查任务 + 2 次对象读拿时间轴」——而这些数据在一次请求里根本不会变。
//
// 为什么挂在 context 上而不是给 Resolve 加参数：Resolve 是接口方法
// （AnimationPageResolver），改签名要动全部测试替身与两处装配；而这份缓存的正确
// 生命周期恰好就是「一次请求」，context 是它唯一的自然锚点（与 tenant.WithContext 同形）。
//
// 只能用于**只读**路径：缓存里的 *project.Document 会被同一个请求里的多个 handler
// 共享，谁改它别人就看到改后的值。写入（导入、解析、备注）都发生在 worker 或
// 单页端点里，那些路径不开缓存，因此这里只做读缓存是安全的。

type requestCacheKey struct{}

// documentCacheKey 定位一份解析产物：同一租户同一项目的某个源版本。
type documentCacheKey struct {
	tenantID  string
	projectID string
	revision  int
}

// timelineCacheKey 只需要项目级：页时长来自「本项目最近一次成功配音」那一份时间轴，
// 整份 deck 共用，与页无关。
type timelineCacheKey struct {
	tenantID  string
	projectID string
}

type requestCache struct {
	documents *memo[*project.Document]
	timelines *memo[*app.TimelineAsset]
}

// withRequestCache 给 ctx 挂一份请求级缓存。幂等：已有则原样返回，
// 免得同一个请求里两处各自建一份空缓存、谁也命中不了谁。
func withRequestCache(ctx context.Context) context.Context {
	if requestCacheFrom(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, requestCacheKey{}, &requestCache{
		documents: &memo[*project.Document]{},
		timelines: &memo[*app.TimelineAsset]{},
	})
}

// requestCacheFrom 取当前请求的缓存。未开启时返回 nil，调用方按原路径直读——
// 缓存是加速手段，不是正确性的前提。
func requestCacheFrom(ctx context.Context) *requestCache {
	cache, _ := ctx.Value(requestCacheKey{}).(*requestCache)
	return cache
}

// memo 是只做「同 key 只算一次」的最小缓存。
//
// 值可以是 nil，且 nil 与"没算过"必须能区分：读不到文档、没有时间轴都是**结论**，
// 缓存它们正是这里的收益——否则一次存储抖动会让同一请求的后续页各自再超时一遍。
type memo[T any] struct {
	mu      sync.Mutex
	entries map[any]T
}

func (m *memo[T]) load(key any) (T, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.entries[key]
	return v, ok
}

func (m *memo[T]) store(key any, v T) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[any]T, 4)
	}
	m.entries[key] = v
}
