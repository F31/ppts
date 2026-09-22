// 规则表的数据化与自检（M15，2026-09-22）。
//
// 此前等价表是散在 entities.go 里的 Go 源码常量：加一条等价写法要改 Go 代码、重新编译部署，
// 而且**表本身的错误没有任何检查**——例如同一个 token 落在两个组里，归一化结果会静默地
// 取决于声明顺序（`unitCanonical` 后写覆盖先写），编译期与运行期都不会报错。
//
// 现在表搬到 rules.json，本文件负责：
//  1. 内嵌加载（go:embed，无 IO、无 DB 依赖——安全门禁必须确定性，不能因数据库不可用而变行为）；
//  2. **启动期校验**（ValidateRules）：表结构错误在进程起飞前就炸掉，而不是等某个租户的
//     页面被静默放行；
//  3. 暴露 RulesVersion 供报告/基线留痕（"这份判定是在哪版规则下得到的"）。
//
// 为什么不放进数据库/租户配置：等价表是安全门禁的底线，若可被配置改动，"同一份文本在两处
// 结论不同"的缺陷将不可复现。需要租户可配的是**领域型号别名**那一层，且必须只增不减——
// 那属于后续里程碑，不在本文件范围内。
package validation

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed rules.json
var rulesJSON []byte

// Rules 是等价表的声明形式。
type Rules struct {
	Note string `json:"_note,omitempty"`
	// Version 供报告与基线留痕；改表必须同时改它（有测试断言非空）。
	Version string `json:"version"`
	// UnitAliasGroups：同一物理量的不同写法，组内首个为规范写法。
	UnitAliasGroups [][]string `json:"unitAliasGroups"`
	// PlainUnits：只有一种写法的单位（中文技术文案里直接沿用英文缩写）。
	PlainUnits []string `json:"plainUnits"`
	// ModelAliasGroups：缩写与其通用全称的语言级同义，组内首个为规范写法。
	ModelAliasGroups [][]string `json:"modelAliasGroups"`
}

// defaultRules 是进程内唯一的一份规则表；加载或校验失败直接 panic——
// 内嵌数据损坏意味着二进制作废，静默降级会让安全门禁在无人察觉的情况下失效。
var defaultRules = mustLoadRules()

// 三个视图变量在 rules.go 里赋值（表本身在 rules.json）。等价关系只声明一次，
// 提示词侧通过 FormatEquivalenceHint 取同一张表，避免"指令说可以这样写、校验器却拒绝"。
var (
	unitAliasGroups  = defaultRules.UnitAliasGroups
	plainUnits       = defaultRules.PlainUnits
	modelAliasGroups = defaultRules.ModelAliasGroups
)

// RulesVersion 返回当前规则表版本，供回放报告/基线留痕。
func RulesVersion() string { return defaultRules.Version }

// DefaultRules 返回内嵌的规则表（测试与工具用；返回值只读）。
func DefaultRules() Rules { return defaultRules }

// ParseRules 解析规则表声明；结构错误在 ValidateRules 里报。
func ParseRules(data []byte) (Rules, error) {
	var r Rules
	if err := json.Unmarshal(data, &r); err != nil {
		return Rules{}, fmt.Errorf("解析规则表: %w", err)
	}
	return r, nil
}

func mustLoadRules() Rules {
	r, err := ParseRules(rulesJSON)
	if err != nil {
		panic("validation: 内嵌规则表损坏: " + err.Error())
	}
	if problems := ValidateRules(r); len(problems) > 0 {
		panic("validation: 内嵌规则表非法: " + strings.Join(problems, "; "))
	}
	return r
}

// ValidateRules 检查规则表的**结构性错误**，返回人类可读的问题列表（空 = 合法）。
//
// 覆盖的都是"编译期看不见、运行期不报错、只表现为某个实体被静默放行/误杀"的错误：
//   - 空 token / token 含空白（空白在正则里会被 QuoteMeta 后仍匹配不上，归一化悄悄失效）
//   - 等价组只有 1 个写法（不是等价，是打字错误）
//   - 同一 token 出现在两个等价组，或同时出现在等价组与 plainUnits
//     → 归一化结果取决于声明顺序（后写覆盖先写），换个顺序行为就变，却没有报错
//   - 单位集合里存在仅大小写不同的重复（大小写被折叠后同样会互相覆盖）
func ValidateRules(r Rules) []string {
	var problems []string
	if strings.TrimSpace(r.Version) == "" {
		problems = append(problems, "version 不能为空（改表必须留痕）")
	}
	if len(r.UnitAliasGroups) == 0 && len(r.PlainUnits) == 0 && len(r.ModelAliasGroups) == 0 {
		problems = append(problems, "三张表全空：门禁会退化成几乎不校验")
	}

	check := func(ns string, owner map[string]string, where, token string) {
		t := strings.ToLower(strings.TrimSpace(token))
		switch {
		case t == "":
			problems = append(problems, where+" 含空 token")
			return
		case strings.ContainsAny(token, " \t\r\n"):
			problems = append(problems, where+" 的 token 含空白："+fmt.Sprintf("%q", token))
			return
		}
		if prev, dup := owner[t]; dup {
			problems = append(problems, fmt.Sprintf(
				"%s 里 token %q 同时出现在 %s 与 %s：归一化结果会取决于声明顺序（后写覆盖先写）",
				ns, t, prev, where))
			return
		}
		owner[t] = where
	}

	units := map[string]string{}
	for i, g := range r.UnitAliasGroups {
		where := fmt.Sprintf("unitAliasGroups[%d]", i)
		if len(g) < 2 {
			problems = append(problems, fmt.Sprintf("%s 只有 %d 个写法：等价组至少需要 2 个", where, len(g)))
		}
		for _, t := range g {
			check("单位", units, where, t)
		}
	}
	for _, t := range r.PlainUnits {
		check("单位", units, "plainUnits", t)
	}

	models := map[string]string{}
	for i, g := range r.ModelAliasGroups {
		where := fmt.Sprintf("modelAliasGroups[%d]", i)
		if len(g) < 2 {
			problems = append(problems, fmt.Sprintf("%s 只有 %d 个写法：等价组至少需要 2 个", where, len(g)))
		}
		for _, t := range g {
			check("型号", models, where, t)
		}
	}
	return problems
}
