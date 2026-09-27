package textnorm

import (
	"testing"
)

// TestDecimalToCN 小数公共库：整数/小数/负数/前导零。
func TestDecimalToCN(t *testing.T) {
	cases := []struct{ in, want string }{
		{"13.5", "十三点五"},
		{"6.3", "六点三"},
		{"0.5", "零点五"},
		{"100", "一百"},
		{"-3.5", "负三点五"},
		{"0.75", "零点七五"},
	}
	for _, c := range cases {
		got, ok := DecimalToCN(c.in)
		if !ok || got != c.want {
			t.Errorf("DecimalToCN(%q) = %q,%v want %q,true", c.in, got, ok, c.want)
		}
	}
	if _, ok := DecimalToCN(""); ok {
		t.Error("DecimalToCN empty should be false")
	}
}

func engineWithPatterns(ps ...Pattern) *Engine {
	e := New()
	for _, p := range ps {
		e.RegisterPattern(p)
	}
	e.Build()
	return e
}

// TestPercentPattern 百分比：整百分比 + 小数百分比 + 不吞型号。
func TestPercentPattern(t *testing.T) {
	e := engineWithPatterns(PercentPattern())
	cases := []struct{ in, want string }{
		{"同比增长 6.3%", "同比增长 百分之六点三"},
		{"增长 12%", "增长 百分之十二"},
		{"RTX5090 占比 3%", "RTX5090 占比 百分之三"}, // 型号不展开，3% 展开
	}
	for _, c := range cases {
		got := e.Run(c.in, "zh-CN").Text
		if got != c.want {
			t.Errorf("percent Run(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

// TestDecimalPattern 小数：展开，不吞整数。
// 日期保护靠配套注册 date（组合约束，见 TestClassesCompose）。
func TestDecimalPattern(t *testing.T) {
	e := engineWithPatterns(DecimalPattern())
	cases := []struct{ in, want string }{
		{"误差 1.5", "误差 一点五"},
		{"2.75 倍", "二点七五 倍"},
		{"2024 年", "2024 年"}, // 整数不吞
	}
	for _, c := range cases {
		got := e.Run(c.in, "zh-CN").Text
		if got != c.want {
			t.Errorf("decimal Run(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

// TestDatePattern 日期：2002/01/28、2002-1-28、2002.01.28。
func TestDatePattern(t *testing.T) {
	e := engineWithPatterns(DatePattern())
	cases := []struct{ in, want string }{
		{"2002/01/28 上线", "二零零二年一月二十八日 上线"},
		{"2002-1-28 上线", "二零零二年一月二十八日 上线"},
		{"2002.01.28 上线", "二零零二年一月二十八日 上线"},
		{"2024 年", "2024 年"}, // 非日期不吞
	}
	for _, c := range cases {
		got := e.Run(c.in, "zh-CN").Text
		if got != c.want {
			t.Errorf("date Run(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

// TestMoneyPattern 金额：币种映射 + 万元。
func TestMoneyPattern(t *testing.T) {
	e := engineWithPatterns(MoneyPattern())
	cases := []struct{ in, want string }{
		{"售价 ¥13.5", "售价 人民币十三点五"},
		{"价值 $250", "价值 美元二百五十"},
		{"收入 13.5万元", "收入 人民币十三点五万元"}, // 尾随"元"保留（匹配到"万"）
		{"HKD500 预算", "港元五百 预算"},
	}
	for _, c := range cases {
		got := e.Run(c.in, "zh-CN").Text
		if got != c.want {
			t.Errorf("money Run(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

// TestUnitPattern 数值+单位：数字展开、单位保持。
func TestUnitPattern(t *testing.T) {
	e := engineWithPatterns(UnitPattern())
	cases := []struct{ in, want string }{
		{"重达 25kg", "重达 二十五kg"},
		{"跨度 120m", "跨度 一百二十m"},
		{"5GHz", "5GHz"}, // 型号单位（词典处理），不入单位类
	}
	for _, c := range cases {
		got := e.Run(c.in, "zh-CN").Text
		if got != c.want {
			t.Errorf("unit Run(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

// TestClassesCompose 组合场景：同段多类别叠加，互不吞字（V3.0 §2.2）。
func TestClassesCompose(t *testing.T) {
	e := engineWithPatterns(PercentPattern(), DecimalPattern(), DatePattern(), MoneyPattern(), UnitPattern())
	cases := []struct{ in, want string }{
		{"2024/03/15 增长 6.3% 营收 ¥250", "二零二四年三月十五日 增长 百分之六点三 营收 人民币二百五十"},
	}
	for _, c := range cases {
		got := e.Run(c.in, "zh-CN").Text
		if got != c.want {
			t.Errorf("compose Run(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

// TestClassesIndexMap 类别改写产出精确 IndexMap（不影响逐字高亮）。
func TestClassesIndexMap(t *testing.T) {
	e := engineWithPatterns(PercentPattern())
	res := e.Run("增长 6.3%", "zh-CN")
	if res.IndexMap == nil || res.IndexMap.Empty() {
		t.Fatal("expected IndexMap from percent rewrite")
	}
	if _, ok := res.IndexMap.MapSrcToDst(3); !ok {
		t.Fatalf("MapSrcToDst(3) not covered (got segments %v)", res.IndexMap.Segments)
	}
}
