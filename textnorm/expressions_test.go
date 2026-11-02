package textnorm

import (
	"context"
	"testing"
)

func TestExpressionRules(t *testing.T) {
	e, err := Compile(Config{Rules: []CandidateRule{ZHFractionRule(), ZHTimeRule(), ZHOrdinalRule(), ZHEnglishMoneyRule(), ZHMoneyRule(), ZHDecimalRule(), ZHNumberRule(NumberModeQuantity)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		text, want string
		semantic   SemanticKind
	}{
		{"1/3", "三分之一", SemanticFraction},
		{"12/100", "一百分之十二", SemanticFraction},
		{"1/0", "1/0", SemanticFraction},
		{"10:30", "十点三十分", SemanticTime},
		{"00:00", "零点整", SemanticTime},
		{"23:59:05", "二十三点五十九分五秒", SemanticTime},
		{"24:00", "24:00", SemanticTime},
		{"10:60", "10:60", SemanticTime},
		{"10:30:20:10", "10:30:20:10", SemanticTime},
		{"第12章", "第十二章", ""},
		{"第 12 章", "第十二章", ""},
		{"No.3 要点", "第三 要点", ""},
		{"no.4 版", "第四 版", ""},
		{"No. 5 章", "第五 章", ""},
		{"#2 问题", "第二 问题", ""},
		{"第3 ABC", "第三 ABC", ""},
		{"USD 1.5 million", "美元一百五十万", ""},
		{"$1.25 billion", "美元十二亿五千万", ""},
		{"A$500K", "澳元五十万", ""},
		{"HKD -2M", "港元负二百万", ""},
		{"USD 0.0000001 million", "美元零点一", ""},
		{"USD 1000 billion", "USD 1000 billion", ""},
		{"型号500K", "型号500K", ""},
	} {
		t.Run(tc.text, func(t *testing.T) {
			req := Request{Text: tc.text}
			if tc.semantic != "" {
				req.Hints = []Hint{{Range: Range{Start: 0, End: len([]rune(tc.text))}, Kind: HintSemantic, Semantic: tc.semantic}}
			}
			res, err := e.Normalize(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if res.Text != tc.want {
				t.Fatalf("text = %q, want %q", res.Text, tc.want)
			}
		})
	}
}

// TestOrdinalRuleGuards 只装 ordinal 规则，断言「哪些标记不算序数」——
// 与完整规则集混跑时，数字规则会把被拒的数字读掉，掩盖真正的守卫行为。
func TestOrdinalRuleGuards(t *testing.T) {
	e, err := Compile(Config{Rules: []CandidateRule{ZHOrdinalRule()}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ text, want string }{
		{"#2024", "#2024"},     // 话题标签/年份，超过两位不按序号读
		{"C#2", "C#2"},         // ASCII 左边界：C# 语言名里的 # 不是序号
		{"piano.3", "piano.3"}, // 不是 no. 前缀（左边界被字母占用）
		{"No.3", "第三"},         // 基本形态
		{"# 2", "第二"},          // 标记与数字间允许空格
		{"第 3 章", "第三章"},       // 空格被并入候选，不留「第三 章」
		{"第3 ABC", "第三 ABC"},   // 后面不是汉字，保留中英间隔
		{"第x章", "第x章"},         // 没有数字，不是候选
	} {
		res, err := e.Normalize(context.Background(), Request{Text: tc.text})
		if err != nil {
			t.Fatalf("%s: %v", tc.text, err)
		}
		if res.Text != tc.want {
			t.Errorf("%s: text = %q, want %q", tc.text, res.Text, tc.want)
		}
	}
}

func TestAmbiguousExpressionsRequireHints(t *testing.T) {
	e, err := Compile(Config{Rules: []CandidateRule{ZHFractionRule(), ZHTimeRule()}})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"1/3", "10:30", "2024/01/28"} {
		res, err := e.Normalize(context.Background(), Request{Text: text})
		if err != nil {
			t.Fatal(err)
		}
		if res.Text != text || len(res.Edits) != 0 {
			t.Errorf("ambiguous input changed: %q", res.Text)
		}
	}
	res, err := e.Normalize(context.Background(), Request{Text: "10:30", Hints: []Hint{{Range: Range{Start: 0, End: 1}, Kind: HintSemantic, Semantic: SemanticTime}}})
	if err != nil || res.Text != "10:30" {
		t.Fatalf("partial hint should not authorize whole expression: %+v, %v", res, err)
	}
}
