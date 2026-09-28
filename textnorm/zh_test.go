package textnorm

import (
	"context"
	"testing"
)

func TestZHDateConvertBeatsDecimal(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		ZHDateRule(ModeConvert),
		ZHDecimalRule(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "日期是2024.01.28"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "日期是二零二四年一月二十八日" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 1 || res.Edits[0].RuleID != "zh.date" {
		t.Fatalf("edits = %+v, want date edit only", res.Edits)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].Code != "conflict" || res.Diagnostics[0].RuleID != "zh.decimal" {
		t.Fatalf("diagnostics = %+v, want decimal conflict", res.Diagnostics)
	}
}

func TestZHDatePreserveProtectsAgainstDecimal(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		ZHDateRule(ModePreserve),
		ZHDecimalRule(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "日期是2024.01.28"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "日期是2024.01.28" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 0 {
		t.Fatalf("preserve should not create edits: %+v", res.Edits)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].Code != "conflict" || res.Diagnostics[0].RuleID != "zh.decimal" {
		t.Fatalf("diagnostics = %+v, want decimal conflict", res.Diagnostics)
	}
}

func TestZHDecimalConvertsWhenNoDateCandidate(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHDateRule(ModePreserve), ZHDecimalRule()}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "增长13.5%"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "增长十三点五%" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 1 || res.Edits[0].RuleID != "zh.decimal" {
		t.Fatalf("edits = %+v, want decimal edit", res.Edits)
	}
}

func TestZHDateRejectsInvalidCalendarDate(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHDateRule(ModeConvert), ZHDecimalRule()}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "日期是2024.02.31"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "日期是二千零二十四点零二.31" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 1 || res.Edits[0].RuleID != "zh.decimal" {
		t.Fatalf("edits = %+v, want decimal fallback", res.Edits)
	}
}
