package objectstore

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// filePublisherFake 记录收到的发布路径，验证「路由层把文件发布交给了后端」。
type filePublisherFake struct {
	err       error
	putFiles  []ObjectKey
	paths     []string
	putReader int
}

func (f *filePublisherFake) Put(_ context.Context, _ ObjectKey, _ io.Reader, _ ObjectMeta) error {
	f.putReader++
	return f.err
}
func (f *filePublisherFake) Get(_ context.Context, _ ObjectKey) (io.ReadCloser, ObjectMeta, error) {
	return nil, ObjectMeta{}, f.err
}
func (f *filePublisherFake) SignedURL(_ context.Context, _ ObjectKey, _ Operation, _ time.Duration) (string, error) {
	return "", f.err
}
func (f *filePublisherFake) Delete(_ context.Context, _ ObjectKey) error { return f.err }
func (f *filePublisherFake) ApplyLifecyclePolicy(_ context.Context, _ string, _ LifecyclePolicy) error {
	return f.err
}
func (f *filePublisherFake) PutFile(_ context.Context, key ObjectKey, path string, _ ObjectMeta) error {
	f.putFiles = append(f.putFiles, key)
	f.paths = append(f.paths, path)
	return f.err
}

// TestRegistryPutFileRoutesToFileBackend：路由层必须转发文件发布——
// 生产接线是 Registry → inventory → LocalFS，Registry 不转发的话 rename 发布永远到不了。
func TestRegistryPutFileRoutesToFileBackend(t *testing.T) {
	backend := &filePublisherFake{}
	registry, err := NewRegistry("local", map[string]ObjectStore{"local": backend}, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	key := testKey()
	if err := registry.PutFile(context.Background(), key, "/tmp/artifact.bin", ObjectMeta{}); err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	if len(backend.putFiles) != 1 || backend.putFiles[0] != key || backend.paths[0] != "/tmp/artifact.bin" {
		t.Fatalf("backend got putFiles=%v paths=%v", backend.putFiles, backend.paths)
	}
	if backend.putReader != 0 {
		t.Fatalf("file-capable backend must not see a reader Put, got %d", backend.putReader)
	}
}

// TestRegistryPutFileFallsBackForReaderOnlyBackend：S3 这类后端不实现 FilePublisher，
// Registry.PutFile 必须退回 reader 路径而不是报「不支持」——否则混合后端部署里
// 文件发布变成导出失败的来源。
func TestRegistryPutFileFallsBackForReaderOnlyBackend(t *testing.T) {
	backend := newMemoryStore()
	registry, err := NewRegistry("s3", map[string]ObjectStore{"s3": backend}, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	src := filepath.Join(t.TempDir(), "artifact.bin")
	data := []byte("artifact bytes")
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	key := testKey()
	if err := registry.PutFile(context.Background(), key, src, ObjectMeta{}); err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	if got := readMemoryStore(t, backend, key); string(got) != string(data) {
		t.Fatalf("fallback published %q, want %q", got, data)
	}
}

// TestInventoryPutFileRecordsBothBranches：文件发布的两条路（rename 直发 / reader 回退）
// 都必须登记——漏登记的对象在用量账本里不存在，会被周级孤儿扫描误删。
func TestInventoryPutFileRecordsBothBranches(t *testing.T) {
	key := testKey()
	srcData := []byte("artifact bytes")

	// 分支一：内层支持文件发布（真实 LocalFS，走 rename）。
	rec := &inventoryRecorderFake{}
	local := NewLocal(t.TempDir(), nil)
	store := WithInventory(local, rec)
	publisher, ok := store.(FilePublisher)
	if !ok {
		t.Fatal("inventoryStore wrapping a FilePublisher must implement FilePublisher")
	}
	src := filepath.Join(t.TempDir(), "artifact.bin")
	if err := os.WriteFile(src, srcData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publisher.PutFile(context.Background(), key, src, ObjectMeta{Size: int64(len(srcData))}); err != nil {
		t.Fatalf("PutFile (rename branch): %v", err)
	}
	if len(rec.recorded) != 1 || rec.recorded[0] != key {
		t.Fatalf("rename branch recorded = %+v, want [%s]", rec.recorded, key)
	}

	// 分支二：内层不支持（S3 类后端），退回 reader 路径，同样要登记。
	rec2 := &inventoryRecorderFake{}
	backend := newMemoryStore()
	store2 := WithInventory(backend, rec2)
	publisher2, ok2 := store2.(FilePublisher)
	if !ok2 {
		t.Fatal("inventoryStore must implement FilePublisher regardless of inner backend")
	}
	src2 := filepath.Join(t.TempDir(), "artifact.bin")
	if err := os.WriteFile(src2, srcData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publisher2.PutFile(context.Background(), key, src2, ObjectMeta{Size: int64(len(srcData))}); err != nil {
		t.Fatalf("PutFile (fallback branch): %v", err)
	}
	if len(rec2.recorded) != 1 || rec2.recorded[0] != key {
		t.Fatalf("fallback branch recorded = %+v, want [%s]", rec2.recorded, key)
	}
	if got := readMemoryStore(t, backend, key); string(got) != string(srcData) {
		t.Fatalf("fallback published %q, want %q", got, srcData)
	}
}

// TestEncryptionStoreDoesNotPublishFiles 是安全门禁：加密包装绝不能实现 FilePublisher。
//
// 文件路径上交给后端的是明文；一旦实现了这个接口，加密租户的导出会直接把明文
// rename 进对象根，绕过信封加密。现在这条靠类型断言失败回退 reader 路径（加密在
// Put 里做）；将来若有人给 encryptionStore 加 PutFile「顺手补全接口」，这里先红。
func TestEncryptionStoreDoesNotPublishFiles(t *testing.T) {
	store, err := WithEnvelopeEncryption(newMemoryStore(), encryptionResolverFake{"tenant-1": true}, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("WithEnvelopeEncryption: %v", err)
	}
	if _, ok := store.(FilePublisher); ok {
		t.Fatal("encryptionStore must not implement FilePublisher: it would publish plaintext by rename")
	}
}

// readMemoryStore 从 memoryStore 读回对象内容（发布正确性的共用断言）。
func readMemoryStore(t *testing.T, store ObjectStore, key ObjectKey) []byte {
	t.Helper()
	r, _, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
