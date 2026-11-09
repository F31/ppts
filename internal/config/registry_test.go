package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestCatalogCoversEveryRead(t *testing.T) {
	root := repoRoot(t)
	var missing []string
	walkGoFiles(t, root, func(path, rel, body string) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, body, 0)
		if err != nil {
			t.Fatalf("解析 %s: %v", rel, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			key := strings.Trim(lit.Value, "`\"")
			if !pptKey.MatchString(key) {
				return true
			}
			if _, known := Lookup(key); !known {
				missing = append(missing, rel+": "+key)
			}
			return true
		})
	})
	if len(missing) > 0 {
		t.Fatalf("这些键被读取但没有登记（补 internal/config/catalog.go 后 go generate ./internal/config/）：\n  %s",
			strings.Join(missing, "\n  "))
	}
}

var pptKey = regexp.MustCompile(`^PPTS_[A-Z0-9_]+$`)
var envPPTS = regexp.MustCompile(`process\.env\.(PPTS_[A-Z0-9_]+)`)

func TestCatalogCoversRendererEnv(t *testing.T) {
	root := repoRoot(t)
	var missing []string
	err := filepath.Walk(filepath.Join(root, "renderer"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".mjs") && !strings.HasSuffix(path, ".js") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, m := range envPPTS.FindAllStringSubmatch(string(body), -1) {
			if _, known := Lookup(m[1]); !known {
				missing = append(missing, filepath.ToSlash(rel)+": "+m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		t.Fatalf("渲染脚本读取了未登记的键：\n  %s", strings.Join(missing, "\n  "))
	}
}

func TestGeneratedCatalogIsCurrent(t *testing.T) {
	root := repoRoot(t)
	check := func(rel, want string) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("读取 %s: %v（先运行 go generate ./internal/config/）", rel, err)
		}
		if strings.ReplaceAll(string(got), "\r\n", "\n") != want {
			t.Fatalf("%s 与登记表不一致，运行 go generate ./internal/config/", rel)
		}
	}
	check("packaging/config.env.example", InstallExample())
	check("packaging/config.md", CatalogMarkdown())
	example := InstallExample()
	for _, key := range []string{
		"PPTS_DATABASE_URL=",
		"PPTS_SCHEDULER_DATABASE_URL=",
		"PPTS_OBJECT_SECRET=",
		"PPTS_JWT_SECRET=",
		"PPTS_PASSWORD_PEPPER=",
		"PPTS_GATEWAY_AES_KEY_BASE64=",
		"PPTS_HTTP_ADDR=",
	} {
		if !strings.Contains(example, "\n"+key) && !strings.HasPrefix(example, key) {
			t.Fatalf("安装模板缺少 %s，install.sh 会写不进去", key)
		}
	}
}

func TestUploadDefaultMatchesFrontend(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "web", "src", "components", "ImportDialog.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "100 * 1024 * 1024") {
		t.Fatal("导入对话框的 100 MiB 与 PPTS_MAX_UPLOAD_BYTES 缺省不一致")
	}
	spec, ok := Lookup("PPTS_MAX_UPLOAD_BYTES")
	if !ok || spec.DefaultText != "104857600" {
		t.Fatalf("PPTS_MAX_UPLOAD_BYTES 缺省 = %q，要与 100 MiB 对齐", spec.DefaultText)
	}
}

func TestUnknownIgnoresRegisteredAndValues(t *testing.T) {
	t.Setenv("PPTS_HTTP_ADDR", "secret-value")
	t.Setenv("PPTS_NOT_A_REAL_SETTING", "also-secret")
	var keys []string
	for _, key := range Unknown() {
		if key == "PPTS_NOT_A_REAL_SETTING" || key == "PPTS_HTTP_ADDR" {
			keys = append(keys, key)
		}
		if strings.Contains(key, "secret") {
			t.Fatalf("未登记列表带出了取值: %s", key)
		}
	}
	if strings.Join(keys, ",") != "PPTS_NOT_A_REAL_SETTING" {
		t.Fatalf("Unknown = %v", keys)
	}
}
