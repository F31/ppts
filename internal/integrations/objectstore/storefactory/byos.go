package storefactory

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/objectstore/s3"
	"github.com/F31/ppts/internal/tenant"
)

const byosPrefix = "byos:"

// BYOSCredentialReader loads decrypted customer-owned storage credentials.
type BYOSCredentialReader interface {
	GetBYOSCredential(ctx context.Context, tenantID, credentialID string, cipher tenant.CredentialCipher) (*tenant.BYOSCredential, error)
}

// BYOSResolver lazily constructs object stores for tenant policies such as storage_backend=byos:main.
type BYOSResolver struct {
	creds  BYOSCredentialReader
	cipher tenant.CredentialCipher
	mu     sync.Mutex
	cache  map[string]objectstore.ObjectStore
}

// NewBYOSResolver creates a dynamic BYOS resolver.
func NewBYOSResolver(creds BYOSCredentialReader, cipher tenant.CredentialCipher) *BYOSResolver {
	return &BYOSResolver{creds: creds, cipher: cipher, cache: map[string]objectstore.ObjectStore{}}
}

func (r *BYOSResolver) ObjectStoreForBackend(ctx context.Context, tenantID, backend string) (objectstore.ObjectStore, error) {
	credentialID, ok := strings.CutPrefix(backend, byosPrefix)
	if !ok || credentialID == "" {
		return nil, nil
	}
	if r.creds == nil || r.cipher == nil {
		return nil, fmt.Errorf("%w: %s", objectstore.ErrBackendNotFound, backend)
	}
	cacheKey := tenantID + ":" + credentialID
	r.mu.Lock()
	store := r.cache[cacheKey]
	r.mu.Unlock()
	if store != nil {
		return store, nil
	}
	cred, err := r.creds.GetBYOSCredential(ctx, tenantID, credentialID, r.cipher)
	if err != nil {
		return nil, err
	}
	if cred.Backend != "s3" {
		return nil, fmt.Errorf("%w: unsupported BYOS backend %q", objectstore.ErrBackendNotFound, cred.Backend)
	}
	store, err = s3.New(s3.Config{
		Endpoint: cred.Config.Endpoint, Bucket: cred.Config.Bucket,
		AccessKey: cred.Config.AccessKey, SecretKey: cred.Config.SecretKey,
		Region: cred.Config.Region, UseSSL: cred.Config.UseSSL,
	})
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.cache[cacheKey] = store
	r.mu.Unlock()
	return store, nil
}
