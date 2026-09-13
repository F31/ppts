package objectstore

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type inventoryRecorderFake struct {
	recorded []ObjectKey
	deleted  []ObjectKey
	err      error
}

func (f *inventoryRecorderFake) RecordObject(_ context.Context, key ObjectKey, _ ObjectMeta) error {
	f.recorded = append(f.recorded, key)
	return f.err
}

func (f *inventoryRecorderFake) DeleteObjectRecord(_ context.Context, key ObjectKey) error {
	f.deleted = append(f.deleted, key)
	return f.err
}

type inventoryBackendFake struct {
	err error
}

func (f *inventoryBackendFake) Put(context.Context, ObjectKey, io.Reader, ObjectMeta) error {
	return f.err
}
func (f *inventoryBackendFake) Get(context.Context, ObjectKey) (io.ReadCloser, ObjectMeta, error) {
	return io.NopCloser(strings.NewReader("")), ObjectMeta{}, f.err
}
func (f *inventoryBackendFake) SignedURL(context.Context, ObjectKey, Operation, time.Duration) (string, error) {
	return "", f.err
}
func (f *inventoryBackendFake) Delete(context.Context, ObjectKey) error { return f.err }
func (f *inventoryBackendFake) ApplyLifecyclePolicy(context.Context, string, LifecyclePolicy) error {
	return f.err
}

func TestWithInventoryRecordsAfterSuccessfulPutAndDelete(t *testing.T) {
	rec := &inventoryRecorderFake{}
	store := WithInventory(&inventoryBackendFake{}, rec)
	key := ObjectKey{TenantID: "t", ProjectID: "p", Revision: "r", AssetType: "audio", AssetID: "a", Ext: "mp3"}

	if err := store.Put(context.Background(), key, strings.NewReader("x"), ObjectMeta{Size: 1}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(rec.recorded) != 1 || rec.recorded[0] != key {
		t.Fatalf("recorded = %+v", rec.recorded)
	}
	if err := store.Delete(context.Background(), key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(rec.deleted) != 1 || rec.deleted[0] != key {
		t.Fatalf("deleted = %+v", rec.deleted)
	}
}

func TestWithInventoryDoesNotRecordFailedPut(t *testing.T) {
	want := errors.New("boom")
	rec := &inventoryRecorderFake{}
	store := WithInventory(&inventoryBackendFake{err: want}, rec)
	key := ObjectKey{TenantID: "t", ProjectID: "p", Revision: "r", AssetType: "audio", AssetID: "a"}

	if err := store.Put(context.Background(), key, strings.NewReader("x"), ObjectMeta{}); !errors.Is(err, want) {
		t.Fatalf("Put err = %v want %v", err, want)
	}
	if len(rec.recorded) != 0 {
		t.Fatalf("unexpected record: %+v", rec.recorded)
	}
}
