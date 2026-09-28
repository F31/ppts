package textnorm

import (
	"context"
	"testing"
)

func TestZHMoney(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHMoneyRule(), ZHDecimalRule(), ZHNumberRule(NumberModeQuantity)}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want string }{
		{"营收 ¥250", "营收 人民币二百五十"},
		{"单价 $1.5", "单价 美元一点五"},
		{"应收 A$8", "应收 澳元八"},
		{"支付 港币66", "支付 港元六十六"},
		{"估值 5万", "估值 人民币五万"},
		{"预算 ¥13.5 万", "预算 人民币十三点五 万"},
	}
	for _, c := range cases {
		res, err := engine.Normalize(context.Background(), Request{Text: c.in})
		if err != nil {
			t.Fatal(err)
		}
		if res.Text != c.want {
			t.Errorf("money Run(%q) = %q, want %q", c.in, res.Text, c.want)
		}
	}
}

func TestZHMoneyWinsOverDecimal(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHMoneyRule(), ZHDecimalRule()}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "¥13.5"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "人民币十三点五" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 1 || res.Edits[0].RuleID != "zh.money" {
		t.Fatalf("edits = %+v, want single money edit", res.Edits)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].RuleID != "zh.decimal" {
		t.Fatalf("diagnostics = %+v, want decimal conflict", res.Diagnostics)
	}
}
