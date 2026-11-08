package objectstore

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// putFileFixture 在 srcDir 里放一份样本文件，返回路径与内容。
func putFileFixture(t *testing.T, srcDir string) (string, []byte) {
	t.Helper()
	data := []byte("a large artifact would live here; bytes are bytes")
	path := filepath.Join(srcDir, "artifact.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, data
}

// TestLocalPutFileRenamesSameVolume 是「文件发布 = rename，不是拷贝」的判据：
// 发布后源路径必须不复存在。如果实现退回了拷贝（或有人把 rename 改成 copy），
// 源文件还在，这条立刻红——它守的就是「零字节搬运」本身。
func TestLocalPutFileRenamesSameVolume(t *testing.T) {
	srcDir := t.TempDir()
	rootDir := t.TempDir()
	path, data := putFileFixture(t, srcDir)
	store := NewLocal(rootDir, nil)
	key := testKey()
	meta := ObjectMeta{ContentHash: "irrelevant-locally", Size: int64(len(data))}

	if err := store.PutFile(context.Background(), key, path, meta); err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("source must be renamed away, stat err = %v（拷贝实现会留下源文件）", err)
	}
	full := filepath.Join(rootDir, filepath.FromSlash(key.String()))
	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("published object: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("published bytes mismatch: %q want %q", got, data)
	}
	// 权限必须与 Put 的语义一致：产物临时文件是 0600，不补 chmod 就把 0600 带进对象根。
	// 注意平台差异：Windows 只区分只读位，0600 与 0644 都是「可写」，漏 chmod 在
	// Windows 上不可观测（本机变异验证时存活）——Linux CI 上该变异必死（实测
	// -rw------- ≠ -rw-r--r--，已用 WSL 复核）。两个平台的期望值不同但都锁死该断言。
	wantPerm := os.FileMode(0o644)
	if runtime.GOOS == "windows" {
		wantPerm = 0o666
	}
	info, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != wantPerm {
		t.Fatalf("published mode = %v, want %v（与 Put 一致）", info.Mode().Perm(), wantPerm)
	}
}

// TestLocalPutFileFallsBackToCopyWhenRenameFails 覆盖跨盘/被占用时的拷贝回退：
// rename 被注入失败后，对象仍要完整发布，且源文件保留（由调用方清理）。
func TestLocalPutFileFallsBackToCopyWhenRenameFails(t *testing.T) {
	srcDir := t.TempDir()
	rootDir := t.TempDir()
	path, data := putFileFixture(t, srcDir)
	store := NewLocal(rootDir, nil)
	store.renameHook = func(string, string) error { return os.ErrPermission }
	key := testKey()

	if err := store.PutFile(context.Background(), key, path, ObjectMeta{}); err != nil {
		t.Fatalf("PutFile with failing rename: %v", err)
	}
	// 拷贝回退不移动源文件：清理责任在调用方（Close 里 os.Remove），这里只验证它还在。
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("copy fallback must keep the source, stat err = %v", err)
	}
	full := filepath.Join(rootDir, filepath.FromSlash(key.String()))
	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("published object: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("published bytes mismatch after fallback: %q want %q", got, data)
	}
}

// TestLocalPutFileReplacesExistingObject 断言文件发布与 Put 一样是幂等覆盖：
// 同键重发（重试、重放）必须原子替换旧对象，而不是报错或留下半截。
func TestLocalPutFileReplacesExistingObject(t *testing.T) {
	srcDir := t.TempDir()
	rootDir := t.TempDir()
	store := NewLocal(rootDir, nil)
	key := testKey()

	first, _ := putFileFixture(t, srcDir)
	if err := store.PutFile(context.Background(), key, first, ObjectMeta{}); err != nil {
		t.Fatalf("first PutFile: %v", err)
	}
	second := filepath.Join(srcDir, "artifact-2.bin")
	newData := []byte("the retry produced different bytes")
	if err := os.WriteFile(second, newData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.PutFile(context.Background(), key, second, ObjectMeta{}); err != nil {
		t.Fatalf("second PutFile: %v", err)
	}
	full := filepath.Join(rootDir, filepath.FromSlash(key.String()))
	got, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newData) {
		t.Fatalf("object after re-publish = %q, want %q", got, newData)
	}
}
