package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// BackendResolver resolves the storage backend configured for a tenant.
// Empty backend names fall back to the registry default.
type BackendResolver interface {
	ObjectStoreBackend(ctx context.Context, tenantID string) (string, error)
}

// ErrBackendNotFound means a tenant policy references an unregistered backend.
var ErrBackendNotFound = errors.New("objectstore: backend not found")

// Registry routes object operations to a backend based on the tenant prefix in ObjectKey.
type Registry struct {
	defaultBackend string
	stores         map[string]ObjectStore
	resolver       BackendResolver
}

// NewRegistry creates a tenant-aware object store router.
func NewRegistry(defaultBackend string, stores map[string]ObjectStore, resolver BackendResolver) (*Registry, error) {
	if defaultBackend == "" {
		return nil, errors.New("objectstore: default backend is required")
	}
	if len(stores) == 0 || stores[defaultBackend] == nil {
		return nil, fmt.Errorf("%w: default backend %q", ErrBackendNotFound, defaultBackend)
	}
	copyStores := make(map[string]ObjectStore, len(stores))
	for name, store := range stores {
		if name == "" || store == nil {
			return nil, errors.New("objectstore: backend name and store are required")
		}
		copyStores[name] = store
	}
	return &Registry{defaultBackend: defaultBackend, stores: copyStores, resolver: resolver}, nil
}

func (r *Registry) storeFor(ctx context.Context, tenantID string) (ObjectStore, error) {
	backend := r.defaultBackend
	if r.resolver != nil {
		resolved, err := r.resolver.ObjectStoreBackend(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		if resolved != "" {
			backend = resolved
		}
	}
	store := r.stores[backend]
	if store == nil {
		return nil, fmt.Errorf("%w: %s", ErrBackendNotFound, backend)
	}
	return store, nil
}

func (r *Registry) Put(ctx context.Context, key ObjectKey, reader io.Reader, meta ObjectMeta) error {
	if err := key.Validate(); err != nil {
		return err
	}
	store, err := r.storeFor(ctx, key.TenantID)
	if err != nil {
		return err
	}
	return store.Put(ctx, key, reader, meta)
}

func (r *Registry) Get(ctx context.Context, key ObjectKey) (io.ReadCloser, ObjectMeta, error) {
	if err := key.Validate(); err != nil {
		return nil, ObjectMeta{}, err
	}
	store, err := r.storeFor(ctx, key.TenantID)
	if err != nil {
		return nil, ObjectMeta{}, err
	}
	return store.Get(ctx, key)
}

func (r *Registry) SignedURL(ctx context.Context, key ObjectKey, op Operation, ttl time.Duration) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	store, err := r.storeFor(ctx, key.TenantID)
	if err != nil {
		return "", err
	}
	return store.SignedURL(ctx, key, op, ttl)
}

func (r *Registry) Delete(ctx context.Context, key ObjectKey) error {
	if err := key.Validate(); err != nil {
		return err
	}
	store, err := r.storeFor(ctx, key.TenantID)
	if err != nil {
		return err
	}
	return store.Delete(ctx, key)
}

// ApplyLifecyclePolicy applies a lifecycle policy to all registered backends.
func (r *Registry) ApplyLifecyclePolicy(ctx context.Context, bucket string, policy LifecyclePolicy) error {
	for _, store := range r.stores {
		if err := store.ApplyLifecyclePolicy(ctx, bucket, policy); err != nil && !errors.Is(err, ErrOperationNotSupported) {
			return err
		}
	}
	return nil
}

// ApplyTenantLifecyclePolicy applies lifecycle policy only to the backend selected for tenantID.
func (r *Registry) ApplyTenantLifecyclePolicy(ctx context.Context, tenantID, bucket string, policy LifecyclePolicy) error {
	store, err := r.storeFor(ctx, tenantID)
	if err != nil {
		return err
	}
	if err := store.ApplyLifecyclePolicy(ctx, bucket, policy); err != nil && !errors.Is(err, ErrOperationNotSupported) {
		return err
	}
	return nil
}

// ParseSignedURL delegates local signed URL parsing to registered backends that support it.
func (r *Registry) ParseSignedURL(raw string) (ObjectKey, Operation, error) {
	for _, store := range r.stores {
		parser, ok := store.(interface {
			ParseSignedURL(string) (ObjectKey, Operation, error)
		})
		if !ok {
			continue
		}
		key, op, err := parser.ParseSignedURL(raw)
		if err == nil {
			return key, op, nil
		}
	}
	return ObjectKey{}, "", ErrSignatureInvalid
}
