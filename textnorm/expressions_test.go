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
