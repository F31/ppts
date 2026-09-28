package textnorm

import (
	"context"
	"regexp"
	"testing"
)

func TestTechnicalModelPreserveBlocksDigitRule(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		TechnicalModelRule(),
		RegexRule{ID: "digits", Priority: 30, Match: regexp.MustCompile(`[0-9]+`), Replacement: "数字"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "显卡RTX5090Ti发布"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "显卡RTX5090Ti发布" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 0 {
		t.Fatalf("model preserve should not create edits: %+v", res.Edits)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].Code != "conflict" || res.Diagnostics[0].RuleID != "digits" {
		t.Fatalf("diagnostics = %+v, want digits conflict", res.Diagnostics)
	}
}

func TestTechnicalModelPreserveBlocksDecimalInsideModel(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{TechnicalModelRule(), ZHDecimalRule()}})
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
		t.Fatalf("model preserve should block decimal edit: %+v", res.Edits)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].RuleID != "zh.decimal" {
		t.Fatalf("diagnostics = %+v, want decimal conflict", res.Diagnostics)
	}
}

func TestTechnicalModelPreservesHyphenatedCPUModel(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		TechnicalModelRule(),
		RegexRule{ID: "digits", Priority: 30, Match: regexp.MustCompile(`[0-9]+`), Replacement: "数字"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "CPU i7-11800H 可用"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "CPU i7-11800H 可用" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v, want two digit conflicts", res.Diagnostics)
	}
}

func TestTechnicalModelDoesNotProtectDigitLedUnit(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		TechnicalModelRule(),
		RegexRule{ID: "digit", Priority: 30, Match: regexp.MustCompile(`[0-9]+`), Replacement: "五"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "支持5GHz频段"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "支持五GHz频段" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v, want no model conflict", res.Diagnostics)
	}
}
