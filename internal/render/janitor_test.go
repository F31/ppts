package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// orphanID 返回一个合法的 job 目录名（ShortID 是 24 位小写十六进制）。
func orphanID(seed byte) string { return strings.Repeat(string(seed), 24) }

// mkOrphan 在 workDir 下造一个带内容的 job 目录，返回其路径与字节数。
func mkOrphan(t *testing.T, workDir, name string, files map[string]string) (string, int64) {
	t.Helper()
	dir := filepath.Join(workDir, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	var total int64
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
		total += int64(len(body))
	}
	return dir, total
}

// 重启后上一轮的 job 目录必须消失，包括重试产生的 -rN 目录。
// 这正是 tasks 3.14：jobs 只在内存，重启后没有任何记录能回收这些目录。
func TestStartCleanRemovesOrphanJobDirs(t *testing.T) {
	dir := t.TempDir()
	first, _ := mkOrphan(t, dir, orphanID('a'), map[string]string{"segment_000.mp4": "video"})
	retry, _ := mkOrphan(t, dir, orphanID('b')+"-r2", map[string]string{"nested/segment.mp4": "video"})

	if _, err := NewService(ServiceConfig{Backend: &countingBackend{}, WorkDir: dir}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{first, retry} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("孤儿目录 %s 未被清理（stat err = %v）", filepath.Base(p), err)
		}
	}
}

// 删除范围必须严格限定在本服务分配的名字上：WorkDir 可能和别的东西共处，
// 重启不该变成一次「把不认识的目录都删掉」。
func TestStartCleanKeepsForeignEntries(t *testing.T) {
	dir := t.TempDir()
	keep := []string{
		"selftest-3021454188",          // cmd/renderer -selftest 用 MkdirTemp 造的
		"tmp",                          // 运维临时目录
		"lost+found",                   // 文件系统自带
		"README",                       // 说明文件
		strings.Repeat("a", 23),        // 23 位：不是 ShortID
		strings.Repeat("a", 25),        // 25 位
		strings.ToUpper(orphanID('a')), // 大写十六进制：ShortID 只产小写
		orphanID('c') + "-r0",          // 重试后缀从 1 开始
		orphanID('d') + "-r",           // 缺编号
		orphanID('e') + "-x1",          // 非 r 后缀
	}
	for _, name := range keep {
		_, _ = mkOrphan(t, dir, name, map[string]string{"f": "x"})
	}
	// 名字像 job id 的普通文件也不该删：本服务的产物形态是目录。
	fileLikeJob := filepath.Join(dir, orphanID('f'))
	if err := os.WriteFile(fileLikeJob, []byte("not a dir"), 0o640); err != nil {
		t.Fatal(err)
	}

	svc, err := NewService(ServiceConfig{Backend: &countingBackend{}, WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	// 只在该用例里直接看目录内容：NewService 之后 WorkDir 里应只剩外来项。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name()] = true
	}
	if len(got) != len(keep)+1 {
		t.Fatalf("WorkDir 残留条目数 = %d，期望 %d：%v", len(got), len(keep)+1, got)
	}
	for _, name := range append(keep, orphanID('f')) {
		if !got[name] {
			t.Fatalf("外来条目 %s 被误删", name)
		}
	}
	// 清扫之后服务必须照常可用。
	id, err := svc.Submit(validPlan())
	if err != nil {
		t.Fatalf("清扫后提交失败: %v", err)
	}
	if _, ok := svc.Status(id); !ok {
		t.Fatal("清扫后任务查不到")
	}
	// 等任务落终态再退出：TempDir 清理会撞上后台 goroutine 仍在写的目录
	// （Windows 上表现为 "directory is not empty"）。
	waitUntilState(t, svc, id, StateSucceeded)
}

// 多副本共享 WorkDir 时清扫会删掉别的副本正在写的目录，必须能关掉。
func TestStartCleanRespectsKeepOrphans(t *testing.T) {
	dir := t.TempDir()
	p, _ := mkOrphan(t, dir, orphanID('a'), map[string]string{"segment.mp4": "video"})
	if _, err := NewService(ServiceConfig{
		Backend: &countingBackend{}, WorkDir: dir, KeepOrphans: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("KeepOrphans 打开时目录被删了: %v", err)
	}
}

// 清扫结果要能回答「这次重启回收了多少磁盘」，否则运维只能靠猜。
func TestStartCleanReportsBytes(t *testing.T) {
	dir := t.TempDir()
	_, want := mkOrphan(t, dir, orphanID('a'), map[string]string{
		"segment_000.mp4":        strings.Repeat("x", 100),
		"nested/segment_001.mp4": strings.Repeat("y", 50),
	})
	svc, err := NewService(ServiceConfig{
		Backend: &countingBackend{}, WorkDir: dir, KeepOrphans: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	rep := svc.cleanOrphanDirs()
	if rep.Removed != 1 || rep.Failed != 0 {
		t.Fatalf("report = %+v，期望 Removed=1 Failed=0", rep)
	}
	if rep.Bytes != want {
		t.Fatalf("Bytes = %d，期望 %d", rep.Bytes, want)
	}
	if rep.Empty() {
		t.Fatal("Empty 对非空 report 返回 true")
	}
}

// WorkDir 读不到（权限/挂载异常）不该让服务起不来：能起来报错，比直接崩掉好诊断。
func TestStartCleanSurvivesUnreadableWorkDir(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "missing")
	svc, err := NewService(ServiceConfig{
		Backend: &countingBackend{}, WorkDir: dir, KeepOrphans: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.cfg.WorkDir = gone
	rep := svc.cleanOrphanDirs()
	if rep.Failed != 1 || rep.Removed != 0 {
		t.Fatalf("report = %+v，期望 Failed=1 Removed=0", rep)
	}
}
