//go:build pg

package narration

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

// countingTracer 记录每条 SQL 的关键字（用于数「这次调用打了几次库」）。
// 只统计碰 narration_* 的语句：BEGIN / set_config('app.tenant_id') 属于连接与租户
// 上下文，不是数据访问，混进来会让计数随 tenant.Run 的实现细节漂移。
type countingTracer struct {
	mu    sync.Mutex
	stmts []string
}

func (c *countingTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.Contains(data.SQL, "narration_") {
		c.stmts = append(c.stmts, firstLine(data.SQL))
	}
	return ctx
}

func (c *countingTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *countingTracer) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stmts = nil
}

func (c *countingTracer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.stmts)
}

func (c *countingTracer) dump() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.stmts, " | ")
}

func firstLine(sql string) string {
	if i := strings.IndexByte(sql, '\n'); i >= 0 {
		sql = sql[:i]
	}
	return strings.Join(strings.Fields(sql), " ")
}

// tracedStore 建一个带 tracer 的 PGStore（复用 nrStore 的建库/清库/播种租户逻辑）。
func tracedStore(tb testing.TB) (*PGStore, *countingTracer) {
	tb.Helper()
	dsn := os.Getenv("PPTS_TEST_DATABASE")
	if dsn == "" {
		tb.Skip("PPTS_TEST_DATABASE not set")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		tb.Fatalf("parse dsn: %v", err)
	}
	tracer := &countingTracer{}
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		tb.Fatalf("pool: %v", err)
	}
	tb.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx,
		"TRUNCATE narration_segments, narration_scripts, jobs, job_steps, source_revisions, projects, tenants CASCADE"); err != nil {
		tb.Fatalf("truncate: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO tenants(id,name) VALUES ($1,$2) ON CONFLICT DO NOTHING", nrTenant, "nr"); err != nil {
		tb.Fatalf("seed tenant: %v", err)
	}
	if err := tenant.Run(ctx, pool, nrTenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			"INSERT INTO projects(id,tenant_id,owner_user,title) VALUES ($1,$2,$3,$3) ON CONFLICT DO NOTHING",
			nrProject, nrTenant, "tester")
		return err
	}); err != nil {
		tb.Fatalf("seed project: %v", err)
	}
	return NewPGStore(pool), tracer
}

// ListByProject 的查询数必须与页数无关：分段批量加载（一次 IN 查询）而不是逐页查。
//
// 这条用例就是 N+1 的门禁——它不看时间（时间会被机器与缓存影响），只看「打了几次库」。
// 3 页和 12 页的语句数必须相同；改回逐页 loadSegmentsTx 会立刻变成 3 vs 12。
func TestListByProjectQueryCountIsConstant(t *testing.T) {
	s, tracer := tracedStore(t)
	ctx := context.Background()

	seedNarrationSlides(t, s, nrTenant, nrProject, 5, 3)
	tracer.reset()
	if _, err := s.ListByProject(ctx, nrTenant, nrProject, 5, lang); err != nil {
		t.Fatalf("ListByProject(3): %v", err)
	}
	small := tracer.count()
	smallDump := tracer.dump()

	seedNarrationSlidesRange(t, s, nrTenant, nrProject, 5, 3, 12)
	tracer.reset()
	list, err := s.ListByProject(ctx, nrTenant, nrProject, 5, lang)
	if err != nil {
		t.Fatalf("ListByProject(12): %v", err)
	}
	large := tracer.count()

	if len(list) != 12 {
		t.Fatalf("list len = %d, want 12", len(list))
	}
	for _, r := range list {
		if len(r.Segments) != 2 {
			t.Fatalf("slide %s segments = %d, want 2", r.SlideID, len(r.Segments))
		}
		if r.Segments[0].SegmentID != "seg-01" || r.Segments[1].SegmentID != "seg-02" {
			t.Fatalf("slide %s segment order = %s,%s", r.SlideID, r.Segments[0].SegmentID, r.Segments[1].SegmentID)
		}
	}
	if small != large {
		t.Fatalf("查询数随页数增长：3 页 %d 条、12 页 %d 条（3 页时：%s）", small, large, smallDump)
	}
	if large > 3 {
		t.Fatalf("ListByProject 打了 %d 次库，期望 ≤3（脚本一次 + 分段一次 + 余量）", large)
	}
}

// BenchmarkListByProjectPG30：PG 上一次 30 页列表读的成本（逐页查分段时是 31 次往返）。
func BenchmarkListByProjectPG30(b *testing.B) {
	ctx := context.Background()
	s, _ := tracedStore(b)
	seedNarrationSlides(b, s, nrTenant, nrProject, 5, 30)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		list, err := s.ListByProject(ctx, nrTenant, nrProject, 5, lang)
		if err != nil {
			b.Fatal(err)
		}
		if len(list) != 30 {
			b.Fatalf("list len = %d", len(list))
		}
	}
}
