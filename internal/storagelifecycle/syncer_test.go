package storagelifecycle

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/tenant"
)

type fakePolicyLister struct {
	settings []tenant.LifecyclePolicySetting
	err      error
}

func (f *fakePolicyLister) ListLifecyclePolicies(context.Context) ([]tenant.LifecyclePolicySetting, error) {
	return f.settings, f.err
}

type fakeLifecycleObjects struct {
	applied []objectstore.LifecyclePolicy
	err     error
}

func (f *fakeLifecycleObjects) Put(context.Context, objectstore.ObjectKey, io.Reader, objectstore.ObjectMeta) error {
	return nil
}
func (f *fakeLifecycleObjects) Get(context.Context, objectstore.ObjectKey) (io.ReadCloser, objectstore.ObjectMeta, error) {
	return nil, objectstore.ObjectMeta{}, objectstore.ErrObjectNotFound
}
func (f *fakeLifecycleObjects) SignedURL(context.Context, objectstore.ObjectKey, objectstore.Operation, time.Duration) (string, error) {
	return "", nil
}
func (f *fakeLifecycleObjects) Delete(context.Context, objectstore.ObjectKey) error { return nil }
func (f *fakeLifecycleObjects) ApplyLifecyclePolicy(_ context.Context, _ string, policy objectstore.LifecyclePolicy) error {
	f.applied = append(f.applied, policy)
	return f.err
}

func TestSyncerAppliesExplicitPolicies(t *testing.T) {
	objects := &fakeLifecycleObjects{}
	s := NewSyncer(&fakePolicyLister{settings: []tenant.LifecyclePolicySetting{
		{TenantID: "tenant-1", Policy: tenant.Policy{StorageTransitionDays: 30, StorageExpirationDays: 365}},
		{TenantID: "tenant-2", Policy: tenant.Policy{SourceRetentionDays: 7}},
	}}, objects, nil)

	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(objects.applied) != 1 {
		t.Fatalf("applied policies = %d want 1", len(objects.applied))
	}
	got := objects.applied[0]
	if len(got.Transitions) != 1 || got.Transitions[0].AfterDays != 30 || got.Expiration == nil || got.Expiration.AfterDays != 365 {
		t.Fatalf("policy = %+v", got)
	}
}

func TestSyncerSkipsUnsupportedBackends(t *testing.T) {
	objects := &fakeLifecycleObjects{err: objectstore.ErrOperationNotSupported}
	s := NewSyncer(&fakePolicyLister{settings: []tenant.LifecyclePolicySetting{
		{TenantID: "tenant-1", Policy: tenant.Policy{StorageTransitionDays: 30}},
	}}, objects, nil)

	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("Sync unsupported: %v", err)
	}
}

func TestSyncerReturnsListerError(t *testing.T) {
	want := errors.New("boom")
	s := NewSyncer(&fakePolicyLister{err: want}, &fakeLifecycleObjects{}, nil)
	if err := s.Sync(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Sync err = %v want %v", err, want)
	}
}
