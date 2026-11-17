package app

import (
	"context"
	"sync"

	"github.com/F31/ppts/internal/animation/bind"
)

// directorCache 是进程内拍表导演缓存。temperature=0 仍不确定，同一提示词
// 必须回同一段 JSON，否则同一次 worker 里 HTML 与 MP4 会对不上。
const maxDirectorCache = 256

type directorCache struct {
	mu    sync.Mutex
	items map[string]string
}

func newDirectorCache() bind.CompletionCache {
	return &directorCache{items: make(map[string]string, 32)}
}

func (c *directorCache) Get(_ context.Context, key string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[key]
	return v, ok, nil
}

func (c *directorCache) Put(_ context.Context, key, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= maxDirectorCache {
		c.items = make(map[string]string, 32)
	}
	c.items[key] = text
	return nil
}
