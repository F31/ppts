package validation

import (
	"strings"
	"testing"
)

// TestDefaultRulesValid 是**启动期校验的 CI 版**：内嵌规则表若有结构错误，
// mustLoadRules 会在进程起飞时 panic；这条测试让它在 CI 就暴露，而不是等部署。
func TestDefaultRulesValid(t *testing.T) {
	r := DefaultRules()
	if problems := ValidateRules(r); len(problems) > 0 {
		t.Fatalf("内嵌规则表非法：\n  %s", strings.Join(problems, "\n  "))
	}
	if strings.TrimSpace(RulesVersion()) == "" {
		t.Error("RulesVersion 为空：改规则表必须留痕（回放报告/基线要能标出判定依据的版本）")
	}
	if len(r.UnitAliasGroups) == 0 || len(r.PlainUnits) == 0 || len(r.ModelAliasGroups) == 0 {
		t.Fatalf("规则表三张子表都应有内容：unit=%d plain=%d model=%d",
			len(r.UnitAliasGroups), len(r.PlainUnits), len(r.ModelAliasGroups))
	}
}

// TestValidateRulesCatchesTableErrors 逐条验证自检能抓住**编译期看不见**的表错误。
// 这些错误的共同特征是：不报错、只是让某个实体被静默放行或误杀。
func TestValidateRulesCatchesTableErrors(t *testing.T) {
	base := func() Rules {
		return Rules{
			Version:          "test",
			UnitAliasGroups:  [][]string{{"ms", "毫秒"}},
			PlainUnits:       []string{"gb"},
			ModelAliasGroups: [][]string{{"k8s", "kubernetes"}},
		}
	}

	cases := []struct {
		name    string
		mutate  func(*Rules)
		wantSub string
	}{
		{
			name:    "版本号为空",
			mutate:  func(r *Rules) { r.Version = "  " },
			wantSub: "version 不能为空",
		},
		{
			name:    "等价组只有一个写法",
			mutate:  func(r *Rules) { r.UnitAliasGroups = [][]string{{"ms"}} },
			wantSub: "至少需要 2 个",
		},
		{
			name:    "模型等价组只有一个写法",
			mutate:  func(r *Rules) { r.ModelAliasGroups = [][]string{{"k8s"}} },
			wantSub: "至少需要 2 个",
		},
		{
			name:    "同一 token 落在两个等价组",
			mutate:  func(r *Rules) { r.UnitAliasGroups = append(r.UnitAliasGroups, []string{"毫秒", "msec"}) },
			wantSub: "同时出现在",
		},
		{
			name:    "token 同时在等价组与 plainUnits",
			mutate:  func(r *Rules) { r.PlainUnits = append(r.PlainUnits, "ms") },
			wantSub: "同时出现在",
		},
		{
			name:    "仅大小写不同的重复",
			mutate:  func(r *Rules) { r.PlainUnits = append(r.PlainUnits, "GB") },
			wantSub: "同时出现在",
		},
		{
			name:    "空 token",
			mutate:  func(r *Rules) { r.UnitAliasGroups = [][]string{{"ms", ""}} },
			wantSub: "空 token",
		},
		{
			name:    "token 含空白",
			mutate:  func(r *Rules) { r.UnitAliasGroups = [][]string{{"ms", "毫 秒"}} },
			wantSub: "含空白",
		},
		{
			name: "三张表全空",
			mutate: func(r *Rules) {
				r.UnitAliasGroups = nil
				r.PlainUnits = nil
				r.ModelAliasGroups = nil
			},
			wantSub: "门禁会退化",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := base()
			c.mutate(&r)
			problems := ValidateRules(r)
			if len(problems) == 0 {
				t.Fatalf("应当被自检拦下，却判定为合法：%+v", r)
			}
			joined := strings.Join(problems, "; ")
			if !strings.Contains(joined, c.wantSub) {
				t.Fatalf("问题描述不含 %q：%s", c.wantSub, joined)
			}
		})
	}
}

// TestValidateRulesAcceptsLegitTable 守门：合法表不能被自检误伤（否则会有人
// 为了"让测试过"而删检查）。
func TestValidateRulesAcceptsLegitTable(t *testing.T) {
	r := Rules{
		Version:          "2026-09-22.1",
		UnitAliasGroups:  [][]string{{"%", "pct"}, {"s", "sec", "秒"}},
		PlainUnits:       []string{"gb", "万元"},
		ModelAliasGroups: [][]string{{"k8s", "kubernetes"}},
	}
	if problems := ValidateRules(r); len(problems) > 0 {
		t.Fatalf("合法表被误判：%v", problems)
	}
}

func TestParseRulesRejectsMalformedJSON(t *testing.T) {
	if _, err := ParseRules([]byte("{ not json")); err == nil {
		t.Fatal("非法 JSON 应当报错")
	}
	r, err := ParseRules([]byte(`{"version":"v","unitAliasGroups":[["a","b"]]}`))
	if err != nil {
		t.Fatalf("合法 JSON: %v", err)
	}
	if r.Version != "v" || len(r.UnitAliasGroups) != 1 {
		t.Fatalf("解析结果不符：%+v", r)
	}
}

// TestRulesTableIsDataDriven 确认视图变量确实来自数据文件（而非某处又留了一份字面表）：
// 改 Rules 的副本不影响包级视图，但包级视图必须与 DefaultRules 同源。
func TestRulesTableIsDataDriven(t *testing.T) {
	def := DefaultRules()
	if len(unitAliasGroups) != len(def.UnitAliasGroups) {
		t.Fatalf("unitAliasGroups 与数据文件不同源：%d vs %d",
			len(unitAliasGroups), len(def.UnitAliasGroups))
	}
	if len(plainUnits) != len(def.PlainUnits) || len(modelAliasGroups) != len(def.ModelAliasGroups) {
		t.Fatal("plainUnits / modelAliasGroups 与数据文件不同源")
	}
}
