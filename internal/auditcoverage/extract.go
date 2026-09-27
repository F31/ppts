// Package auditcoverage 从源码 AST 提取审计埋点，供「覆盖矩阵」与「覆盖门禁」共用同一份事实。
//
// 为什么要在源代码上提取而不是手写清单（P2-B2）：
//   - 手写清单会与代码漂移，且漂移方向总是"清单说有、代码没有"——最危险的一种；
//   - "清单上没有"和"代码里没有"会混成同一个结论，人工核对成本低但不做就一定错。
//
// 因此：矩阵由本包渲染，门禁由本包判据，两者共享 Scan 的结果，不存在第二份判断。
//
// 三种埋点写法都要认出来（缺一种就会漏掉整整一类）：
//  1. 直接构造 audit.Event{Action: "a.b"}；
//  2. Action 用包级常量（Action: publicationActionPublish）；
//  3. 动作名经助手参数下传： recordXxx(ctx, rec, ..., action, ...)
//     助手内部才把它放进 Action。这是最容易被人工盘点漏掉的一类 —— 埋点位置
//     与动作名分处两个文件，肉眼搜 "a.b" 搜不到。
package auditcoverage

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// DynamicAction 是动作名无法静态解析时的占位值，例如 `Action: action` 这种由参数传入、
// 且调用点也没传字面量的写法。用显式占位而不是空串，是为了让"没解析出来"与
// "动作名是空串"在矩阵里看起来不同。
const DynamicAction = "<dynamic>"

// Kind 标记一条埋点是从哪里认出来的。
const (
	KindEvent = "event" // 直接构造 audit.Event{...} 字面量
	KindCall  = "call"  // 经助手函数的 action 形参下传
)

// Site 是一处审计埋点。Start/End 为文件内字节区间，供调用方取原文做脱敏等二级检查。
type Site struct {
	File         string
	Line         int
	Action       string
	ResourceType string
	Kind         string
	DynamicName  string // 无法静态解析时的标识符名
	Start, End   int
}

type parsedDoc struct {
	path string
	dir  string
	file *ast.File
	tf   *token.File
}

// helper 记录"某个函数第几个形参就是审计动作名"。
type helper struct {
	dir   string
	name  string
	index int
}

// Scan 递归扫描 root 下所有非测试 Go 文件，返回按 文件→行号 排序的埋点列表。
func Scan(root string) ([]Site, error) {
	files, err := goFiles(root)
	if err != nil {
		return nil, err
	}

	// 第一遍：解析全部文件，收集包级常量 + 助手形参索引。
	// Go 的常量作用域是"包"而非"文件"，而一个包的多个源文件是分散的，
	// 故必须先整体收集再回头解析引用 —— 顺序反过来会把常量写法误判成无法解析。
	fset := token.NewFileSet()
	var docs []parsedDoc
	consts := make(map[string]map[string]string)
	var helpers []helper
	for _, p := range files {
		f, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			return nil, fmt.Errorf("parse %s: %w", p, perr)
		}
		docs = append(docs, parsedDoc{path: p, dir: filepath.Dir(p), file: f, tf: fset.File(f.Pos())})
		if consts[filepath.Dir(p)] == nil {
			consts[filepath.Dir(p)] = make(map[string]string)
		}
		collectConsts(f, consts[filepath.Dir(p)])
		helpers = append(helpers, collectHelpers(f, filepath.Dir(p))...)
	}

	byName := make(map[string][]helper) // dir/name → 候选（同名方法可能分属不同接收者）
	for _, h := range helpers {
		byName[h.dir+"/"+h.name] = append(byName[h.dir+"/"+h.name], h)
	}

	var sites []Site
	for _, d := range docs {
		cs := consts[d.dir]
		sites = append(sites, scanDoc(d, cs, func(name string) []helper {
			return byName[d.dir+"/"+name]
		})...)
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].File != sites[j].File {
			return sites[i].File < sites[j].File
		}
		return sites[i].Line < sites[j].Line
	})
	return sites, nil
}

func goFiles(root string) ([]string, error) {
	var files []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		// testdata 是 fixture，不是真实埋点：把它们算进去会让矩阵凭空多出几行，
		// 而读矩阵的人无从判断这几行是"系统里有一条真实审计"还是"测试用的样本"。
		if strings.Contains(filepath.ToSlash(p), "/testdata/") {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func scanDoc(d parsedDoc, consts map[string]string, lookup func(string) []helper) []Site {
	var sites []Site

	ast.Inspect(d.file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CompositeLit:
			if !isAuditEvent(x.Type) {
				return true
			}
			action, dyn := resolveAction(fieldValue(x, "Action"), consts)
			s := Site{
				File:         filepath.ToSlash(d.path),
				Line:         d.tf.Line(x.Pos()),
				Action:       action,
				ResourceType: literalValue(fieldValue(x, "ResourceType")),
				Kind:         KindEvent,
				Start:        d.tf.Offset(x.Pos()),
				End:          d.tf.Offset(x.End()),
				DynamicName:  dyn,
			}
			sites = append(sites, s)
		case *ast.CallExpr:
			// 助手既可能是包级函数（recordPublication(...)），也可能是方法（s.record(...)）；
			// 两种写法都得认，只认前一种会漏掉整个 retention Sweeper 的埋点。
			name := calleeName(x.Fun)
			if name == "" {
				return true
			}
			var (
				action string
				dyn    string
				arg    ast.Expr
			)
			for _, cand := range lookup(name) {
				if cand.index >= len(x.Args) {
					continue
				}
				a, d := resolveAction(x.Args[cand.index], consts)
				if action == "" || (action == DynamicAction && a != DynamicAction) {
					action, dyn, arg = a, d, x.Args[cand.index]
				}
				if a != DynamicAction {
					break // 解析出真实动作名的候选优先，消除同名方法带来的歧义
				}
			}
			if arg == nil {
				return true
			}
			// 区间取**整段调用**而不仅是动作实参：调用方要在这段原文上做二级检查
			// （例如断言匿名访问的审计没把 token / publicId 一起写进去）。
			sites = append(sites, Site{
				File:        filepath.ToSlash(d.path),
				Line:        d.tf.Line(arg.Pos()),
				Action:      action,
				Kind:        KindCall,
				DynamicName: dyn,
				Start:       d.tf.Offset(x.Pos()),
				End:         d.tf.Offset(x.End()),
			})
		}
		return true
	})
	return sites
}

// resolveAction 把 Action 位置的表达式解析成字符串：字面量直接取，
// 标识符先查包级常量，其余视为无法解析并返回 DynamicAction 占位。
func resolveAction(expr ast.Expr, consts map[string]string) (action string, dynamic string) {
	if expr == nil {
		return DynamicAction, ""
	}
	switch v := expr.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			if s, err := strconv.Unquote(v.Value); err == nil && s != "" {
				return s, ""
			}
		}
	case *ast.Ident:
		if s, ok := consts[v.Name]; ok && s != "" {
			return s, ""
		}
		return DynamicAction, v.Name
	case *ast.SelectorExpr: // 跨包常量：本轮不跨包解析，留名待人工确认
		return DynamicAction, pkgConstName(v)
	}
	return DynamicAction, ""
}

// collectHelpers 找出"某个形参被原样放进 audit.Event.Action"的函数。
// 这类助手是盘点最容易漏的一层：埋点位置与动作名分处两个文件。
func collectHelpers(f *ast.File, dir string) []helper {
	var out []helper
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Type.Params == nil {
			continue
		}
		paramIndex := make(map[string]int)
		i := 0
		for _, field := range fd.Type.Params.List {
			for _, name := range field.Names {
				paramIndex[name.Name] = i
				i++
			}
			if len(field.Names) == 0 { // 匿名形参（多为接口/下划线）
				i++
			}
		}
		found := -1
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isAuditEvent(lit.Type) {
				return true
			}
			fv := fieldValue(lit, "Action")
			if id, ok := fv.(*ast.Ident); ok {
				if idx, ok := paramIndex[id.Name]; ok {
					found = idx
				}
			}
			return true
		})
		if found >= 0 {
			out = append(out, helper{dir: dir, name: fd.Name.Name, index: found})
		}
	}
	return out
}

// collectConsts 收集包级字符串常量值，用于把具名写法解析回真实动作名。
func collectConsts(f *ast.File, into map[string]string) {
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) == 0 || len(vs.Values) == 0 {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			into[vs.Names[0].Name] = v
		}
	}
}

func isAuditEvent(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "audit" && sel.Sel != nil && sel.Sel.Name == "Event"
}

// fieldValue 返回复合字面量里 key 对应的值表达式。
func fieldValue(lit *ast.CompositeLit, key string) ast.Expr {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == key {
			return kv.Value
		}
	}
	return nil
}

func literalValue(expr ast.Expr) string {
	v, ok := expr.(*ast.BasicLit)
	if !ok || v.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(v.Value)
	if err != nil {
		return ""
	}
	return s
}

func pkgConstName(sel *ast.SelectorExpr) string {
	if id, ok := sel.X.(*ast.Ident); ok {
		return id.Name + "." + sel.Sel.Name
	}
	return sel.Sel.Name
}

// calleeName 取调用的函数名：普通函数取标识符名，方法调用（s.record）取选择器名。
// 返回值用于在同一包的其他文件里找对应的助手定义。
func calleeName(fn ast.Expr) string {
	switch f := fn.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if f.Sel != nil {
			return f.Sel.Name
		}
	}
	return ""
}
