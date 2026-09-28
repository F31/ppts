package textnorm

import (
	"context"
	"testing"
)

func TestZHNumberQuantity(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHNumberRule(NumberModeQuantity)}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want string }{
		{"涨价到 2024 年", "涨价到 二千零二十四 年"},
		{"成本 5 元", "成本 五 元"},
		{"共 13 台", "共 十三 台"},
	}
	for _, c := range cases {
		res, err := engine.Normalize(context.Background(), Request{Text: c.in})
		if err != nil {
			t.Fatal(err)
		}
		if res.Text != c.want {
			t.Errorf("quantity Run(%q) = %q, want %q", c.in, res.Text, c.want)
		}
	}
}

func TestZHNumberYear(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHNumberRule(NumberModeYear)}})
	if err != nil {
		// fallback: register via map
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "公元 2024 年"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "公元 二零二四 年" {
		t.Fatalf("year Run = %q", res.Text)
	}
	if res, _ := engine.Normalize(context.Background(), Request{Text: "编号 0024"}); res.Text != "编号 二四" {
		t.Fatalf("leading-zero trim: %q", res.Text)
	}
}

func TestZHNumberDoesNotSplitModelAdjacent(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHNumberRule(NumberModeQuantity), TechnicalModelRule()}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want string }{
		{"支持5GHz频段", "支持5GHz频段"},       // model/unit token not bisected
		{"显卡RTX5090Ti", "显卡RTX5090Ti"}, // model preserved
	}
	for _, c := range cases {
		res, err := engine.Normalize(context.Background(), Request{Text: c.in})
		if err != nil {
			t.Fatal(err)
		}
		if res.Text != c.want {
			t.Errorf("Run(%q) = %q, want %q", c.in, res.Text, c.want)
		}
	}
}

func TestZHNumberRejectsOver12Digits(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHNumberRule(NumberModeQuantity)}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "1234567890123"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "1234567890123" {
		t.Fatalf("13-digit should be untouched, got %q", res.Text)
	}
}

func TestZHNumberDistinctRuleIDs(t *testing.T) {
	if _, err := Compile(Config{Rules: []CandidateRule{ZHNumberRule(NumberModeYear), ZHNumberRule(NumberModeQuantity)}}); err != nil {
		t.Fatalf("year+quantity must be co-registrable: %v", err)
	}
}
