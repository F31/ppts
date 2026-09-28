package textnorm

import (
	"context"
	"testing"
)

func TestReadingHintReplacesRange(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHDecimalRule()}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{
		Text:  "价格为3.14",
		Hints: []Hint{{Range: Range{3, 7}, Kind: HintReading, Text: "三点一四"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "价格为三点一四" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 1 || res.Edits[0].RuleID != "hint.reading" {
		t.Fatalf("edits = %+v", res.Edits)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].Code != "conflict" || res.Diagnostics[0].RuleID != "zh.decimal" {
		t.Fatalf("diagnostics = %+v, want decimal conflict", res.Diagnostics)
	}
}

func TestVerbatimHintPreservesRange(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHPercentRule()}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{
		Text:  "占比13.5%",
		Hints: []Hint{{Range: Range{2, 7}, Kind: HintVerbatim}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "占比13.5%" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 0 {
		t.Fatalf("edits = %+v, want none", res.Edits)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].RuleID != "zh.percent" {
		t.Fatalf("diagnostics = %+v, want percent conflict", res.Diagnostics)
	}
}

func TestSemanticHintAllowsMatchingRule(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHDateRule(ModeConvert), ZHDecimalRule()}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{
		Text:  "2024.01.28",
		Hints: []Hint{{Range: Range{0, 10}, Kind: HintSemantic, Semantic: SemanticDate}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "二零二四年一月二十八日" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 1 || res.Edits[0].RuleID != "zh.date" {
		t.Fatalf("edits = %+v", res.Edits)
	}
}

func TestSemanticHintBlocksNonMatchingAndPreserves(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHPercentRule(), ZHDecimalRule()}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{
		Text:  "占比13.5%",
		Hints: []Hint{{Range: Range{2, 7}, Kind: HintSemantic, Semantic: SemanticNumber}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "占比13.5%" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 0 {
		t.Fatalf("edits = %+v, want none", res.Edits)
	}
	if len(res.Diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v, want two hint-blocked candidates", res.Diagnostics)
	}
	for _, d := range res.Diagnostics {
		if d.Code != "hint" {
			t.Fatalf("diagnostic code = %q, want hint", d.Code)
		}
	}
}

func TestContradictoryHintsRejected(t *testing.T) {
	engine, err := Compile(Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Normalize(context.Background(), Request{
		Text: "abc",
		Hints: []Hint{
			{Range: Range{1, 2}, Kind: HintReading, Text: "x"},
			{Range: Range{1, 2}, Kind: HintVerbatim},
		},
	})
	if err == nil {
		t.Fatal("Normalize should reject overlapping hints")
	}
}

func TestUnknownSemanticHintRejected(t *testing.T) {
	engine, err := Compile(Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Normalize(context.Background(), Request{
		Text:  "abc",
		Hints: []Hint{{Range: Range{0, 3}, Kind: HintSemantic, Semantic: "unknown"}},
	})
	if err == nil {
		t.Fatal("Normalize should reject unknown semantic hint")
	}
}

func TestHintOutOfBoundsRejected(t *testing.T) {
	engine, err := Compile(Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Normalize(context.Background(), Request{
		Text:  "abc",
		Hints: []Hint{{Range: Range{1, 4}, Kind: HintVerbatim}},
	})
	if err == nil {
		t.Fatal("Normalize should reject out-of-range hint")
	}
}
