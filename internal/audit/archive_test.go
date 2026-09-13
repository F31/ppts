package audit

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"testing"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

type fakeArchiveStore struct {
	events  map[string][]Event
	deletes map[string]int64
}

func newFakeArchiveStore() *fakeArchiveStore {
	return &fakeArchiveStore{events: map[string][]Event{}, deletes: map[string]int64{}}
}

func (s *fakeArchiveStore) Record(_ context.Context, e Event) error {
	s.events[e.TenantID] = append(s.events[e.TenantID], e)
	return nil
}

func (s *fakeArchiveStore) List(_ context.Context, tenantID string, filter Filter) ([]Event, error) {
	var all, out []Event
	all = append(all, s.events[tenantID]...)
	if !filter.Before.IsZero() {
		for _, e := range all {
			if e.CreatedAt.Before(filter.Before) {
				out = append(out, e)
			}
		}
	} else {
		out = all
	}
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (s *fakeArchiveStore) DeleteBefore(_ context.Context, tenantID string, before time.Time) (int64, error) {
	var kept []Event
	for _, e := range s.events[tenantID] {
		if !e.CreatedAt.Before(before) {
			kept = append(kept, e)
		}
	}
	deleted := int64(len(s.events[tenantID]) - len(kept))
	s.events[tenantID] = kept
	s.deletes[tenantID] += deleted
	return deleted, nil
}

type fakeTenantLister struct{ ids []string }

func (f *fakeTenantLister) ListTenants(context.Context) ([]string, error) { return f.ids, nil }

type fakeArchiveObjects struct{ puts int }

func (f *fakeArchiveObjects) Put(context.Context, objectstore.ObjectKey, io.Reader, objectstore.ObjectMeta) error {
	f.puts++
	return nil
}
func (f *fakeArchiveObjects) Get(context.Context, objectstore.ObjectKey) (io.ReadCloser, objectstore.ObjectMeta, error) {
	return nil, objectstore.ObjectMeta{}, errors.New("not used")
}
func (f *fakeArchiveObjects) SignedURL(context.Context, objectstore.ObjectKey, objectstore.Operation, time.Duration) (string, error) {
	return "", errors.New("not used")
}
func (f *fakeArchiveObjects) Delete(context.Context, objectstore.ObjectKey) error { return nil }
func (f *fakeArchiveObjects) ApplyLifecyclePolicy(context.Context, string, objectstore.LifecyclePolicy) error {
	return errors.New("not used")
}

func TestArchiverArchivesOldAndKeepsFresh(t *testing.T) {
	now := time.Now()
	oldA := Event{TenantID: "t1", Action: "old", CreatedAt: now.Add(-48 * time.Hour)}
	oldB := Event{TenantID: "t1", Action: "old2", CreatedAt: now.Add(-40 * time.Hour)}
	fresh := Event{TenantID: "t1", Action: "fresh", CreatedAt: now.Add(-time.Hour)}
	other := Event{TenantID: "t2", Action: "other-old", CreatedAt: now.Add(-72 * time.Hour)}

	store := newFakeArchiveStore()
	for _, e := range []Event{oldA, oldB, fresh, other} {
		store.events[e.TenantID] = append(store.events[e.TenantID], e)
	}
	objects := &fakeArchiveObjects{}
	a := NewArchiver(store, &fakeTenantLister{ids: []string{"t1", "t2"}}, objects, log.New(os.Stderr, "", 0))

	cutoff := now.Add(-24 * time.Hour)
	if err := a.ArchiveBefore(context.Background(), cutoff); err != nil {
		t.Fatalf("ArchiveBefore: %v", err)
	}
	if objects.puts != 2 {
		t.Fatalf("archive objects written = %d want 2", objects.puts)
	}
	if store.deletes["t1"] != 2 || store.deletes["t2"] != 1 {
		t.Fatalf("deletes = %+v want t1:2 t2:1", store.deletes)
	}
	if len(store.events["t1"]) != 1 || store.events["t1"][0].Action != "fresh" {
		t.Fatalf("t1 remaining = %+v want only fresh", store.events["t1"])
	}
	if len(store.events["t2"]) != 0 {
		t.Fatalf("t2 remaining = %+v want empty", store.events["t2"])
	}
}

func TestArchiverNoOldEventsWritesNothing(t *testing.T) {
	store := newFakeArchiveStore()
	store.events["t1"] = []Event{{TenantID: "t1", Action: "fresh", CreatedAt: time.Now()}}
	objects := &fakeArchiveObjects{}
	a := NewArchiver(store, &fakeTenantLister{ids: []string{"t1"}}, objects, nil)

	if err := a.ArchiveBefore(context.Background(), time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("ArchiveBefore: %v", err)
	}
	if objects.puts != 0 || store.deletes["t1"] != 0 {
		t.Fatalf("wrote %d deletes=%+v want no-op", objects.puts, store.deletes)
	}
}
