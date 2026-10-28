package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAsset 在资产根里造一个文件（含父目录）。
func writeAsset(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStageAssetsCopiesOnlyReferenced：只搬计划真正引用的文件。
//
// 整目录搬运会让一份 30 页计划把资产根里几百兆的无关页面也塞进 tmpfs，
// 而工作目录常常只有 2g。
func TestStageAssetsCopiesOnlyReferenced(t *testing.T) {
	assets := t.TempDir()
	work := t.TempDir()
	writeAsset(t, assets, "pages/p0.png", "page-zero")
	writeAsset(t, assets, "pages/unused.png", "never-copied")

	svc, err := NewService(ServiceConfig{
		Backend:  &countingBackend{},
		WorkDir:  work,
		AssetDir: assets,
		Version:  "test-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	jobDir := t.TempDir()
	plan := pagesPlan("asset", 2)
	if err := svc.stageAssets(plan, jobDir); err != nil {
		t.Fatalf("stageAssets: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(jobDir, "pages", "p0.png"))
	if err != nil {
		t.Fatalf("底图未复制: %v", err)
	}
	if string(got) != "page-zero" {
		t.Fatalf("底图内容 = %q", got)
	}
	if _, err := os.Stat(filepath.Join(jobDir, "pages", "unused.png")); !os.IsNotExist(err) {
		t.Fatalf("未引用的资产不该被复制（err=%v）", err)
	}
}

// TestSubmitStagesAssets：走完整提交路径，成功任务的 job 目录里必须有底图。
//
// 这条是用例级的回归：起跑前的 RemoveAll 会把预先放好的文件删掉，
// 只有 run 里那次 stageAssets 才能让 baseImage 重新出现。
func TestSubmitStagesAssets(t *testing.T) {
	assets := t.TempDir()
	writeAsset(t, assets, "pages/p0.png", "page-zero")
	svc, work := newServiceWith(t, func(cfg *ServiceConfig) {
		cfg.AssetDir = assets
	})

	id, err := svc.Submit(pagesPlan("stage", 1))
	if err != nil {
		t.Fatal(err)
	}
	st := waitState(t, svc, id)
	if st.State != StateSucceeded {
		t.Fatalf("state = %s, err = %s", st.State, st.Error)
	}
	got, err := os.ReadFile(filepath.Join(work, id, "pages", "p0.png"))
	if err != nil {
		t.Fatalf("job 目录里没有底图: %v", err)
	}
	if string(got) != "page-zero" {
		t.Fatalf("底图内容 = %q", got)
	}
}

// TestSubmitFailsOnMissingAsset：缺图必须报出「哪一页、哪个路径」，
// 而不是让渲染器在 Node 阶段抛一个看不出因果的错误。
func TestSubmitFailsOnMissingAsset(t *testing.T) {
	assets := t.TempDir()
	svc, _ := newServiceWith(t, func(cfg *ServiceConfig) {
		cfg.AssetDir = assets
	})

	id, err := svc.Submit(pagesPlan("missing", 1))
	if err != nil {
		t.Fatal(err)
	}
	st := waitState(t, svc, id)
	if st.State != StateFailed {
		t.Fatalf("state = %s", st.State)
	}
	for _, want := range []string{"准备底图失败", "page 0", "pages/p0.png"} {
		if !strings.Contains(st.Error, want) {
			t.Fatalf("错误信息缺少 %q: %s", want, st.Error)
		}
	}
}

// TestStageAssetsRejectsEscape：rel 来自请求体，两侧都要过路径校验。
// Validate 已经拦过一次，这里是最后一道能接触文件系统的关口。
func TestStageAssetsRejectsEscape(t *testing.T) {
	assets := t.TempDir()
	work := t.TempDir()
	outer := filepath.Join(filepath.Dir(assets), "secret.png")
	if err := os.WriteFile(outer, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{"../secret.png", "pages/../../secret.png", "/etc/passwd"} {
		if err := copyAsset(assets, work, rel); err == nil {
			t.Fatalf("路径 %q 应被拒绝", rel)
		}
	}
}

// TestStageAssetsRejectsSymlink：符号链接可以指向资产根之外，
// 只校验路径字符串是不够的。
func TestStageAssetsRejectsSymlink(t *testing.T) {
	assets := t.TempDir()
	work := t.TempDir()
	target := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(assets, "link.png")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("本机不支持符号链接: %v", err)
	}

	err := copyAsset(assets, work, "link.png")
	if err == nil || !strings.Contains(err.Error(), "不是普通文件") {
		t.Fatalf("符号链接应被拒绝，err = %v", err)
	}
}

// TestAssetDirValidatedAtStartup：配错目录应当在启动时炸，
// 而不是让每个任务各自失败一次。
func TestAssetDirValidatedAtStartup(t *testing.T) {
	_, err := NewService(ServiceConfig{
		Backend:  &countingBackend{},
		WorkDir:  t.TempDir(),
		AssetDir: filepath.Join(t.TempDir(), "does-not-exist"),
	})
	if err == nil {
		t.Fatal("不存在的 AssetDir 应让 NewService 失败")
	}

	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(ServiceConfig{
		Backend:  &countingBackend{},
		WorkDir:  t.TempDir(),
		AssetDir: file,
	}); err == nil {
		t.Fatal("AssetDir 指向普通文件时应失败")
	}
}

// TestNoAssetDirKeepsOldBehaviour：不配资产根时行为不变（向后兼容）。
func TestNoAssetDirKeepsOldBehaviour(t *testing.T) {
	svc, _ := newServiceWith(t, func(cfg *ServiceConfig) {})
	id, err := svc.Submit(pagesPlan("noasset", 1))
	if err != nil {
		t.Fatal(err)
	}
	if st := waitState(t, svc, id); st.State != StateSucceeded {
		t.Fatalf("state = %s, err = %s", st.State, st.Error)
	}
}
