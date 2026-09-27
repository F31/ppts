// audit_coverage 渲染审计覆盖矩阵：**内容由源码 AST 提取，不由人手写**。
//
// 用法（仓库根执行）：
//
//	go run ./scripts/audit_coverage                  # 打印矩阵到 stdout
//	go run ./scripts/audit_coverage -out docs/审计覆盖矩阵.md  # 写入文件
//	go run ./scripts/audit_coverage -dir internal/api          # 只扫指定目录
//
// 为什么要有这个命令：矩阵是给人看的，但人写的清单会与代码漂移，且总是漂移成
// "清单说有、代码没有"。所以这里的每一行都来自 internal/auditcoverage.Scan，
// 与覆盖门禁 TestAuditCoverageRequiredActions 共用同一份事实，不存在第二份判断。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/F31/ppts/internal/auditcoverage"
)

func main() {
	var (
		dir  = flag.String("dir", "internal", "扫描根目录")
		out  = flag.String("out", "", "输出文件路径；为空则打印到 stdout")
		root = flag.String("root", ".", "仓库根，用于把路径裁剪成相对路径")
	)
	flag.Parse()
	if err := run(*dir, *out, *root); err != nil {
		fmt.Fprintln(os.Stderr, "audit_coverage:", err)
		os.Exit(1)
	}
}

func run(dir, out, root string) error {
	sites, err := auditcoverage.Scan(dir)
	if err != nil {
		return err
	}
	rel := func(p string) string {
		r, err := filepath.Rel(root, p)
		if err != nil {
			return p
		}
		return filepath.ToSlash(r)
	}

	var b strings.Builder
	b.WriteString("# 审计覆盖矩阵\n\n")
	b.WriteString("> 本文件由 `go run ./scripts/audit_coverage -out docs/审计覆盖矩阵.md` 生成，**请勿手工编辑**。\n")
	b.WriteString("> 数据来源：源码 AST（`internal/auditcoverage`），与门禁 `TestAuditCoverageRequiredActions` 同源。\n")
	b.WriteString("> 生成时间：" + time.Now().Format("2006-01-02 15:04:05 MST") + "\n\n")

	if len(sites) == 0 {
		b.WriteString("未发现任何审计埋点 —— 这几乎一定是提取器失效，而不是真的没有埋点。\n")
		return writeOut(out, b.String())
	}

	// 按动作聚合并给每类动作列出证据位置。
	type agg struct {
		resource string
		locs     []string
	}
	byAction := make(map[string]*agg)
	var dyn []auditcoverage.Site
	for _, s := range sites {
		if s.Action == auditcoverage.DynamicAction {
			dyn = append(dyn, s)
			continue
		}
		a := byAction[s.Action]
		if a == nil {
			a = &agg{resource: s.ResourceType}
			byAction[s.Action] = a
		}
		if a.resource == "" {
			a.resource = s.ResourceType
		}
		a.locs = append(a.locs, fmt.Sprintf("%s:%d", rel(s.File), s.Line))
	}
	actions := make([]string, 0, len(byAction))
	for a := range byAction {
		actions = append(actions, a)
	}
	sort.Strings(actions)

	b.WriteString("## 一、已解析动作\n\n")
	b.WriteString("| 动作 | ResourceType | 埋点位置 |\n|---|---|---|\n")
	for _, a := range actions {
		g := byAction[a]
		rt := g.resource
		if rt == "" {
			rt = "—"
		}
		b.WriteString(fmt.Sprintf("| `%s` | `%s` | %s |\n", a, rt, strings.Join(g.locs, " ")))
	}

	b.WriteString("\n## 二、无法静态解析的动作（需人工审阅）\n\n")
	if len(dyn) == 0 {
		b.WriteString("无。\n")
	} else {
		b.WriteString("这些位置的 `Action` 来自变量/参数，静态分析无法确定取值；**改了这里要亲手跑一遍确认审计仍有写入**。\n\n")
		b.WriteString("| 位置 | 标识符 | Kind |\n|---|---|---|\n")
		for _, s := range dyn {
			b.WriteString(fmt.Sprintf("| %s:%d | `%s` | %s |\n", rel(s.File), s.Line, s.DynamicName, s.Kind))
		}
	}

	b.WriteString("\n## 三、四类高危操作对照\n\n")
	b.WriteString("| 类别 | 期望动作 | 现状 |\n|---|---|---|\n")
	for _, want := range []struct {
		category string
		actions  []string
	}{
		{"删除（业务对象）", []string{"artifact.delete", "project.source_revision.delete", "publication.delete", "tenant.purge"}},
		{"删除（软删/归档）", []string{"project.archive"}},
		{"导出/下载", []string{"export.create", "export.download", "tenant.export"}},
		{"公开发布", []string{"publication.publish", "publication.feature", "publication.review", "publication.recall"}},
		{"匿名访问", []string{"publication.anonymous_access", "share_link.anonymous_access"}},
	} {
		var missing []string
		for _, a := range want.actions {
			if byAction[a] == nil {
				missing = append(missing, a)
			}
		}
		status := "已覆盖"
		if len(missing) > 0 {
			status = "**缺失**：" + strings.Join(missing, ", ")
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s |\n", want.category, strings.Join(want.actions, ", "), status))
	}
	return writeOut(out, b.String())
}

func writeOut(path, content string) error {
	if path == "" {
		fmt.Print(content)
		return nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}
