package validation

import (
	"strings"
	"testing"
)

// 本文件锁定「插入数字按形态分流」这条策略（counts.go + rules.json 的 counts）：
//   · 概括计数（4 类场景）是模型对页面已列条目的转述，默认放行但提示；
//   · 序号（第 2 步）沿用既有裁定：不豁免；
//   · 带单位的实测数值、超过 maxValue 的计数、被改动的计数，一律拦。
//
// 每条判定都同时验"默认策略"与"显式策略"，因为策略是配置项，不是常量：
// 只测默认值会让"改配置就能绕过"这件事无人发现。

func strictCounts() Counts {
	return Counts{InsertedCounts: ModeBlock, InsertedOrdinals: ModeBlock, MaxValue: 20}
}

func laxCounts(mode string) Counts {
	return Counts{InsertedCounts: mode, InsertedOrdinals: ModeBlock, MaxValue: 20}
}

// TestNumberForm 锁定形态判定的边界：带单位=事实、序数前缀=序号、量词/后缀=计数。
func TestNumberForm(t *testing.T) {
	cases := []struct {
		text string
		want string // 第一个数字的形态
	}{
		{"容量 64GB", FormFact},
		{"提升 40%", FormFact},
		{"覆盖 4 类场景", FormCount},
		{"共 3 个步骤", FormCount},
		{"4 大关键能力", FormCount},
		{"三个维度里的 2 个", FormCount},
		{"第 2 步先解析", FormOrdinal},
		{"第2步先解析", FormOrdinal},
		{"No.3 先解析", FormOrdinal},
		{"2026 年发布", FormFact}, // 年份是事实，不是计数
	}
	for _, c := range cases {
		ents := ExtractEntities(c.text)
		var got string
		for _, e := range ents {
			if e.Kind == "number" {
				got = e.Form
				break
			}
		}
		if got != c.want {
			t.Errorf("%q 的形态 = %q，want %q（实体 %v）", c.text, got, c.want, ents)
		}
	}
}

// TestInsertedCountsFollowPolicy 是这次改动的核心：同一条文本，策略不同结论不同。
func TestInsertedCountsFollowPolicy(t *testing.T) {
	const source = "企业培训\n在线课程\n销售路演\n无障碍"
	const target = "本页覆盖 4 类场景，分别面向培训、课程、路演与无障碍。"

	// 默认（规则表 counts.insertedCounts）：放行 + 提示。
	def := CheckPreserved(source, target)
	if !def.OK() {
		t.Fatalf("默认策略应放行概括计数，却报违规：%v", FormatEntities(def.Inserted))
	}
	if !def.HasWarnings() {
		t.Fatalf("放行必须留提示，否则是无声明地放宽门禁")
	}
	if got := FormatEntities(def.Warnings); len(got) != 1 || got[0] != "number:4" {
		t.Fatalf("提示项 = %v，应为 number:4", got)
	}

	// 显式 block：回到"原文没有的数字一律拦"。
	blocked := CheckPreservedWithPolicy(source, target, strictCounts())
	if blocked.OK() {
		t.Fatalf("策略为 block 时应判违规")
	}
	if blocked.HasWarnings() {
		t.Fatalf("block 策略下不应产生提示项")
	}
}

// TestOrdinalsStayBlockedByPolicy 序号不因计数放行而顺带豁免。
func TestOrdinalsStayBlockedByPolicy(t *testing.T) {
	r := CheckPreserved("先做解析，再做渲染", "第 1 步先做解析，第 2 步再做渲染。")
	if r.OK() {
		t.Fatalf("序号默认不得豁免，却通过了：warnings=%v", FormatEntities(r.Warnings))
	}
	// 显式允许时也应放行，且同样留提示——策略是配置，行为必须随配置走。
	allow := Counts{InsertedOrdinals: ModeWarn, InsertedCounts: ModeBlock, MaxValue: 20}
	r2 := CheckPreservedWithPolicy("先做解析，再做渲染", "第 1 步先做解析，第 2 步再做渲染。", allow)
	if !r2.OK() || !r2.HasWarnings() {
		t.Fatalf("ordinals=warn 时应放行并提示：ok=%v warnings=%v", r2.OK(), FormatEntities(r2.Warnings))
	}
}

// TestFactsAreNeverExempt 形态分流不得成为事实篡改的旁路：
// 带单位的数字、超过上限的计数、被改动的计数，都不能被"计数豁免"吞掉。
func TestFactsAreNeverExempt(t *testing.T) {
	cases := []struct {
		name   string
		source string
		target string
	}{
		{"带单位的新增", "本页讲增长", "本页讲增长 40%。"},
		{"带单位的新增（容量）", "本页讲存储", "本页讲 64GB 存储。"},
		{"超过上限的计数", "解析后渲染", "解析后渲染，共 120 个步骤。"},
		{"计数被改", "共 3 个步骤", "共 4 个步骤。"},
		{"计数被删", "共 3 个步骤", "步骤很多。"},
		{"序号被改", "第 2 步先解析", "第 5 步先解析。"},
	}
	for _, c := range cases {
		r := CheckPreserved(c.source, c.target)
		if r.OK() {
			t.Errorf("[%s] 应拦下却通过了：warnings=%v", c.name, FormatEntities(r.Warnings))
		}
	}
}

// TestCountMaxValueBoundary maxValue 是配置项，边界必须严格（= 上限放行，> 上限拦）。
func TestCountMaxValueBoundary(t *testing.T) {
	policy := Counts{InsertedCounts: ModeAllow, MaxValue: 5}
	if r := CheckPreservedWithPolicy("解析后渲染", "解析后渲染，共 5 个步骤。", policy); !r.OK() {
		t.Fatalf("等于上限应放行")
	}
	if r := CheckPreservedWithPolicy("解析后渲染", "解析后渲染，共 6 个步骤。", policy); r.OK() {
		t.Fatalf("超过上限应拦下")
	}
}

// TestCountHintFollowsPolicy 提示词与校验器同源：策略变了，给模型的约束必须跟着变。
func TestCountHintFollowsPolicy(t *testing.T) {
	hint := FormatCountHint()
	if !strings.Contains(hint, "②") {
		t.Fatalf("提示词缺少编号：%q", hint)
	}
	// 默认 counts=warn：应明确"可以概括计数"，而不是继续写"不要计数"。
	if counts.InsertedCounts == ModeWarn || counts.InsertedCounts == ModeAllow {
		if strings.Contains(hint, "不要自行编号或计数") {
			t.Fatalf("策略已放行概括计数，提示词却仍禁止：%q", hint)
		}
		if !strings.Contains(hint, "计数") {
			t.Fatalf("提示词未说明计数规则：%q", hint)
		}
	}
}
