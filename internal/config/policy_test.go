package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 这条门禁钉住的是「配置读取不回头」：env 解析曾经在 api 与 cmd 里各写一份，
// 两份的合法取值集合靠人肉同步，结果 PPTS_PPROF=yes 与 PPTS_HSTS=yes 一个开一个不开。
// 现在读取统一走本包，新增代码若绕开它，这里直接红。
//
// 规则：
//  1. internal/api 与 cmd 是装配层，非测试代码不得直接读 os.Getenv/LookupEnv；
//  2. 任何非测试代码不得再定义 env 解析辅助（envInt/envBool/envFloat/envString…）；
//  3. 不得再用 EqualFold(os.Getenv(...), "true") 这种一次性布尔判定。
//
// 不约束 internal/db、internal/mail、internal/integrations/... 这类「配置构造函数」：
// 它们各自把自己的 env 键读成结构体，是配置的归属地，不属于绕道。

var envHelperPat = regexp.MustCompile(`func (?:env(?:Int|Bool|Float|String|Duration|List)(?:Value)?)\s*\(`)
var equalFoldPat = regexp.MustCompile(`EqualFold\([^)]*os\.Getenv`)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("定位仓库根目录: %v", err)
	}
	return root
}

func walkGoFiles(t *testing.T, root string, fn func(path string, rel string, body string)) {
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			// node_modules 与临时脚手架不参与：前者是依赖，后者本来就不入库。
			if base == "node_modules" || base == ".git" || base == "tmp" || base == "build" || base == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		fn(path, filepath.ToSlash(rel), string(body))
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 %s: %v", root, err)
	}
}

func TestAssemblyLayersDoNotReadEnvDirectly(t *testing.T) {
	root := repoRoot(t)
	var offenders []string
	walkGoFiles(t, root, func(path, rel, body string) {
		if !strings.HasPrefix(rel, "internal/api/") && !strings.HasPrefix(rel, "cmd/") {
			return
		}
		// 本包自己（internal/config）不会出现在这两个前缀下，无需例外。
		if strings.Contains(body, "os.Getenv(") || strings.Contains(body, "os.LookupEnv(") {
			offenders = append(offenders, rel)
		}
	})
	if len(offenders) > 0 {
		t.Fatalf("装配层直接读环境变量，请改走 internal/config：\n  %s", strings.Join(offenders, "\n  "))
	}
}

func TestNoLocalEnvParsers(t *testing.T) {
	root := repoRoot(t)
	var offenders []string
	walkGoFiles(t, root, func(path, rel, body string) {
		if envHelperPat.MatchString(body) && !strings.HasPrefix(rel, "internal/config/") {
			offenders = append(offenders, rel+" 命中 "+envHelperPat.FindString(body))
		}
		if equalFoldPat.MatchString(body) {
			offenders = append(offenders, rel+" 使用 EqualFold(os.Getenv(...), \"true\")")
		}
	})
	if len(offenders) > 0 {
		t.Fatalf("发现重复实现的配置解析，请使用 internal/config：\n  %s", strings.Join(offenders, "\n  "))
	}
}
