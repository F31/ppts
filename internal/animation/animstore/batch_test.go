package animstore

import (
	"context"
	"errors"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/db"
)

func batchKey(rev *animation.Revision) animation.Key { return rev.Key() }

func TestSQLiteApplyBatchWritesAllOrNothing(t *testing.T) {
	ctx := context.Background()
	s := newAnimationStore(t)
	tenant, project := db.LocalTenantID, "p-batch"

	revs := []*animation.Revision{
		sampleRevision(tenant, project, "s1", 1),
		sampleRevision(tenant, project, "s2", 1),
		sampleRevision(tenant, project, "s3", 1),
	}
	ops := make([]BatchOp, 0, len(revs))
	for _, rev := range revs {
		ops = append(ops, BatchOp{Key: batchKey(rev), Rev: rev, Expected: 0})
	}
	saved, err := s.ApplyBatch(ctx, ops)
	if err != nil {
		t.Fatalf("ApplyBatch: %v", err)
	}
	if len(saved) != 3 {
		t.Fatalf("saved len = %d, want 3", len(saved))
	}
	for i, rev := range revs {
		got, err := s.Get(ctx, batchKey(rev))
		if err != nil {
			t.Fatalf("Get %s: %v", rev.SlideID, err)
		}
		if got.Revision != saved[i].Revision || got.Revision != 1 {
			t.Fatalf("%s revision = %d (saved %d), want 1", rev.SlideID, got.Revision, saved[i].Revision)
		}
	}
}

// 中途冲突必须整批回滚：第 1 页不能因为"排在前面"就留下一行。
// 这一条守的正是批量套用的产品承诺——没有它，界面会说"已回滚"，库里却多出一页新动效。
func TestSQLiteApplyBatchRollsBackOnMidBatchConflict(t *testing.T) {
	ctx := context.Background()
	s := newAnimationStore(t)
	tenant, project := db.LocalTenantID, "p-batch-rollback"

	existing := sampleRevision(tenant, project, "s2", 1)
	if _, err := s.Put(ctx, existing, 0); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	fresh := sampleRevision(tenant, project, "s1", 1)
	stale := sampleRevision(tenant, project, "s2", 1)

	ops := []BatchOp{
		{Key: batchKey(fresh), Rev: fresh, Expected: 0},
		{Key: batchKey(stale), Rev: stale, Expected: 999}, // 与当前 revision=1 冲突
	}
	if _, err := s.ApplyBatch(ctx, ops); err == nil {
		t.Fatal("ApplyBatch 应当因冲突失败")
	}
	if _, err := s.Get(ctx, batchKey(fresh)); !errors.Is(err, animation.ErrNotFound) {
		t.Fatalf("第 1 页不应留下写入，Get err = %v", err)
	}
	got, err := s.Get(ctx, batchKey(existing))
	if err != nil {
		t.Fatalf("Get existing: %v", err)
	}
	if got.Revision != 1 {
		t.Fatalf("已有行的 revision 被改成了 %d，事务没有回滚", got.Revision)
	}
}

// 撤销"套用前没有规格"的页必须真的删行，而不是写一份空草稿。
func TestSQLiteApplyBatchDeleteRestoresAbsentPage(t *testing.T) {
	ctx := context.Background()
	s := newAnimationStore(t)
	tenant, project := db.LocalTenantID, "p-batch-undo"

	rev := sampleRevision(tenant, project, "s1", 1)
	saved, err := s.Put(ctx, rev, 0)
	if err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	if _, err := s.ApplyBatch(ctx, []BatchOp{{Key: batchKey(rev), Rev: nil, Expected: 999}}); err == nil {
		t.Fatal("过期 revision 的删除应当冲突")
	}
	if _, err := s.Get(ctx, batchKey(rev)); err != nil {
		t.Fatalf("冲突后行应仍在，Get err = %v", err)
	}
	if _, err := s.ApplyBatch(ctx, []BatchOp{{Key: batchKey(rev), Rev: nil, Expected: saved.Revision}}); err != nil {
		t.Fatalf("ApplyBatch delete: %v", err)
	}
	if _, err := s.Get(ctx, batchKey(rev)); !errors.Is(err, animation.ErrNotFound) {
		t.Fatalf("删除后应读不到，Get err = %v", err)
	}
}
