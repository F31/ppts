package retention

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
)

type fakeRetentionStore struct {
	tenants        []string
	orphans        []OrphanUpload
	sources        []SourceToDelete
	stale          []StaleReservation
	derived        []DerivedToDelete
	orphanObjs     []OrphanToDelete
	aborted        []string
	deleted        []string
	released       []string
	derivedRecords []string
	orphanCutoffs  []time.Time
}

func (f *fakeRetentionStore) ListTenants(context.Context) ([]string, error) { return f.tenants, nil }
func (f *fakeRetentionStore) PendingUploadsBefore(context.Context, string, time.Time) ([]OrphanUpload, error) {
	return f.orphans, nil
}
func (f *fakeRetentionStore) AbortUpload(_ context.Context, _, uploadID string) error {
	f.aborted = append(f.aborted, uploadID)
	return nil
}
func (f *fakeRetentionStore) SourcesToDelete(context.Context, string, time.Time) ([]SourceToDelete, error) {
	return f.sources, nil
}
func (f *fakeRetentionStore) MarkSourceDeleted(_ context.Context, _, revisionID string) error {
	f.deleted = append(f.deleted, revisionID)
	return nil
}
func (f *fakeRetentionStore) DerivedToDelete(context.Context, string, time.Time) ([]DerivedToDelete, error) {
	return f.derived, nil
}
func (f *fakeRetentionStore) DeleteDerivedRecord(_ context.Context, _, objectKey, _ string) error {
	f.derivedRecords = append(f.derivedRecords, objectKey)
	return nil
}
func (f *fakeRetentionStore) OrphansToDelete(_ context.Context, _ string, cutoff time.Time) ([]OrphanToDelete, error) {
	f.orphanCutoffs = append(f.orphanCutoffs, cutoff)
	return f.orphanObjs, nil
}
func (f *fakeRetentionStore) StaleReservationsBefore(context.Context, string, time.Time) ([]StaleReservation, error) {
	return f.stale, nil
}
func (f *fakeRetentionStore) ReleaseReservationByID(_ context.Context, _, reservationID string) error {
	f.released = append(f.released, reservationID)
	return nil
}

type fakeAuditor struct{ events []audit.Event }

func (f *fakeAuditor) Record(_ context.Context, e audit.Event) error {
	f.events = append(f.events, e)
	return nil
}

func TestSweeperWritesAuditEvents(t *testing.T) {
	key := objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "src", AssetType: "source", AssetID: "a", Ext: "pptx"}
	store := &fakeRetentionStore{
		tenants: []string{"tenant-1"},
		orphans: []OrphanUpload{{UploadID: "upload-1", ObjectKey: key.String()}},
		sources: []SourceToDelete{{RevisionID: "rev-1", ObjectKey: key.String()}},
		stale:   []StaleReservation{{ReservationID: "res-1", LogicalOperationID: "op-1", UsageKind: "gen_seconds"}},
		derived: []DerivedToDelete{{ObjectKey: key.String(), AssetType: "audio", AssetID: "seg-1"}},
	}
	objects := objectstore.NewLocal(t.TempDir(), []byte("s"))
	auditor := &fakeAuditor{}
	sweeper := NewSweeper(store, objects, time.Hour, nil).
		WithQuotaReservationTTL(time.Hour).
		WithAuditor(auditor)

	if err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	actions := map[string]bool{}
	for _, e := range auditor.events {
		actions[e.Action] = true
		if e.TenantID != "tenant-1" || e.ActorUser != "system" {
			t.Fatalf("audit event = %+v", e)
		}
	}
	for _, want := range []string{"source.delete", "upload.abort", "quota.reservation_release", "derived.delete"} {
		if !actions[want] {
			t.Fatalf("missing audit action %q in %+v", want, auditor.events)
		}
	}
	if len(store.derivedRecords) != 1 || store.derivedRecords[0] != key.String() {
		t.Fatalf("derived records deleted = %+v", store.derivedRecords)
	}
}

func orphanKey() objectstore.ObjectKey {
	return objectstore.ObjectKey{TenantID: "tenant-1", ProjectID: "project-1", Revision: "artifact", AssetType: "artifact", AssetID: "hash-1", Ext: "pptx"}
}

func putOrphanObject(t *testing.T, objects objectstore.ObjectStore, key objectstore.ObjectKey) {
	t.Helper()
	if err := objects.Put(context.Background(), key, strings.NewReader("payload"),
		objectstore.ObjectMeta{ContentType: "application/octet-stream"}); err != nil {
		t.Fatalf("put: %v", err)
	}
}

// TestSweeperOrphanScanOffUnlessEnabled 保证关闭时一次查询都不发。
func TestSweeperOrphanScanOffUnlessEnabled(t *testing.T) {
	key := orphanKey()
	store := &fakeRetentionStore{tenants: []string{"tenant-1"}}
	objects := objectstore.NewLocal(t.TempDir(), []byte("s"))
	sweeper := NewSweeper(store, objects, time.Hour, nil)
	if err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(store.orphanCutoffs) != 0 {
		t.Errorf("orphan scan must be off unless enabled, got %d queries", len(store.orphanCutoffs))
	}
	_ = key
}

// TestSweeperOrphanReportOnlyByDefault 是安全底线：删对象不可逆，而"无引用"是启发式判定，
// 判据来自"归属表里查不到引用行"——漏认一种引用关系就会删掉在用对象。故默认只报告。
func TestSweeperOrphanReportOnlyByDefault(t *testing.T) {
	key := orphanKey()
	store := &fakeRetentionStore{
		tenants:    []string{"tenant-1"},
		orphanObjs: []OrphanToDelete{{ObjectKey: key.String(), AssetType: "artifact", AssetID: "hash-1"}},
	}
	objects := objectstore.NewLocal(t.TempDir(), []byte("s"))
	putOrphanObject(t, objects, key)
	auditor := &fakeAuditor{}
	sweeper := NewSweeper(store, objects, time.Hour, nil).
		WithOrphanScan(time.Hour, false).
		WithAuditor(auditor)

	if err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(store.derivedRecords) != 0 {
		t.Errorf("report-only must not delete inventory records, got %+v", store.derivedRecords)
	}
	// Get 返回的 ReadCloser 必须关：Windows 上未关闭的句柄会让 TempDir 清理失败，
	// 表现为与本断言无关的 cleanup 报错。
	if rc, _, err := objects.Get(context.Background(), key); err != nil {
		t.Errorf("report-only must not delete the object: %v", err)
	} else {
		_ = rc.Close()
	}
	// 只报告不等于什么都不做：必须留下可供人工复核的审计痕迹。
	found := false
	for _, e := range auditor.events {
		if e.Action != "orphan.detected" {
			continue
		}
		found = true
		if e.Metadata["deleted"] != false {
			t.Errorf("orphan.detected deleted = %v, want false", e.Metadata["deleted"])
		}
	}
	if !found {
		t.Errorf("no orphan.detected audit event in %+v", auditor.events)
	}
}

func TestSweeperOrphanDeletesWhenEnabled(t *testing.T) {
	key := orphanKey()
	store := &fakeRetentionStore{
		tenants:    []string{"tenant-1"},
		orphanObjs: []OrphanToDelete{{ObjectKey: key.String(), AssetType: "artifact", AssetID: "hash-1"}},
	}
	objects := objectstore.NewLocal(t.TempDir(), []byte("s"))
	putOrphanObject(t, objects, key)
	auditor := &fakeAuditor{}
	sweeper := NewSweeper(store, objects, time.Hour, nil).
		WithOrphanScan(time.Hour, true).
		WithAuditor(auditor)

	if err := sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, _, err := objects.Get(context.Background(), key); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Errorf("object should be gone when deletion enabled, got err=%v", err)
	}
	if len(store.derivedRecords) != 1 || store.derivedRecords[0] != key.String() {
		t.Errorf("inventory record not deleted: %+v", store.derivedRecords)
	}
	found := false
	for _, e := range auditor.events {
		if e.Action != "orphan.delete" {
			continue
		}
		found = true
		if e.Metadata["deleted"] != true {
			t.Errorf("orphan.delete deleted = %v, want true", e.Metadata["deleted"])
		}
	}
	if !found {
		t.Errorf("no orphan.delete audit event in %+v", auditor.events)
	}
}
