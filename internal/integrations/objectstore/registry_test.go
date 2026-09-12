package objectstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type testBackendResolver map[string]string

func (r testBackendResolver) ObjectStoreBackend(_ context.Context, tenantID string) (string, error) {
	return r[tenantID], nil
}

type memoryStore struct {
	objects map[string][]byte
}

func newMemoryStore() *memoryStore { return &memoryStore{objects: map[string][]byte{}} }

func (s *memoryStore) Put(_ context.Context, key ObjectKey, r io.Reader, _ ObjectMeta) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.objects[key.String()] = data
	return nil
}

func (s *memoryStore) Get(_ context.Context, key ObjectKey) (io.ReadCloser, ObjectMeta, error) {
	data, ok := s.objects[key.String()]
	if !ok {
		return nil, ObjectMeta{}, ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), ObjectMeta{Size: int64(len(data))}, nil
}

func (s *memoryStore) SignedURL(context.Context, ObjectKey, Operation, time.Duration) (string, error) {
	return "", ErrOperationNotSupported
}

func (s *memoryStore) Delete(_ context.Context, key ObjectKey) error {
	delete(s.objects, key.String())
	return nil
}

func (s *memoryStore) ApplyLifecyclePolicy(context.Context, string, LifecyclePolicy) error {
	return ErrOperationNotSupported
}

func TestRegistryRoutesByTenantBackend(t *testing.T) {
	localStore := newMemoryStore()
	s3Store := newMemoryStore()
	registry, err := NewRegistry("local", map[string]ObjectStore{"local": localStore, "s3": s3Store}, testBackendResolver{"tenant-s3": "s3"})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	localKey := ObjectKey{TenantID: "tenant-local", ProjectID: "p", Revision: "r", AssetType: "source", AssetID: "a", Ext: "pptx"}
	s3Key := ObjectKey{TenantID: "tenant-s3", ProjectID: "p", Revision: "r", AssetType: "source", AssetID: "a", Ext: "pptx"}
	if err := registry.Put(context.Background(), localKey, bytes.NewReader([]byte("local")), ObjectMeta{}); err != nil {
		t.Fatalf("Put local: %v", err)
	}
	if err := registry.Put(context.Background(), s3Key, bytes.NewReader([]byte("s3")), ObjectMeta{}); err != nil {
		t.Fatalf("Put s3: %v", err)
	}
	if _, ok := localStore.objects[localKey.String()]; !ok {
		t.Fatalf("local tenant object not routed to default backend")
	}
	if _, ok := s3Store.objects[s3Key.String()]; !ok {
		t.Fatalf("s3 tenant object not routed to configured backend")
	}
	if _, ok := localStore.objects[s3Key.String()]; ok {
		t.Fatalf("s3 tenant object leaked to default backend")
	}
}

func TestRegistryRejectsUnknownBackend(t *testing.T) {
	registry, err := NewRegistry("local", map[string]ObjectStore{"local": newMemoryStore()}, testBackendResolver{"tenant-1": "missing"})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	key := ObjectKey{TenantID: "tenant-1", ProjectID: "p", Revision: "r", AssetType: "source", AssetID: "a"}
	if err := registry.Put(context.Background(), key, bytes.NewReader([]byte("x")), ObjectMeta{}); !errors.Is(err, ErrBackendNotFound) {
		t.Fatalf("Put unknown backend = %v, want ErrBackendNotFound", err)
	}
}
