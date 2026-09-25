package pipeline

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/F31/ppts/internal/db"
)

// 本文件守护 columns.go 建立的不变量：**列清单顺序 == 两侧 Scan 目标顺序**。
//
// 为什么需要它：这类错位编译期不可见，运行期表现为「类型不匹配」或更糟的
// 「值被安静地读进另一个字段」。双存储包为此付过学费（internal/artifact 曾因 SQLite 侧
// 错位导致整个 profile 硬失败）。下方的哨兵往返测试刻意让 17 列取各不相同的值，
// 任何换位都会在断言里体现出来——而不是在某处表现为一个"看起来合理"的错误数字。

// TestJobColumnsSingleSource 断言列清单文本与两侧 dest 都由 jobColumns 表生成。
func TestJobColumnsSingleSource(t *testing.T) {
	if len(jobColumns) == 0 {
		t.Fatal("jobColumns 为空")
	}
	if got := jobColumnList(jobColumns); got != jobSelectColumns {
		t.Fatalf("jobSelectColumns 与 jobColumns 不一致：\n got: %s\nwant: %s", jobSelectColumns, got)
	}
	// 列清单里不得有重复列（重复列会让 Scan 把后者的值覆盖前者，且通常不报错）。
	seen := map[string]int{}
	for i, c := range jobColumns {
		if prev, ok := seen[c.name]; ok {
			t.Fatalf("jobColumns[%d] 重复列 %q（另见 [%d]）", i, c.name, prev)
		}
		seen[c.name] = i
	}
}

// TestJobDestLengthMatchesColumns 断言两侧 dest 与列清单等长。
//
// 这是"漏加列"的最小防线：加了 SQL 列却忘了给某一侧 dest 补目标（或反过来），
// 长度立刻不等。它挡不住"顺序对调"，那类错误由下面的哨兵往返守。
func TestJobDestLengthMatchesColumns(t *testing.T) {
	pg := pgJobDest(jobColumns, &jobScanBuf{})
	sq := sqJobDest(jobColumns, &sqJobBuf{})
	if len(pg) != len(jobColumns) {
		t.Fatalf("PG dest 长度 = %d want %d", len(pg), len(jobColumns))
	}
	if len(sq) != len(jobColumns) {
		t.Fatalf("SQLite dest 长度 = %d want %d", len(sq), len(jobColumns))
	}
	// 每个目标都必须是非 nil 指针：nil 目标在 pgx/database-sql 下会在运行时才炸。
	for i, d := range pg {
		if d == nil || reflect.ValueOf(d).Kind() != reflect.Ptr {
			t.Fatalf("PG dest[%d] = %#v，必须是非 nil 指针", i, d)
		}
	}
	for i, d := range sq {
		if d == nil || reflect.ValueOf(d).Kind() != reflect.Ptr {
			t.Fatalf("SQLite dest[%d] = %#v，必须是非 nil 指针", i, d)
		}
	}
}

// TestSQLiteJobColumnRoundTrip 用裸 SQL 写入 17 列各不相同的哨兵值，再经 SQLiteStore 读回。
//
// 值全部互不相同是刻意的：一旦某列读到邻居的值，断言会精确指出哪一列，
// 而不是给出一个仍然"像真的"的错误数字。
func TestSQLiteJobColumnRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newPipelineStore(t)

	const (
		id             = "job-sentinel-1"
		tenantID       = "tenant-sentinel"
		projectID      = "project-sentinel"
		kind           = "render"
		state          = "queued"
		inputSnapshot  = `{"input":"snapshot-sentinel"}`
		idempotencyKey = "idem-sentinel"
		leaseOwner     = "lease-owner-sentinel"
		lastErrorJSON  = `{"code":"sentinel","message":"last-error-sentinel"}`
		traceparent    = "traceparent-sentinel"
	)
	// SQLite 时间列只存到毫秒（internal/db.TimeFormat），哨兵因此取毫秒级且彼此不同。
	// 刻意**不**用纳秒：那会让精度上限在这里伪装成"列错位"，把真正要抓的错误淹掉。
	var (
		leaseUntil = time.Date(2031, 2, 3, 4, 5, 6, 111000000, time.UTC)
		runAt      = time.Date(2032, 3, 4, 5, 6, 7, 222000000, time.UTC)
		createdAt  = time.Date(2033, 4, 5, 6, 7, 8, 333000000, time.UTC)
		updatedAt  = time.Date(2034, 5, 6, 7, 8, 9, 444000000, time.UTC)
	)

	if _, err := s.db.ExecContext(ctx, `INSERT INTO jobs
		(id, tenant_id, project_id, kind, state, input_snapshot, idempotency_key,
		 attempt, lease_owner, lease_until, fencing_token, run_at, progress,
		 last_error, created_at, updated_at, traceparent, phase, affected_pages)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, tenantID, projectID, kind, state, inputSnapshot, idempotencyKey,
		7, leaseOwner, db.FormatTime(leaseUntil), 13, db.FormatTime(runAt), 42,
		lastErrorJSON, db.FormatTime(createdAt), db.FormatTime(updatedAt), traceparent,
		"phase-sentinel", `["p1","p2"]`); err != nil {
		t.Fatalf("insert sentinel: %v", err)
	}

	got, err := s.Get(ctx, id, tenantID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// 逐列断言：顺序错位会在这里暴露为"某列读到了别人的值"。
	if got.ID != id {
		t.Errorf("ID = %q want %q", got.ID, id)
	}
	if got.TenantID != tenantID {
		t.Errorf("TenantID = %q want %q", got.TenantID, tenantID)
	}
	if got.ProjectID != projectID {
		t.Errorf("ProjectID = %q want %q", got.ProjectID, projectID)
	}
	if got.Kind != JobKind(kind) {
		t.Errorf("Kind = %q want %q", got.Kind, kind)
	}
	if got.State != StateQueued {
		t.Errorf("State = %q want %q", got.State, state)
	}
	if got.InputSnapshot != inputSnapshot {
		t.Errorf("InputSnapshot = %q want %q", got.InputSnapshot, inputSnapshot)
	}
	if got.IDempotencyKey != idempotencyKey {
		t.Errorf("IDempotencyKey = %q want %q", got.IDempotencyKey, idempotencyKey)
	}
	if got.Attempt != 7 {
		t.Errorf("Attempt = %d want 7", got.Attempt)
	}
	if got.LeaseOwner != leaseOwner {
		t.Errorf("LeaseOwner = %q want %q", got.LeaseOwner, leaseOwner)
	}
	assertSameTime(t, "LeaseUntil", got.LeaseUntil, leaseUntil)
	if got.FencingToken != 13 {
		t.Errorf("FencingToken = %d want 13", got.FencingToken)
	}
	assertSameTime(t, "RunAt", got.RunAt, runAt)
	if got.Progress != 42 {
		t.Errorf("Progress = %d want 42", got.Progress)
	}
	if got.LastError == nil || got.LastError.Message != "last-error-sentinel" {
		t.Errorf("LastError = %+v want message=last-error-sentinel", got.LastError)
	}
	assertSameTime(t, "CreatedAt", got.CreatedAt, createdAt)
	assertSameTime(t, "UpdatedAt", got.UpdatedAt, updatedAt)
	if got.TraceParent != traceparent {
		t.Errorf("TraceParent = %q want %q", got.TraceParent, traceparent)
	}
	if t.Failed() {
		t.FailNow()
	}

	// 列表页投影：在核心列之后追加 phase 与受影响页数，追加位置错同样会 Scan 失败或读错。
	rows, _, err := s.ListPage(ctx, tenantID, JobFilter{Sort: "pages"}, "", 10)
	if err != nil {
		t.Fatalf("list page: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("list page rows = %d want 1", len(rows))
	}
	if rows[0].Phase != "phase-sentinel" {
		t.Errorf("Phase = %q want phase-sentinel", rows[0].Phase)
	}
	if rows[0].PageCount != 2 {
		t.Errorf("PageCount = %d want 2", rows[0].PageCount)
	}
	if rows[0].Job == nil || rows[0].Job.ID != id {
		t.Errorf("ListPage 未回带正确的核心投影：%+v", rows[0].Job)
	}
}

// assertSameTime 按 SQLite 时间列的存储精度（internal/db.TimeFormat，毫秒 UTC）比对，
// 避免把存储精度上限误报成列错位。
func assertSameTime(t *testing.T, field string, got, want time.Time) {
	t.Helper()
	if got.Format(db.TimeFormat) != want.Format(db.TimeFormat) {
		t.Errorf("%s = %s want %s", field, got.Format(db.TimeFormat), want.Format(db.TimeFormat))
	}
}
