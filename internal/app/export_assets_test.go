package app

import (
	"bytes"
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/media"
)

type delayGetStore struct {
	objectstore.ObjectStore
	delay    time.Duration
	inFlight atomic.Int32
	peak     atomic.Int32
	gets     atomic.Int32
}

func (s *delayGetStore) Get(ctx context.Context, key objectstore.ObjectKey) (io.ReadCloser, objectstore.ObjectMeta, error) {
	s.gets.Add(1)
	n := s.inFlight.Add(1)
	for {
		p := s.peak.Load()
		if n <= p || s.peak.CompareAndSwap(p, n) {
			break
		}
	}
	defer s.inFlight.Add(-1)
	if s.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, objectstore.ObjectMeta{}, ctx.Err()
		case <-time.After(s.delay):
		}
	}
	return s.ObjectStore.Get(ctx, key)
}

func putExportAsset(t *testing.T, objects objectstore.ObjectStore, id string, body []byte) string {
	t.Helper()
	key := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: "project-1", Revision: "r1",
		AssetType: "render", AssetID: id, Ext: "bin",
	}
	if err := objects.Put(context.Background(), key, bytes.NewReader(body), objectstore.ObjectMeta{ContentType: "application/octet-stream"}); err != nil {
		t.Fatal(err)
	}
	return key.String()
}

func TestFetchTenantObjectsOverlapsGets(t *testing.T) {
	inner := objectstore.NewLocal(t.TempDir(), nil)
	store := &delayGetStore{ObjectStore: inner, delay: 40 * time.Millisecond}
	h := NewExportHandler(nil, nil, store, nil)
	keys := make([]string, 6)
	for i := range keys {
		keys[i] = putExportAsset(t, inner, "p"+string(rune('a'+i)), []byte{byte(i + 1)})
	}
	start := time.Now()
	data, errs := h.fetchTenantObjects(context.Background(), "tenant-1", keys, 4)
	elapsed := time.Since(start)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("key %d: %v", i, err)
		}
		if len(data[i]) != 1 || data[i][0] != byte(i+1) {
			t.Fatalf("key %d body = %v", i, data[i])
		}
	}
	if store.peak.Load() < 2 {
		t.Fatalf("peak concurrency = %d, want overlapping GETs", store.peak.Load())
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("elapsed %s, concurrent fetch should not stack 6×40ms", elapsed)
	}
}

func TestFetchTenantObjectsDedupsKeys(t *testing.T) {
	inner := objectstore.NewLocal(t.TempDir(), nil)
	store := &delayGetStore{ObjectStore: inner}
	h := NewExportHandler(nil, nil, store, nil)
	key := putExportAsset(t, inner, "shared", []byte("x"))
	data, errs := h.fetchTenantObjects(context.Background(), "tenant-1", []string{key, key, key}, 4)
	if store.gets.Load() != 1 {
		t.Fatalf("gets = %d, want 1 for the same key", store.gets.Load())
	}
	if len(data) != 3 || string(data[0]) != "x" || string(data[2]) != "x" || errs[1] != nil {
		t.Fatalf("data=%v errs=%v", data, errs)
	}
}

func TestLoadExportPagePNGsKeepsPreviousOnMiss(t *testing.T) {
	objects := objectstore.NewLocal(t.TempDir(), nil)
	h := NewExportHandler(nil, nil, objects, nil)
	first := putExportAsset(t, objects, "slide-a", []byte("AAA"))
	third := putExportAsset(t, objects, "slide-c", []byte("CCC"))
	out := h.loadExportPagePNGs(context.Background(), "tenant-1", []string{first, "", third}, 3, 32, 18)
	if len(out) != 3 {
		t.Fatalf("len = %d", len(out))
	}
	if string(out[0]) != "AAA" || string(out[1]) != "AAA" || string(out[2]) != "CCC" {
		t.Fatalf("pages = %q %q %q", out[0], out[1], out[2])
	}
}

func TestLoadExportAudioClipsFailsOnMissing(t *testing.T) {
	objects := objectstore.NewLocal(t.TempDir(), nil)
	h := NewExportHandler(nil, nil, objects, nil)
	_, err := h.loadExportAudioClips(context.Background(), "tenant-1", []media.SlideCue{{
		Segments: []media.SegmentCue{{AudioKey: "tenant-1/project-1/r1/audio/missing.wav"}},
	}})
	if err == nil {
		t.Fatal("missing clip must fail the export")
	}
}
