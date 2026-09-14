package retention

import (
	"context"
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
	aborted        []string
	deleted        []string
	released       []string
	derivedRecords []string
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
