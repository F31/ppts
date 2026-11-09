package retention

import (
	"context"
	"testing"
	"time"

	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
)

// TestSweeperJobRetentionOffUnlessEnabled 保证关闭时一次删除查询都不发。
// 任务清理与孤儿扫描不同：删的是任务行（可再生产），不是对象，默认开启；
// 但 WithJobRetention 未调用时仍必须零查询——「没开的开关不产生副作用」。
func TestSweeperJobRetentionOffUnlessEnabled(t *testing.T) {
	store := &fakeRetentionStore{tenants: []string{"tenant-1"}}
	objects := objectstore.NewLocal(t.TempDir(), []byte("s"))
	sweeper := NewSweeper(store, objects, time.Hour, nil)
	if err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(store.jobCalls) != 0 {
		t.Fatalf("job retention must be off unless enabled, got %d DeleteExpiredJobs calls", len(store.jobCalls))
	}
}

// TestSweeperDeletesExpiredJobsInBatches 断言批式清空与逐条审计：
// 前两批满批（触发继续）、第三批不满（触发停止），批间以「返回条数 < 批大小」终止。
func TestSweeperDeletesExpiredJobsInBatches(t *testing.T) {
	full := make([]ExpiredJob, jobDeleteBatch)
	for i := range full {
		full[i] = ExpiredJob{JobID: "job", Kind: "export", State: "succeeded"}
	}
	partial := []ExpiredJob{{JobID: "last", Kind: "narration", State: "failed"}}
	store := &fakeRetentionStore{
		tenants:    []string{"tenant-1"},
		jobBatches: [][]ExpiredJob{full, full, partial},
	}
	objects := objectstore.NewLocal(t.TempDir(), []byte("s"))
	auditor := &fakeAuditor{}
	sweeper := NewSweeper(store, objects, time.Hour, nil).
		WithJobRetention(30 * 24 * time.Hour).
		WithAuditor(auditor)

	before := time.Now()
	if err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if len(store.jobCalls) != 3 {
		t.Fatalf("DeleteExpiredJobs calls = %d, want 3 (two full batches + one partial)", len(store.jobCalls))
	}
	for i, call := range store.jobCalls {
		if call.tenantID != "tenant-1" {
			t.Fatalf("call %d tenant = %q, want tenant-1", i, call.tenantID)
		}
		if call.limit != jobDeleteBatch {
			t.Fatalf("call %d limit = %d, want %d", i, call.limit, jobDeleteBatch)
		}
		wantCutoff := before.Add(-30 * 24 * time.Hour)
		if call.cutoff.Before(wantCutoff.Add(-time.Minute)) || call.cutoff.After(before.Add(-29*24*time.Hour)) {
			t.Fatalf("call %d cutoff = %v, want ~%v", i, call.cutoff, wantCutoff)
		}
	}
	// 逐条审计：2×500 + 1 = 1001 条 job.delete，最后一条带着正确的元数据。
	var jobDeletes int
	for _, e := range auditor.events {
		if e.Action != "job.delete" {
			continue
		}
		jobDeletes++
		if e.TenantID != "tenant-1" || e.ActorUser != "system" || e.ResourceType != "job" {
			t.Fatalf("job.delete event = %+v", e)
		}
	}
	if jobDeletes != 2*jobDeleteBatch+1 {
		t.Fatalf("job.delete audit events = %d, want %d", jobDeletes, 2*jobDeleteBatch+1)
	}
}

// TestSweeperJobRetentionCapsBatchesPerSweep 断言单次 Sweep 的批数上限：
// 积压再大也只清 maxJobDeleteBatchesPerSweep 批，剩余留给下一周期。
func TestSweeperJobRetentionCapsBatchesPerSweep(t *testing.T) {
	batches := make([][]ExpiredJob, maxJobDeleteBatchesPerSweep+5)
	full := make([]ExpiredJob, jobDeleteBatch)
	for i := range full {
		full[i] = ExpiredJob{JobID: "job", Kind: "parse", State: "succeeded"}
	}
	for i := range batches {
		batches[i] = full
	}
	store := &fakeRetentionStore{tenants: []string{"tenant-1"}, jobBatches: batches}
	objects := objectstore.NewLocal(t.TempDir(), []byte("s"))
	sweeper := NewSweeper(store, objects, time.Hour, nil).
		WithJobRetention(time.Hour)

	if err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(store.jobCalls) != maxJobDeleteBatchesPerSweep {
		t.Fatalf("DeleteExpiredJobs calls = %d, want capped at %d", len(store.jobCalls), maxJobDeleteBatchesPerSweep)
	}
}

// TestSweeperJobRetentionAuditFailureDoesNotBlock 断言审计失败不阻断删除主流程
// （与其它清理项的约定一致：审计是旁路记录，不是清理的前置条件）。
func TestSweeperJobRetentionAuditFailureDoesNotBlock(t *testing.T) {
	store := &fakeRetentionStore{
		tenants:    []string{"tenant-1"},
		jobBatches: [][]ExpiredJob{{{JobID: "j1", Kind: "export", State: "succeeded"}}},
	}
	objects := objectstore.NewLocal(t.TempDir(), []byte("s"))
	sweeper := NewSweeper(store, objects, time.Hour, nil).
		WithJobRetention(time.Hour).
		WithAuditor(errorAuditor{})

	if err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep must not fail on audit errors: %v", err)
	}
	if len(store.jobCalls) != 1 {
		t.Fatalf("DeleteExpiredJobs calls = %d, want 1", len(store.jobCalls))
	}
}

type errorAuditor struct{}

func (errorAuditor) Record(context.Context, audit.Event) error { return errAuditFailed }

var errAuditFailed = &auditError{}

type auditError struct{}

func (*auditError) Error() string { return "audit unavailable" }
