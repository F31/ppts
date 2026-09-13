package objectstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type encryptionResolverFake map[string]bool

func (r encryptionResolverFake) ObjectEnvelopeEncryption(_ context.Context, tenantID string) (bool, error) {
	return r[tenantID], nil
}

func TestEnvelopeEncryptionRoundTripAndSignedURLPolicy(t *testing.T) {
	backend := newMemoryStore()
	store, err := WithEnvelopeEncryption(backend, encryptionResolverFake{"tenant-1": true}, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("WithEnvelopeEncryption: %v", err)
	}
	key := ObjectKey{TenantID: "tenant-1", ProjectID: "p", Revision: "r", AssetType: "audio", AssetID: "a", Ext: "mp3"}
	plain := []byte("hello audio")
	if err := store.Put(context.Background(), key, bytes.NewReader(plain), ObjectMeta{Size: int64(len(plain))}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	stored := backend.objects[key.String()]
	if bytes.Equal(stored, plain) || !bytes.HasPrefix(stored, envelopeMagic) {
		t.Fatalf("stored payload was not encrypted: %q", string(stored))
	}
	rc, meta, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, plain) || meta.Size != int64(len(plain)) {
		t.Fatalf("got=%q meta=%+v", string(got), meta)
	}
	if _, err := store.SignedURL(context.Background(), key, OpRead, time.Minute); !errors.Is(err, ErrOperationNotSupported) {
		t.Fatalf("SignedURL encrypted err = %v want ErrOperationNotSupported", err)
	}
}

func TestEnvelopeEncryptionDisabledTenantPassthrough(t *testing.T) {
	backend := newMemoryStore()
	store, err := WithEnvelopeEncryption(backend, encryptionResolverFake{}, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("WithEnvelopeEncryption: %v", err)
	}
	key := ObjectKey{TenantID: "tenant-1", ProjectID: "p", Revision: "r", AssetType: "source", AssetID: "a"}
	if err := store.Put(context.Background(), key, bytes.NewReader([]byte("plain")), ObjectMeta{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if string(backend.objects[key.String()]) != "plain" {
		t.Fatalf("stored = %q want plain", string(backend.objects[key.String()]))
	}
}
