package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocalAssetPublisherWritesUnderRoot(t *testing.T) {
	root := t.TempDir()
	p, err := NewLocalAssetPublisher(root)
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	data := []byte("\x89PNG\r\n\x1a\nfake")
	rel, err := p.Publish(context.Background(), "image/png", data)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !strings.HasSuffix(rel, ".png") {
		t.Fatalf("rel = %q, want .png suffix", rel)
	}
	if filepath.IsAbs(rel) || strings.Contains(rel, "..") {
		t.Fatalf("rel = %q, must be a relative path", rel)
	}
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("content mismatch")
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// 跨 uid 的渲染容器要读得到：0600 会让渲染侧 permission denied。
	if perm := info.Mode().Perm(); perm&0o044 == 0 {
		t.Fatalf("perm = %04o, 渲染容器（可能是别的 uid）读不到", perm)
	}
}

func TestLocalAssetPublisherDeduplicatesByContent(t *testing.T) {
	root := t.TempDir()
	p, err := NewLocalAssetPublisher(root)
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	data := []byte("same-bytes")
	first, err := p.Publish(context.Background(), "image/png", data)
	if err != nil {
		t.Fatalf("publish 1: %v", err)
	}
	second, err := p.Publish(context.Background(), "image/png", data)
	if err != nil {
		t.Fatalf("publish 2: %v", err)
	}
	if first != second {
		t.Fatalf("同一份字节得到两个路径：%q vs %q", first, second)
	}
	if n := countFiles(t, root); n != 1 {
		t.Fatalf("目录里有 %d 个文件，want 1（同一份字节应复用）", n)
	}
	other, err := p.Publish(context.Background(), "image/png", []byte("other-bytes"))
	if err != nil {
		t.Fatalf("publish 3: %v", err)
	}
	if other == first {
		t.Fatalf("不同字节得到同一个路径 %q", other)
	}
}

func TestLocalAssetPublisherRejectsBadInput(t *testing.T) {
	p, err := NewLocalAssetPublisher(t.TempDir())
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	if _, err := p.Publish(context.Background(), "image/png", nil); err == nil {
		t.Fatal("空字节应当报错：空图会让那页渲染成空白，且不会在任何地方报错")
	}
	if _, err := p.Publish(context.Background(), "application/pdf", []byte("x")); err == nil {
		t.Fatal("未知内容类型应当报错：渲染侧按扩展名选解码器")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Publish(ctx, "image/png", []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消的 ctx 应返回 context.Canceled，got %v", err)
	}
}

// 共享卷可能被别的进程写过。落到符号链接上的 rename 是跟随写入，
// 等于把「任意路径写」的把手交出去。
func TestLocalAssetPublisherRefusesSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上创建符号链接需要额外权限")
	}
	root := t.TempDir()
	p, err := NewLocalAssetPublisher(root)
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	data := []byte("payload")
	rel, err := p.Publish(context.Background(), "image/png", data)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.Remove(dst); err != nil {
		t.Fatalf("remove: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.Symlink(outside, dst); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := p.Publish(context.Background(), "image/png", data); err == nil {
		t.Fatal("目标是符号链接时必须报错")
	}
	if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("写入穿过了符号链接，落到了资产根之外")
	}
}

func TestNewLocalAssetPublisherRejectsEmptyRoot(t *testing.T) {
	if _, err := NewLocalAssetPublisher("   "); err == nil {
		t.Fatal("空目录应当报错，而不是写到当前工作目录")
	}
}

// 启动时就该发现「目录不可写」，而不是等到每一单导出都静默降级成静态版。
func TestNewLocalAssetPublisherRejectsUnwritableRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 的目录权限语义与 POSIX 不同")
	}
	if os.Geteuid() == 0 {
		t.Skip("root 无视目录权限")
	}
	root := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(root, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := NewLocalAssetPublisher(root); err == nil {
		t.Fatal("不可写目录应当报错")
	}
}

func countFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return n
}
