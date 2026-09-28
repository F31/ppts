package textnorm

import (
	"fmt"
	"sort"
)

// Built-in profile names (V3.1 §7.3 configuration combinations).
const (
	ProfileConservative = "zh.conservative"
	ProfileDateDecimal  = "zh.date-decimal"
	ProfileDatePreserve = "zh.date-preserve"
	ProfileDictDemo     = "zh.dict-demo"
)

// profileVersions 标记内置配置组合的组合版本（V3.1 §13.1 第二标识：规则/配置组合版本）。
// 当"同名 profile 的行为"变化时递增；模块主版本不变。模块版本变化不自动升级组合版本。
var profileVersions = map[string]int{
	ProfileConservative: 1,
	ProfileDateDecimal:  1,
	ProfileDatePreserve: 1,
	ProfileDictDemo:     1,
}

// ProfileInfo 描述一个内置配置组合。
type ProfileInfo struct {
	Name    string
	Version int
}

// Profiles 返回支持的配置组合清单（按名称排序），供发布产物/行为差异报告使用（§13.4）。
func Profiles() []ProfileInfo {
	names := []string{
		ProfileConservative, ProfileDateDecimal, ProfileDatePreserve, ProfileDictDemo,
	}
	sort.Strings(names)
	out := make([]ProfileInfo, 0, len(names))
	for _, n := range names {
		out = append(out, ProfileInfo{Name: n, Version: profileVersions[n]})
	}
	return out
}

// ProfileVersion 返回配置组合的版本号；未知名称返回 error。
func ProfileVersion(name string) (int, error) {
	if v, ok := profileVersions[name]; ok {
		return v, nil
	}
	return 0, fmt.Errorf("textnorm: unknown profile %q", name)
}

// ProfileRules returns the candidate rules for a built-in profile name.
// Consumers may still build their own Config; these combinations are the
// versioned defaults the component ships and tests against.
func ProfileRules(name string) ([]CandidateRule, error) {
	switch name {
	case ProfileConservative:
		return []CandidateRule{
			ZHVersionPreserveRule(),
			TechnicalModelRule(),
			ZHDateRule(ModeConvert),
			ZHPercentRule(),
			ZHUnitRule(),
			ZHDecimalRule(),
		}, nil
	case ProfileDateDecimal:
		return []CandidateRule{ZHDateRule(ModeConvert), ZHDecimalRule()}, nil
	case ProfileDatePreserve:
		return []CandidateRule{ZHDateRule(ModePreserve), TechnicalModelRule(), ZHDecimalRule()}, nil
	case ProfileDictDemo:
		return []CandidateRule{
			LiteralDictRule("dict", 10, []LiteralEntry{
				{Match: "Model3", Replace: "Model三", Whole: true},
				{Match: "CUDA", Replace: "库达", Whole: true},
			}),
			ZHDecimalRule(),
		}, nil
	}
	return nil, fmt.Errorf("textnorm: unknown profile %q", name)
}

// CompileProfile compiles a built-in profile and stamps its name/version into
// Result.Manifest. It is the preferred entry point for consumers that want a
// stable behavior contract instead of assembling rules manually.
func CompileProfile(name string, opts ...func(*Config)) (*Engine, error) {
	rules, err := ProfileRules(name)
	if err != nil {
		return nil, err
	}
	version, err := ProfileVersion(name)
	if err != nil {
		return nil, err
	}
	cfg := Config{Rules: rules, Profile: name, ProfileVersion: version}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return Compile(cfg)
}
