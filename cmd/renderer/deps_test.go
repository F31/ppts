package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestRendererDoesNotLinkStorageStack 把「渲染容器不背数据库」从注释变成门禁。
//
// 背景：internal/animation 曾把模型、默认规格生成（依赖 project.Page/Shape）与
// SQLite/PostgreSQL 存储放在同一个包里，导致 cmd/renderer 的传递依赖里出现
// modernc sqlite 与 pgx——一个只做光栅渲染的容器白背 7MB 与它们的 CVE 面。
//
// 拆成 animation（模型）+ animdefault（从解析结构生成默认规格）+ animstore（持久化）
// 之后这条依赖才断干净。这里用 `go list -deps` 断言传递闭包，避免以后有人图方便
// 在 animation 里重新 import project 或 db。
func TestRendererDoesNotLinkStorageStack(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	banned := []string{
		"github.com/F31/ppts/internal/db",
		"github.com/F31/ppts/internal/project",
		"github.com/F31/ppts/internal/animation/animstore",
		"github.com/jackc/pgx",
		"modernc.org/sqlite",
	}
	for _, pkg := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		for _, prefix := range banned {
			if pkg == prefix || strings.HasPrefix(pkg, prefix+"/") {
				t.Errorf("cmd/renderer must not link %s (pulled in by %q); "+
					"keep model code free of storage/parsing dependencies", pkg, prefix)
			}
		}
	}
}
