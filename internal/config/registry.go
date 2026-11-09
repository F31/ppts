package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

//go:generate go run ./gencatalog

// Kind 是登记表里的取值类型。它描述这个键的语义，不强制调用方用同名 getter：
// 少数键要区分「非法」和「缺省」（灰度百分比、文本规范化缓存 TTL），调用方会用
// String 自己解析，并在非法时打日志。门禁只要求键已登记。
type Kind string

const (
	KindString   Kind = "string"
	KindBool     Kind = "bool"
	KindInt      Kind = "int"
	KindInt64    Kind = "int64"
	KindFloat    Kind = "float"
	KindDuration Kind = "duration"
	KindList     Kind = "list"
)

// Spec 是一个 PPTS_* 键的登记项。
//
// DefaultText 是进程在变量未设置时实际采用的值（env 语法）。空串表示缺省就是空，
// 或者缺省由代码按上下文计算（见 Note）。安装模板可以写另一个值：单机安装要能
// 开机（例如 TTS 用 fake），代码缺省则偏向「没配就不要静默做危险的事」。
type Spec struct {
	Key         string
	Kind        Kind
	Category    string
	DefaultText string
	Note        string
	Secret      bool
	// Install 为真时写入 packaging/config.env.example。
	Install bool
	// Example 非空，或 ExampleSet 为真时，安装模板用 Example 而不是 DefaultText。
	Example    string
	ExampleSet bool
}

var byKey map[string]*Spec

func init() {
	byKey = make(map[string]*Spec, len(catalog))
	for i := range catalog {
		s := &catalog[i]
		if s.Key == "" || !strings.HasPrefix(s.Key, "PPTS_") {
			panic("config: catalog entry missing PPTS_ key")
		}
		if s.Kind == "" || s.Category == "" || s.Note == "" {
			panic("config: incomplete catalog entry " + s.Key)
		}
		if _, ok := byKey[s.Key]; ok {
			panic("config: duplicate catalog entry " + s.Key)
		}
		byKey[s.Key] = s
	}
}

// Lookup 返回已登记的键。
func Lookup(key string) (Spec, bool) {
	s, ok := byKey[key]
	if !ok {
		return Spec{}, false
	}
	return *s, true
}

// Keys 返回全部已登记键，按分类顺序再按键名排序。
func Keys() []Spec {
	out := make([]Spec, len(catalog))
	copy(out, catalog)
	order := map[string]int{}
	for i, c := range categoryOrder {
		order[c] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		oi, oj := order[out[i].Category], order[out[j].Category]
		if _, ok := order[out[i].Category]; !ok {
			oi = len(order) + 1
		}
		if _, ok := order[out[j].Category]; !ok {
			oj = len(order) + 1
		}
		if oi != oj {
			return oi < oj
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// Unknown 返回当前环境里出现、但登记表没有的 PPTS_* 键名。不返回取值。
func Unknown() []string {
	var out []string
	for _, env := range os.Environ() {
		key, _, ok := strings.Cut(env, "=")
		if !ok || !strings.HasPrefix(key, "PPTS_") {
			continue
		}
		if _, known := byKey[key]; !known {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// WarnUnknown 把未登记的 PPTS_* 键打到日志。拼写错误和已删除的键会在启动时被看见，
// 而不是静默无效。日志只含键名。
func WarnUnknown(printf func(string, ...any)) {
	for _, key := range Unknown() {
		printf("config: 未登记的环境变量 %s 会被忽略（拼写错误或已删除的键）", key)
	}
}

func (s Spec) installValue() string {
	if s.ExampleSet || s.Example != "" {
		return s.Example
	}
	return s.DefaultText
}

// InstallExample 是 packaging/config.env.example 的正文。
//
// 这是 PostgreSQL 单机安装模板，不是「一个变量都不设」的 sqlite 开箱缺省。
// 代码缺省写在每条注释的「缺省」里。install.sh 会改写数据库连接串和四个密钥行。
func InstallExample() string {
	var b strings.Builder
	b.WriteString("# ppts 单机配置（由 install.sh 生成到 /etc/ppts/config.env，两个服务共用）\n")
	b.WriteString("# 语法：KEY=VALUE。systemd EnvironmentFile 逐行加载，不要加引号，不要写 export。\n")
	b.WriteString("#\n")
	b.WriteString("# 未出现在本文件里的键使用代码缺省。全表（含测试与工具键）见 packaging/config.md。\n")
	b.WriteString("# 本文件是 PostgreSQL 安装模板。什么都不设时进程走 sqlite：数据在 ~/.ppts/ppts.db，\n")
	b.WriteString("# 无登录、动画默认开、配音必须另配供应商或模型网关，否则 worker 拒绝启动。\n")
	b.WriteString("# 布尔只认 1/true/yes/on 与 0/false/no/off。时长用 Go 语法（30s、5m、720h）。\n")
	b.WriteString("# 间隔与超时写 0 或负数会退回缺省，不会关闭功能；要关用对应的布尔开关。\n")
	last := ""
	for _, s := range Keys() {
		if !s.Install {
			continue
		}
		if s.Category != last {
			title := categoryTitle[s.Category]
			if title == "" {
				title = s.Category
			}
			fmt.Fprintf(&b, "\n# ─── %s ───\n", title)
			last = s.Category
		}
		fmt.Fprintf(&b, "# %s\n", s.Note)
		def := s.DefaultText
		if def == "" {
			def = "（空）"
		}
		if s.installValue() != s.DefaultText {
			fmt.Fprintf(&b, "# 缺省：%s。本模板：%s\n", def, display(s.installValue()))
		} else {
			fmt.Fprintf(&b, "# 缺省：%s\n", def)
		}
		fmt.Fprintf(&b, "%s=%s\n", s.Key, s.installValue())
	}
	return b.String()
}

func display(v string) string {
	if v == "" {
		return "（空）"
	}
	return v
}

// CatalogMarkdown 是给人和代码评审看的全表，含不进安装模板的测试键与工具键。
func CatalogMarkdown() string {
	var b strings.Builder
	b.WriteString("# ppts 配置清单\n\n")
	b.WriteString("本文件由 `go generate ./internal/config/` 从登记表生成，不要手改。\n\n")
	b.WriteString("未设置、空串、纯空白都视为没配，使用「缺省」列。非法值同样退回缺省，不取零值。\n")
	b.WriteString("布尔只认 `1/true/yes/on` 与 `0/false/no/off`。时长只认 Go 语法（`90s`、`5m`），0 和负数退回缺省。\n\n")
	b.WriteString("两套缺省不要混：\n\n")
	b.WriteString("- **代码缺省**是进程什么都不设时的行为，偏向安全：sqlite、开发头关闭、指标拒绝匿名、配音未配置则 worker 不起。\n")
	b.WriteString("- **安装模板**（`config.env.example`）是 PostgreSQL 单机安装，密钥写 `CHANGE_ME`（install.sh 会换成随机值），TTS 用 `fake` 以便不接供应商也能把服务拉起来。正式配音要改成模型网关或 `siliconflow`。\n\n")
	b.WriteString("解析器版本不是环境变量。修订记录里的版本来自 `go.mod` 的 `github.com/F31/go-pptx/v2`，避免报告里的版本和实际链接的库不一致。\n\n")
	b.WriteString("| 键 | 类型 | 缺省 | 安装模板 | 说明 |\n|---|---|---|---|---|\n")
	for _, s := range Keys() {
		def := s.DefaultText
		if def == "" {
			def = "（空）"
		}
		inst := "—"
		if s.Install {
			inst = "`" + strings.ReplaceAll(s.installValue(), "|", "\\|") + "`"
			if s.installValue() == "" {
				inst = "（空）"
			}
		}
		note := strings.ReplaceAll(s.Note, "|", "\\|")
		fmt.Fprintf(&b, "| `%s` | %s | `%s` | %s | %s |\n", s.Key, s.Kind, strings.ReplaceAll(def, "|", "\\|"), inst, note)
	}
	b.WriteString("\n")
	return b.String()
}
