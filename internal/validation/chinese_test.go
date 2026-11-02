package validation

import (
	"reflect"
	"testing"
)

func TestParseChineseInteger(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"三", 3},
		{"两", 2},
		{"十", 10},
		{"十二", 12},
		{"二十", 20},
		{"二十三", 23},
		{"九十九", 99},
		{"一百零三", 103},
		{"两百", 200},
		{"两千", 2000},
		{"十二万", 120000},
		{"一万二千", 12000},
	} {
		got, ok := parseChineseInteger(tc.in)
		if !ok {
			t.Fatalf("%q 解析失败，期望 %d", tc.in, tc.want)
		}
		if got != tc.want {
			t.Fatalf("%q = %d，期望 %d", tc.in, got, tc.want)
		}
	}
	for _, in := range []string{"", "零", "两三", "十百"} {
		if v, ok := parseChineseInteger(in); ok {
			t.Fatalf("%q 不该解析成功（得到 %d）：模糊/错写的数词必须退回不解析", in, v)
		}
	}
}

func TestChineseNumeralEntities(t *testing.T) {
	for _, tc := range []struct {
		text string
		want []string
	}{
		{"共三个要点", []string{"number:3"}},
		{"两个方面", []string{"number:2"}},
		{"三个方面", []string{"number:3"}},
		// 序数语境：量词/后缀照常参与，即使该词在排除表里（「步」）。
		{"第三步再讲", []string{"number:3"}},
		{"第一章讲背景", []string{"number:1"}},
		// 固定说法不得造实体：这些是"看见数词就解析"会误伤的地方。
		{"统一口径，一定照办", []string{}},
		{"一方面要快，另一方面要稳", []string{}},
		{"进一步说明", []string{}},
	} {
		got := FormatEntities(ExtractEntities(tc.text))
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%q 实体 = %v，期望 %v", tc.text, got, tc.want)
		}
	}
}

func TestChineseNumeralPolicy(t *testing.T) {
	// 同一事实的两种写法：必须放行（此前是必降级的误杀）。
	if r := CheckPreserved("共 3 个要点", "共三个要点。"); !r.OK() {
		t.Fatalf("中文数字与阿拉伯数字应等价，实际 missing=%v inserted=%v",
			FormatEntities(r.Missing), FormatEntities(r.Inserted))
	}
	// 改数：三个 → 四个依然拦得住（不能因为换了写法就绕开比对）。
	if r := CheckPreserved("共 3 个要点", "共四个要点。"); r.OK() {
		t.Fatal("「3 个」被写成「四个」必须判违规")
	}
	// 删除：源里的计数在目标里消失，仍按缺失拦。
	if r := CheckPreserved("共三个要点", "本页讲三件事。"); r.OK() {
		t.Fatal("源里的「三个」在目标里没有对应物，必须判缺失")
	}
	// 序数：第三步 → 第 3 步 等价。
	if r := CheckPreserved("第 3 步再讲", "第三步再讲"); !r.OK() {
		t.Fatalf("「第三步」与「第 3 步」应等价，实际 missing=%v inserted=%v",
			FormatEntities(r.Missing), FormatEntities(r.Inserted))
	}
}

// 开关必须是**真的**关掉：线上回滚不依赖改代码。
func TestChineseNumeralsCanBeDisabled(t *testing.T) {
	if got := FormatEntities(extractEntities("共三个要点", false)); len(got) != 0 {
		t.Fatalf("关闭后不该解析中文数词，实际 %v", got)
	}
	if got := FormatEntities(extractEntities("共三个要点", true)); !reflect.DeepEqual(got, []string{"number:3"}) {
		t.Fatalf("开启后应解析为 number:3，实际 %v", got)
	}
}

// 实体值归一成阿拉伯数字，但原文写法要留着——提示按它定位，否则指错位置。
func TestChineseNumeralSurface(t *testing.T) {
	es := ExtractEntities("共三个要点")
	if len(es) != 1 || es[0].Text != "3" || es[0].Surface != "三个" {
		t.Fatalf("实体 = %+v，期望 Text=3 / Surface=三个", es)
	}
}

func TestValidateChineseNumerals(t *testing.T) {
	// 两种错误分开验：同一份表里放两项时，带空白的那项进 seen 后不会被判重复。
	for _, tails := range [][]string{{"步 "}, {"步", "步"}} {
		r := DefaultRules()
		r.ChineseNumerals.ExcludeTails = tails
		if problems := ValidateRules(r); len(problems) == 0 {
			t.Fatalf("排除项 %v 应报错（空白/重复会让排除静默失效）", tails)
		}
	}
}
