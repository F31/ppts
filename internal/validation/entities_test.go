package validation

import (
	"reflect"
	"strings"
	"testing"
)

func TestCheckPreservedOK(t *testing.T) {
	report := CheckPreserved("吞吐提升 23.5%，延迟 12ms，A100 在 2026-09-13 上线。",
		"这套方案把吞吐提升 23.5%，延迟控制在 12ms，并将在 2026年09月13日支持 A100。")
	if !report.OK() {
		t.Fatalf("report = %+v missing=%v inserted=%v", report, FormatEntities(report.Missing), FormatEntities(report.Inserted))
	}
}

func TestCheckPreservedFlagsMissingAndInserted(t *testing.T) {
	report := CheckPreserved("端口升级到 PCIe 5.0，容量 80GB。", "端口升级到 PCIe 6.0，容量 96GB。")
	if report.OK() {
		t.Fatalf("expected violations")
	}
	// 期望值随 M11 归一化调整：小数尾随零归一（5.0 → 5、6.0 → 6）。
	// 关键不变量未变——被改动的数量仍被识别为「原值缺失 + 新值新增」。
	if !reflect.DeepEqual(FormatEntities(report.Missing), []string{"number:5", "number:80gb"}) {
		t.Fatalf("missing = %v", FormatEntities(report.Missing))
	}
	if !reflect.DeepEqual(FormatEntities(report.Inserted), []string{"number:6", "number:96gb"}) {
		t.Fatalf("inserted = %v", FormatEntities(report.Inserted))
	}
}

// TestEquivalentSpellingsAreAccepted 是 M11 的正向用例：同一实体的合法异写必须通过。
// 每一条都对应一个实测过的误杀（10 个用例中 8 例属此类），此前都会导致整页回退原文。
func TestEquivalentSpellingsAreAccepted(t *testing.T) {
	cases := []struct {
		name   string
		source string
		target string
	}{
		{
			name:   "中文日期加空格",
			source: "2026-09-13 发布",
			target: "计划在 2026 年 9 月 13 日发布。",
		},
		{
			name:   "日期用斜杠与中文年月日混写",
			source: "2026/09/13 上线",
			target: "2026年9月13日正式上线。",
		},
		{
			name:   "千分位与小数尾随零",
			source: "覆盖 1,000 家企业，增长 3.0 倍",
			target: "覆盖 1000 家企业，增长 3 倍。",
		},
		{
			name:   "英文单位缩写改中文量词",
			source: "5 min / 3x / 2h / 12ms",
			target: "耗时 5 分钟，提速 3 倍，续航 2 小时，延迟 12 毫秒。",
		},
		{
			name:   "单位与数字之间有空格",
			source: "容量 80GB，延迟 12ms",
			target: "容量 80 GB，延迟 12 ms。",
		},
		{
			name:   "百分比写法混用",
			source: "提升 23.5%",
			target: "提升 23.5 pct。",
		},
		{
			name:   "全角数字",
			source: "共 １２ 页",
			target: "共 12 页。",
		},
		{
			name:   "日期内部数字不再被拆成裸数字",
			source: "2026-09-13 发布共 3 项",
			target: "2026 年 9 月 13 日发布，共 3 项。",
		},
		{
			name:   "已登记等价的型号缩写与全称",
			source: "k8s 集群共 2 个",
			target: "Kubernetes 集群共 2 个。",
		},
		{
			name:   "计数前缀 x8 与裸数量",
			source: "A100 x8",
			target: "8 张 A100 显卡。",
		},
	}
	for _, c := range cases {
		if r := CheckPreserved(c.source, c.target); !r.OK() {
			t.Errorf("[%s] 应通过却报违规：missing=%v inserted=%v",
				c.name, FormatEntities(r.Missing), FormatEntities(r.Inserted))
		}
	}
}

// TestRealViolationsStillBlocked 是 M11 的**守门用例**：归一化不得把真实违规放过去。
// 这些用例的作用是防止后续「提高通过率」的改动顺手放松门禁。
func TestRealViolationsStillBlocked(t *testing.T) {
	cases := []struct {
		name   string
		source string
		target string
	}{
		{name: "数量被改", source: "容量 80GB", target: "容量 96GB。"},
		{name: "数量被删", source: "得分 98 分", target: "得分很高。"},
		{name: "日期被改", source: "2026-09-13 发布", target: "2026-09-14 发布。"},
		{name: "单位被改", source: "延迟 12ms", target: "延迟 12s。"},
		{name: "型号被改", source: "使用 A100 卡", target: "使用 A800 卡。"},
		{name: "计数前缀被改", source: "A100 x8", target: "A100 x4。"},
		{name: "x86 与 x64 归一后仍不同", source: "x86 架构", target: "x64 架构。"},
		{name: "原文无数字时模型自行编号", source: "先做解析\n再做渲染", target: "第一步先做解析，第 2 步再做渲染。"},
		{name: "原文无数字时模型自行计数", source: "解析后渲染", target: "解析后渲染，共 3 个步骤。"},
	}
	for _, c := range cases {
		r := CheckPreserved(c.source, c.target)
		if r.OK() {
			t.Errorf("[%s] 应拦下却通过了：source=%v target=%v",
				c.name, FormatEntities(r.Source), FormatEntities(ExtractEntities(c.target)))
		}
	}
}

// TestOrdinalsAreNotExempt 显式锁定产品裁定：序号不豁免。
// 「原文没有的数字一律不许新增」是有意保留的门禁——若要放开，必须连同本用例一起改，
// 不能靠某次重构悄悄生效。
func TestOrdinalsAreNotExempt(t *testing.T) {
	if r := CheckPreserved("先做解析", "第 2 步先做解析。"); r.OK() {
		t.Fatalf("序号不得被豁免，却通过了")
	}
	hint := FormatEquivalenceHint()
	if strings.Contains(hint, "序号") || strings.Contains(hint, "第") {
		t.Fatalf("等价写法说明不得暗示序号可用：%s", hint)
	}
}

// TestEquivalenceHintCoversAllGroups 是「单一来源」的守门用例：
// 等价表新增了等价对、提示词却没告诉模型，就会出现「模型照指令写、校验器仍拒绝」的假失败。
func TestEquivalenceHintCoversAllGroups(t *testing.T) {
	hint := FormatEquivalenceHint()
	for _, g := range unitAliasGroups {
		if len(g) < 2 {
			continue
		}
		if !strings.Contains(hint, strings.Join(g, "/")) {
			t.Errorf("单位等价组 %v 未出现在提示词说明里：%s", g, hint)
		}
	}
	for _, g := range modelAliasGroups {
		if len(g) < 2 {
			continue
		}
		if !strings.Contains(hint, strings.Join(g, "/")) {
			t.Errorf("型号等价组 %v 未出现在提示词说明里：%s", g, hint)
		}
	}
}

// TestUnitCanonicalIsSingleValued 保证单位表本身无冲突：同一个写法不得映射到两个规范写法。
func TestUnitCanonicalIsSingleValued(t *testing.T) {
	seen := map[string]string{}
	for _, g := range unitAliasGroups {
		for _, u := range g {
			l := strings.ToLower(u)
			if prev, ok := seen[l]; ok && prev != strings.ToLower(g[0]) {
				t.Errorf("单位 %q 同时属于两个等价组：%q 与 %q", u, prev, g[0])
			}
			seen[l] = strings.ToLower(g[0])
		}
	}
	// 长单位必须排得下：'s' 不能抢先咬住 'sec' 的首字母。
	if got := canonicalUnit("sec"); got != "s" {
		t.Errorf("canonicalUnit(sec) = %q, want s", got)
	}
	if got := canonicalUnit(" ms "); got != "ms" {
		t.Errorf("canonicalUnit( ms ) = %q, want ms", got)
	}
}

// TestExtractEntitiesEdgeCases 覆盖空输入与「数字紧跟字母」的既有行为，防止归一化改坏边界。
func TestExtractEntitiesEdgeCases(t *testing.T) {
	if got := ExtractEntities(""); len(got) != 0 {
		t.Fatalf("空文本应无实体，得到 %v", FormatEntities(got))
	}
	if got := ExtractEntities("   \n\t "); len(got) != 0 {
		t.Fatalf("纯空白应无实体，得到 %v", FormatEntities(got))
	}
	if got := ExtractEntities("解析后渲染。"); len(got) != 0 {
		t.Fatalf("无数字文本应无实体，得到 %v", FormatEntities(got))
	}
	// 字母紧跟数字（5g / 3d / 4K）不是「数量+单位」，也不是型号（modelRe 要求字母打头）。
	for _, s := range []string{"5g 网络", "3d 渲染", "4K 视频"} {
		if got := FormatEntities(ExtractEntities(s)); len(got) != 0 {
			t.Errorf("%q 应无实体，得到 %v", s, got)
		}
	}
	// 单位被更长单词截断时退化为裸数量，而不是把 ms 当成单位。
	if got := FormatEntities(ExtractEntities("12 msec")); !reflect.DeepEqual(got, []string{"number:12"}) {
		t.Errorf("12 msec → %v, want [number:12]", got)
	}
	// 一个日期只算一个日期实体，不再额外产出裸数字。
	if got := FormatEntities(ExtractEntities("2026-09-13")); !reflect.DeepEqual(got, []string{"date:2026-9-13"}) {
		t.Errorf("2026-09-13 → %v, want [date:2026-9-13]", got)
	}
	// 单独的年份仍按数量处理（与「2026 年」同键）。
	if got := FormatEntities(ExtractEntities("2026 年发布")); !reflect.DeepEqual(got, []string{"number:2026"}) {
		t.Errorf("2026 年发布 → %v, want [number:2026]", got)
	}
	// 计数前缀 x/× 折成裸数量，不再被当作型号。
	if got := FormatEntities(ExtractEntities("x86")); !reflect.DeepEqual(got, []string{"number:86"}) {
		t.Errorf("x86 → %v, want [number:86]", got)
	}
	if got := FormatEntities(ExtractEntities("×4")); !reflect.DeepEqual(got, []string{"number:4"}) {
		t.Errorf("×4 → %v, want [number:4]", got)
	}
	// 数字紧跟的 x 是单位（3x），token 内部的 x 不折叠（MX8）。
	if got := FormatEntities(ExtractEntities("3x")); !reflect.DeepEqual(got, []string{"number:3x"}) {
		t.Errorf("3x → %v, want [number:3x]", got)
	}
}
