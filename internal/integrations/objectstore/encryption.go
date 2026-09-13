package objectstore

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
	"time"
)

var envelopeMagic = []byte("pptsenc1")

// EnvelopeEncryptionResolver decides whether a tenant requires object envelope encryption.
type EnvelopeEncryptionResolver interface {
	ObjectEnvelopeEncryption(ctx context.Context, tenantID string) (bool, error)
}

// WithEnvelopeEncryption encrypts Put payloads and decrypts Get payloads for tenants whose
// policy enables envelope_encryption. Direct signed URLs are disabled for encrypted tenants
// because they bypass service-side encryption/decryption.
func WithEnvelopeEncryption(store ObjectStore, resolver EnvelopeEncryptionResolver, key []byte) (ObjectStore, error) {
	if resolver == nil || len(key) == 0 {
		return store, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &encryptionStore{store: store, resolver: resolver, aead: aead}, nil
}

type encryptionStore struct {
	store    ObjectStore
	resolver EnvelopeEncryptionResolver
	aead     cipher.AEAD
}

func (s *encryptionStore) Put(ctx context.Context, key ObjectKey, r io.Reader, meta ObjectMeta) error {
	enabled, err := s.enabled(ctx, key.TenantID)
	if err != nil || !enabled {
		if err != nil {
			return err
		}
		return s.store.Put(ctx, key, r, meta)
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	sealed := make([]byte, 0, len(envelopeMagic)+len(nonce)+len(plain)+s.aead.Overhead())
	sealed = append(sealed, envelopeMagic...)
	sealed = append(sealed, nonce...)
	sealed = s.aead.Seal(sealed, nonce, plain, []byte(key.String()))
	meta.Size = int64(len(sealed))
	return s.store.Put(ctx, key, bytes.NewReader(sealed), meta)
}

func (s *encryptionStore) Get(ctx context.Context, key ObjectKey) (io.ReadCloser, ObjectMeta, error) {
	rc, meta, err := s.store.Get(ctx, key)
	if err != nil {
		return nil, ObjectMeta{}, err
	}
	enabled, err := s.enabled(ctx, key.TenantID)
	if err != nil || !enabled {
		if err != nil {
			_ = rc.Close()
			return nil, ObjectMeta{}, err
		}
		return rc, meta, nil
	}
	defer rc.Close()
	sealed, err := io.ReadAll(rc)
	if err != nil {
		return nil, ObjectMeta{}, err
	}
	plain, err := s.open(key, sealed)
	if err != nil {
		return nil, ObjectMeta{}, err
	}
	meta.Size = int64(len(plain))
	return io.NopCloser(bytes.NewReader(plain)), meta, nil
}

func (s *encryptionStore) SignedURL(ctx context.Context, key ObjectKey, op Operation, ttl time.Duration) (string, error) {
	enabled, err := s.enabled(ctx, key.TenantID)
	if err != nil {
		return "", err
	}
	if enabled && (op == OpRead || op == OpWrite) {
		return "", ErrOperationNotSupported
	}
	return s.store.SignedURL(ctx, key, op, ttl)
}

func (s *encryptionStore) Delete(ctx context.Context, key ObjectKey) error {
	return s.store.Delete(ctx, key)
}

func (s *encryptionStore) ApplyLifecyclePolicy(ctx context.Context, bucket string, policy LifecyclePolicy) error {
	return s.store.ApplyLifecyclePolicy(ctx, bucket, policy)
}

func (s *encryptionStore) ParseSignedURL(raw string) (ObjectKey, Operation, error) {
	parser, ok := s.store.(interface {
		ParseSignedURL(string) (ObjectKey, Operation, error)
	})
	if !ok {
		return ObjectKey{}, "", ErrSignatureInvalid
	}
	return parser.ParseSignedURL(raw)
}

func (s *encryptionStore) ApplyTenantLifecyclePolicy(ctx context.Context, tenantID, bucket string, policy LifecyclePolicy) error {
	applier, ok := s.store.(interface {
		ApplyTenantLifecyclePolicy(context.Context, string, string, LifecyclePolicy) error
	})
	if !ok {
		return s.store.ApplyLifecyclePolicy(ctx, bucket, policy)
	}
	return applier.ApplyTenantLifecyclePolicy(ctx, tenantID, bucket, policy)
}

func (s *encryptionStore) enabled(ctx context.Context, tenantID string) (bool, error) {
	return s.resolver.ObjectEnvelopeEncryption(ctx, tenantID)
}

func (s *encryptionStore) open(key ObjectKey, sealed []byte) ([]byte, error) {
	if len(sealed) < len(envelopeMagic)+s.aead.NonceSize() || !bytes.Equal(sealed[:len(envelopeMagic)], envelopeMagic) {
		return nil, errors.New("objectstore: encrypted object header missing")
	}
	nonceStart := len(envelopeMagic)
	nonceEnd := nonceStart + s.aead.NonceSize()
	nonce := sealed[nonceStart:nonceEnd]
	ciphertext := sealed[nonceEnd:]
	return s.aead.Open(nil, nonce, ciphertext, []byte(key.String()))
}
