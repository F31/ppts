package db

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestSQLiteConcurrentTxUpgradeDoesNotFail 守 _txlock=immediate。
//
// 形状来自真机故障：worker 处理 script_draft 时在**同一个事务**里先读讲稿、再删写
// 分段；与此同时 job 心跳在另一个连接上写租约。deferred 事务在这两步之间被写者
// 抢先，SQLite 直接返回 SQLITE_BUSY_SNAPSHOT —— 这是立即失败，busy_timeout 不生效。
// 现象是任务以 "script_draft: write draft: database is locked (5)" 落地。
//
// 用例刻意在 SELECT 与 UPDATE 之间留一段窗口，并把并发压到 16：deferred 下几乎必然
// 撞上（去掉 _txlock=immediate 时本用例会红，见提交说明的变异验证）。
func TestSQLiteConcurrentTxUpgradeDoesNotFail(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sqldb, err := OpenSQLite(ctx, filepath.Join(t.TempDir(), "tx.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sqldb.Close()

	if _, err := sqldb.ExecContext(ctx, `CREATE TABLE counter (id INTEGER PRIMARY KEY, v INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sqldb.ExecContext(ctx, `INSERT INTO counter (id, v) VALUES (1, 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const writers = 16
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := sqldb.BeginTx(ctx, nil)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = tx.Rollback() }()

			var v int
			if err := tx.QueryRowContext(ctx, `SELECT v FROM counter WHERE id = 1`).Scan(&v); err != nil {
				errs <- err
				return
			}
			// 窗口：让其它写者有机会在本次事务的读快照与写之间提交。
			time.Sleep(5 * time.Millisecond)
			if _, err := tx.ExecContext(ctx, `UPDATE counter SET v = ? WHERE id = 1`, v+1); err != nil {
				errs <- err
				return
			}
			if err := tx.Commit(); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("并发写事务失败（_txlock=immediate 缺失时即如此）：%v", err)
	}

	var got int
	if err := sqldb.QueryRowContext(ctx, `SELECT v FROM counter WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != writers {
		t.Fatalf("计数器 = %d，期望 %d（有事务被回滚）", got, writers)
	}
}
