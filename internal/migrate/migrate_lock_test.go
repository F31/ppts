package migrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// recordingConn 记录执行过的 SQL，用于在没有数据库的情况下验证抢锁的**语句顺序**。
//
// 之所以值得测顺序而不是测能否连词：这里有真实的失败模式 —— 抢锁前必须限时、
// 抢完必须解除限时，两处的任何一方被误改都不会报错，只会让系统在特定时序下
// （另一个副本正在迁移 / 某条迁移特别慢）才坏。
type recordingConn struct {
	stmts []string
	// failOn 包含该 SQL 前缀时返回错误，用于验证失败路径是否会中止后续步骤。
	failOn string
	err    error
}

func (c *recordingConn) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	if c.failOn != "" && strings.HasPrefix(sql, c.failOn) {
		return pgconn.CommandTag{}, c.err
	}
	c.stmts = append(c.stmts, sql)
	return pgconn.CommandTag{}, nil
}

func TestAcquireMigrateLockResetsTimeout(t *testing.T) {
	c := &recordingConn{}
	if err := acquireMigrateLock(context.Background(), c); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if len(c.stmts) != 3 {
		t.Fatalf("want 3 statements, got %d: %v", len(c.stmts), c.stmts)
	}
	// 顺序即语义：限时 → 抢锁 → 解除限时。
	if !strings.Contains(c.stmts[0], "SET statement_timeout") || strings.Contains(c.stmts[0], "'0'") {
		t.Fatalf("step 1 must arm a finite timeout, got %q", c.stmts[0])
	}
	if !strings.Contains(c.stmts[1], "pg_advisory_lock") {
		t.Fatalf("step 2 must acquire the advisory lock, got %q", c.stmts[1])
	}
	if !strings.Contains(c.stmts[2], "'0'") {
		t.Fatalf("step 3 must reset timeout to unlimited, got %q", c.stmts[2])
	}
}

// TestAcquireMigrateLockUsesSessionLock 防止锁被误改成事务级：事务级锁在事务结束时释放，
// 而 Apply 的每个迁移各自成事务，锁会在第一个迁移提交后就没了。
func TestAcquireMigrateLockUsesSessionLock(t *testing.T) {
	c := &recordingConn{}
	_ = acquireMigrateLock(context.Background(), c)
	for _, s := range c.stmts {
		if strings.Contains(s, "pg_advisory_xact_lock") {
			t.Fatalf("must use session-level pg_advisory_lock, got %q", s)
		}
	}
}

// TestAcquireMigrateLockFailsClosed 验证抢不到锁时不会"假装成功"继续跑迁移。
func TestAcquireMigrateLockFailsClosed(t *testing.T) {
	sentinel := errors.New("lock timeout boom")
	c := &recordingConn{failOn: "SELECT pg_advisory_lock", err: sentinel}
	err := acquireMigrateLock(context.Background(), c)
	if err == nil {
		t.Fatal("contended lock must surface an error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error must wrap the cause, got %v", err)
	}
	if !strings.Contains(err.Error(), "another instance") {
		t.Fatalf("error should say why (another instance migrating), got %q", err)
	}
	// 失败即中止：不得继续往下执行后续语句。
	if len(c.stmts) != 1 {
		t.Fatalf("must stop right after the failed statement, got %v", c.stmts)
	}
}
