package textnorm

import (
	"context"
	"testing"
)

func TestLiteralDictRuleReplacesWhole(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		LiteralDictRule("dict", 10, []LiteralEntry{{Match: "Model3", Replace: "Model三", Whole: true}}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "联想Model3发布"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "联想Model三发布" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 1 || res.Edits[0].RuleID != "dict" {
		t.Fatalf("edits = %+v", res.Edits)
	}
}

func TestLiteralDictRuleRejectsEmbeddedMatch(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		LiteralDictRule("dict", 10, []LiteralEntry{{Match: "Model3", Replace: "Model三", Whole: true}}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "XModel3"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "XModel3" || len(res.Edits) != 0 {
		t.Fatalf("embedded match should be rejected: text=%q edits=%+v", res.Text, res.Edits)
	}
}

func TestLiteralDictRuleAllowsEmbeddedWhenNotWhole(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		LiteralDictRule("dict", 10, []LiteralEntry{{Match: "Model3", Replace: "三", Whole: false}}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "XModel3"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "X三" {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestLiteralDictRuleDeleteMarksDeletion(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		LiteralDictRule("strip", 10, []LiteralEntry{{Match: "X", Replace: "", Whole: true}}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "中X中"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "中中" {
		t.Fatalf("text = %q", res.Text)
	}
	found := false
	for _, span := range res.SourceMap.Spans {
		if span.Kind == SourceDeletion && span.Src == (Range{1, 2}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("source spans = %+v, want deletion span", res.SourceMap.Spans)
	}
}

func TestLiteralDictRuleBlocksDecimalInsideLiteral(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		LiteralDictRule("dict", 10, []LiteralEntry{{Match: "Model3", Replace: "Model三", Whole: true}}),
		ZHDecimalRule(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "联想Model3.5"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "联想Model三.5" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 1 || res.Edits[0].RuleID != "dict" {
		t.Fatalf("edits = %+v", res.Edits)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].Code != "conflict" || res.Diagnostics[0].RuleID != "zh.decimal" {
		t.Fatalf("diagnostics = %+v", res.Diagnostics)
	}
}

func TestLiteralDictRuleRejectsEmptyMatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("LiteralDictRule should panic on empty Match")
		}
	}()
	LiteralDictRule("bad", 10, []LiteralEntry{{Match: "", Replace: "x", Whole: true}})
}
