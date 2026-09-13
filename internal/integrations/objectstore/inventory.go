package objectstore

import (
	"context"
	"io"
	"time"
)

// InventoryRecorder persists object metadata for storage usage accounting.
type InventoryRecorder interface {
	RecordObject(ctx context.Context, key ObjectKey, meta ObjectMeta) error
	DeleteObjectRecord(ctx context.Context, key ObjectKey) error
}

// WithInventory wraps store with metadata recording. Object operations remain delegated to store;
// inventory is updated only after the backing operation succeeds.
func WithInventory(store ObjectStore, recorder InventoryRecorder) ObjectStore {
	if recorder == nil {
		return store
	}
	return &inventoryStore{store: store, recorder: recorder}
}

type inventoryStore struct {
	store    ObjectStore
	recorder InventoryRecorder
}

func (s *inventoryStore) Put(ctx context.Context, key ObjectKey, r io.Reader, meta ObjectMeta) error {
	if err := s.store.Put(ctx, key, r, meta); err != nil {
		return err
	}
	return s.recorder.RecordObject(ctx, key, meta)
}

func (s *inventoryStore) Get(ctx context.Context, key ObjectKey) (io.ReadCloser, ObjectMeta, error) {
	return s.store.Get(ctx, key)
}

func (s *inventoryStore) SignedURL(ctx context.Context, key ObjectKey, op Operation, ttl time.Duration) (string, error) {
	return s.store.SignedURL(ctx, key, op, ttl)
}

func (s *inventoryStore) Delete(ctx context.Context, key ObjectKey) error {
	if err := s.store.Delete(ctx, key); err != nil {
		return err
	}
	return s.recorder.DeleteObjectRecord(ctx, key)
}

func (s *inventoryStore) ApplyLifecyclePolicy(ctx context.Context, bucket string, policy LifecyclePolicy) error {
	return s.store.ApplyLifecyclePolicy(ctx, bucket, policy)
}

func (s *inventoryStore) ParseSignedURL(raw string) (ObjectKey, Operation, error) {
	parser, ok := s.store.(interface {
		ParseSignedURL(string) (ObjectKey, Operation, error)
	})
	if !ok {
		return ObjectKey{}, "", ErrSignatureInvalid
	}
	return parser.ParseSignedURL(raw)
}

func (s *inventoryStore) ApplyTenantLifecyclePolicy(ctx context.Context, tenantID, bucket string, policy LifecyclePolicy) error {
	applier, ok := s.store.(interface {
		ApplyTenantLifecyclePolicy(context.Context, string, string, LifecyclePolicy) error
	})
	if !ok {
		return s.store.ApplyLifecyclePolicy(ctx, bucket, policy)
	}
	return applier.ApplyTenantLifecyclePolicy(ctx, tenantID, bucket, policy)
}
