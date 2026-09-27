package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/auditcoverage"
)

// repoRoot 从测试工作目录向上找到 go.mod，避免硬编码相对层级。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, serr := os.Stat(filepath.Join(dir, "go.mod")); serr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("go.mod not found above test dir")
	return ""
}

// relFile 把 Scan 返回的绝对路径裁剪成仓库相对路径（Scan 接绝对路径时返回绝对路径，
// 对账两边必须统一坐标，否则比较永远失配 —— 首版就在这里栽了，报出来的是"全缺失"）。
func relFile(root, file string) string {
	p := filepath.ToSlash(file)
	prefix := filepath.ToSlash(root) + "/"
	if strings.HasPrefix(p, prefix) {
		return strings.TrimPrefix(p, prefix)
	}
	return p
}

func auditSites(t *testing.T) []auditcoverage.Site {
	t.Helper()
	root := repoRoot(t)
	sites, err := auditcoverage.Scan(filepath.Join(root, "internal"))
	if err != nil {
		t.Fatalf("auditcoverage.Scan: %v", err)
	}
	if len(sites) == 0 {
		// 空结果几乎一定是提取器失效，而不是"真的没有埋点"——把它当失败，别让它悄悄绿过去。
		t.Fatal("no audit sites found at all: extractor broken, not repository clean")
	}
	return sites
}

// requiredAudit 是"必须有审计"的动作清单（P2-B2 覆盖矩阵 §3）。
//
// 判据来自本次盘点：删除 / 导出 / 公开发布 / 匿名访问四类操作一旦无痕，
// 事后就回答不了"是谁、什么时候、动了哪个对象"。逐条登记而不是笼统断言，
// 是为了让"少了一处"能精确定位到文件，而不是甩一条"覆盖不足"。
//
// 覆盖矩阵本身由 `go run ./scripts/audit_coverage` 渲染，与这里共用 auditcoverage.Scan，
// 不存在第二份清单。
var requiredAudit = []struct {
	file   string
	action string
	why    string
}{
	// 删除类：对象被消掉之后，"谁干的"就没第二个地方问了。
	{"internal/api/artifact.go", "artifact.delete", "成品删除"},
	{"internal/api/editor.go", "project.source_revision.delete", "源版本软删"},
	{"internal/api/project.go", "project.archive", "项目归档（本系统里最接近删除的操作）"},
	{"internal/api/public.go", "publication.delete", "公开作品下架"},
	{"internal/api/tenant.go", "tenant.purge", "租户数据擦除"},

	// 导出/下载类：数据离开系统的那一刻。
	{"internal/api/export.go", "export.create", "导出任务创建"},
	{"internal/api/export.go", "export.download", "成品下载签发"},
	{"internal/api/tenant.go", "tenant.export", "租户全量导出"},

	// 公开发布类：数据从"站内可见"变成"站外可见"。
	{"internal/api/public.go", "publication.publish", "发布作品"},
	{"internal/api/public.go", "publication.feature", "官方精选"},
	{"internal/api/public.go", "publication.review", "审核通过/驳回"},
	{"internal/api/public.go", "publication.recall", "撤回"},
	{"internal/api/public.go", "publication.anonymous_access", "匿名访问公开作品"},

	// 匿名分享类：私密链接被谁打开过，是外发传播事故唯一能追的线索。
	{"internal/api/collab.go", "share_link.anonymous_access", "匿名访问分享链接"},
}

func TestAuditCoverageRequiredActions(t *testing.T) {
	sites := auditSites(t)
	for _, req := range requiredAudit {
		var found bool
		for _, s := range sites {
			if relFile(repoRoot(t), s.File) == req.file && s.Action == req.action {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing audit %q in %s（%s）", req.action, req.file, req.why)
		}
	}
}

// anonymousCredentials 是"绝不能进审计日志"的东西。
//
// 前三项是**能力本身**：token 是私密分享的钥匙、publicId 是不可反推地址的那一半，
// 写进日志等于把有效期更长的凭据复制到另一处留存。
// 后两项是**可识别信息**：匿名访问的意义就在于不留来访者身份，顺手记 IP/UA 等于自毁约定。
var anonymousCredentials = regexp.MustCompile(`\b(token|publicId|publicID|RemoteAddr|UserAgent|User-Agent)\b`)

// TestAuditAnonymousSitesRecordNoCredentials 检查匿名访问埋点没有把凭证/访客身份带进去。
//
// 这条用例存在的理由：脱敏是写在注释里的约定，注释不会失败，只有断言会。
// 匿名访问的审计一旦顺手把 token 记下来，追溯便利会以"审计库里的永久有效下载链接"为代价。
func TestAuditAnonymousSitesRecordNoCredentials(t *testing.T) {
	root := repoRoot(t)
	sites := auditSites(t)
	var checked int
	for _, s := range sites {
		if s.Action != "publication.anonymous_access" && s.Action != "share_link.anonymous_access" {
			continue
		}
		rel := relFile(root, s.File)
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", s.File, err)
		}
		if s.Start < 0 || s.End > len(src) || s.Start >= s.End {
			t.Errorf("%s:%d bad source range (%d,%d)", rel, s.Line, s.Start, s.End)
			continue
		}
		snippet := string(src[s.Start:s.End])
		if hit := anonymousCredentials.FindString(snippet); hit != "" {
			t.Errorf("%s:%d %s 的审计里出现了 %q —— 匿名访问不得记录凭证/访客身份\n%s",
				s.File, s.Line, s.Action, hit, snippet)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no anonymous audit site found — either they were removed, or file names drifted")
	}
}
