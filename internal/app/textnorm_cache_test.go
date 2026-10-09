package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	newtn "github.com/F31/textnorm"
)

type textNormBuilderFunc func(context.Context, string, string) (*newtn.Engine, error)

func (f textNormBuilderFunc) Build(ctx context.Context, tenant, lang string) (*newtn.Engine, error) {
	return f(ctx, tenant, lang)
}

func TestTextNormCacheIsolationExpiryAndEviction(t *testing.T) {
	var calls int
	fp := "a"
	inner := textNormBuilderFunc(func(context.Context, string, string) (*newtn.Engine, error) {
		calls++
		return newtn.Compile(newtn.Config{})
	})
	c := NewCachedTextNorm(inner, time.Minute, 2, func() string { return fp }, nil).(*cachedTextNorm)
	build := func(tenant, lang string) *newtn.Engine {
		t.Helper()
		e, err := c.Build(context.Background(), tenant, lang)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	first := build("tenant1", "zh")
	if build("tenant1", "zh") != first || calls != 1 {
		t.Fatal("cache miss on unchanged key")
	}
	if build("tenant2", "zh") == first {
		t.Fatal("tenant isolation failed")
	}
	build("tenant1", "en")
	if build("tenant1", "zh") == first {
		t.Fatal("capacity eviction failed")
	}
	key := c.cacheKey("tenant1", "zh")
	c.mu.Lock()
	e := c.entries[key]
	e.builtAt = time.Now().Add(-2 * time.Minute)
	c.entries[key] = e
	c.mu.Unlock()
	if build("tenant1", "zh") == e.engine {
		t.Fatal("expired engine reused")
	}
	before := calls
	fp = "b"
	build("tenant1", "zh")
	if calls != before+1 {
		t.Fatal("configuration change did not rebuild")
	}
	if len(c.entries) > 2 || len(c.flights) != 0 {
		t.Fatal("cache state leaked")
	}
}

func TestTextNormCacheConcurrentBuildAndCancellation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	inner := textNormBuilderFunc(func(context.Context, string, string) (*newtn.Engine, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return newtn.Compile(newtn.Config{})
	})
	c := NewCachedTextNorm(inner, time.Minute, 4, nil, nil)
	var wg sync.WaitGroup
	results := make(chan *newtn.Engine, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, err := c.Build(context.Background(), "tenant", "zh")
			if err != nil {
				t.Error(err)
				return
			}
			results <- e
		}()
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.Build(ctx, "tenant", "zh"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiting caller: %v", err)
	}
	close(release)
	wg.Wait()
	close(results)
	var first *newtn.Engine
	for e := range results {
		if first == nil {
			first = e
		}
		if e != first {
			t.Fatal("concurrent callers received different engines")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("build calls = %d", calls.Load())
	}
}

func TestTextNormCacheDoesNotCacheErrors(t *testing.T) {
	var calls int
	inner := textNormBuilderFunc(func(context.Context, string, string) (*newtn.Engine, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("store unavailable")
		}
		return newtn.Compile(newtn.Config{})
	})
	c := NewCachedTextNorm(inner, time.Minute, 4, nil, nil).(*cachedTextNorm)
	if _, err := c.Build(context.Background(), "tenant", "zh"); err == nil {
		t.Fatal("expected error")
	}
	if len(c.flights) != 0 {
		t.Fatal("failed build leaked flight")
	}
	if _, err := c.Build(context.Background(), "tenant", "zh"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("error was cached")
	}
}

func TestTextNormCacheCanBeDisabled(t *testing.T) {
	var calls int
	inner := textNormBuilderFunc(func(context.Context, string, string) (*newtn.Engine, error) {
		calls++
		return newtn.Compile(newtn.Config{})
	})
	for _, cfg := range []struct {
		ttl time.Duration
		max int
	}{{0, 4}, {time.Minute, 0}} {
		c := NewCachedTextNorm(inner, cfg.ttl, cfg.max, nil, nil)
		before := calls
		for i := 0; i < 2; i++ {
			if _, err := c.Build(context.Background(), "tenant", "zh"); err != nil {
				t.Fatal(err)
			}
		}
		if calls != before+2 {
			t.Fatal("disabled cache reused an engine")
		}
	}
}
