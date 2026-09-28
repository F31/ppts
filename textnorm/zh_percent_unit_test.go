package textnorm

import (
	"context"
	"regexp"
	"testing"
)

func TestZHPercentConvertsWholeSpanBeforeDecimal(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHPercentRule(), ZHDecimalRule()}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "增长13.5%"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "增长百分之十三点五" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 1 || res.Edits[0].RuleID != "zh.percent" {
		t.Fatalf("edits = %+v, want percent edit", res.Edits)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].RuleID != "zh.decimal" || res.Diagnostics[0].Code != "conflict" {
		t.Fatalf("diagnostics = %+v, want decimal conflict", res.Diagnostics)
	}
}

func TestZHUnitConvertsDigitLedUnit(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		TechnicalModelRule(),
		ZHUnitRule(),
		RegexRule{ID: "digit", Priority: 30, Match: regexp.MustCompile(`[0-9]+`), Replacement: "数字"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "支持5GHz频段"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "支持五吉赫兹频段" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 1 || res.Edits[0].RuleID != "zh.unit" {
		t.Fatalf("edits = %+v, want unit edit", res.Edits)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].RuleID != "digit" || res.Diagnostics[0].Code != "conflict" {
		t.Fatalf("diagnostics = %+v, want digit conflict", res.Diagnostics)
	}
}

func TestZHUnitConvertsDecimalUnitBeforeDecimal(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHUnitRule(), ZHDecimalRule()}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "容量3.5GB"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "容量三点五吉字节" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 1 || res.Edits[0].RuleID != "zh.unit" {
		t.Fatalf("edits = %+v, want unit edit", res.Edits)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].RuleID != "zh.decimal" || res.Diagnostics[0].Code != "conflict" {
		t.Fatalf("diagnostics = %+v, want decimal conflict", res.Diagnostics)
	}
}

func TestTechnicalModelStillBeatsUnitRule(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{TechnicalModelRule(), ZHUnitRule(), ZHDecimalRule()}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "接口USB3.0速度快"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "接口USB3.0速度快" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 0 {
		t.Fatalf("model preserve should block edits: %+v", res.Edits)
	}
}
