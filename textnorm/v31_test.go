package textnorm

import (
	"context"
	"regexp"
	"testing"
)

func TestNormalizeDoesNotCascadeRuleOutput(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		RegexRule{ID: "a-to-1", Priority: 10, Match: regexp.MustCompile(`A`), Replacement: "1"},
		RegexRule{ID: "1-to-one", Priority: 20, Match: regexp.MustCompile(`1`), Replacement: "一"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "A"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "1" {
		t.Fatalf("Normalize text = %q, want non-cascaded %q", res.Text, "1")
	}
	if len(res.Edits) != 1 || res.Edits[0].RuleID != "a-to-1" {
		t.Fatalf("edits = %+v, want only first rule", res.Edits)
	}
}

func TestProtectedRangeBlocksCandidate(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		RegexRule{ID: "digits", Priority: 10, Match: regexp.MustCompile(`[0-9]+`), Replacement: "数字"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "RTX5090Ti", Protected: []Range{{Start: 0, End: 9}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "RTX5090Ti" {
		t.Fatalf("protected text = %q", res.Text)
	}
	if len(res.Edits) != 0 {
		t.Fatalf("protected candidate should not edit: %+v", res.Edits)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].Code != "protected" {
		t.Fatalf("diagnostics = %+v, want protected", res.Diagnostics)
	}
}

func TestSourceMapSeparatesIdentityAndReplacement(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		RegexRule{ID: "digits", Priority: 10, Match: regexp.MustCompile(`12`), Replacement: "十二"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "A12B"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "A十二B" {
		t.Fatalf("text = %q", res.Text)
	}
	want := []SourceSpan{
		{Src: Range{0, 1}, Dst: Range{0, 1}, Kind: SourceIdentity},
		{Src: Range{1, 3}, Dst: Range{1, 3}, Kind: SourceReplacement, RuleID: "digits"},
		{Src: Range{3, 4}, Dst: Range{3, 4}, Kind: SourceIdentity},
	}
	if len(res.SourceMap.Spans) != len(want) {
		t.Fatalf("source spans = %+v", res.SourceMap.Spans)
	}
	for i := range want {
		if res.SourceMap.Spans[i] != want[i] {
			t.Fatalf("span[%d] = %+v, want %+v", i, res.SourceMap.Spans[i], want[i])
		}
	}
}

func TestCompileRejectsDuplicateRuleID(t *testing.T) {
	_, err := Compile(Config{Rules: []CandidateRule{
		RegexRule{ID: "dup", Priority: 10, Match: regexp.MustCompile(`A`), Replacement: "a"},
		RegexRule{ID: "dup", Priority: 20, Match: regexp.MustCompile(`B`), Replacement: "b"},
	}})
	if err == nil {
		t.Fatal("Compile should reject duplicate rule id")
	}
}

func TestNormalizeReportsConflictingCandidate(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		RegexRule{ID: "long", Priority: 10, Match: regexp.MustCompile(`123`), Replacement: "一二三"},
		RegexRule{ID: "short", Priority: 20, Match: regexp.MustCompile(`23`), Replacement: "二三"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "123"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "一二三" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].Code != "conflict" || res.Diagnostics[0].RuleID != "short" {
		t.Fatalf("diagnostics = %+v, want short conflict", res.Diagnostics)
	}
}

func TestNormalizeRejectsInvalidProtectedRange(t *testing.T) {
	engine, err := Compile(Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Normalize(context.Background(), Request{Text: "abc", Protected: []Range{{Start: 1, End: 4}}})
	if err == nil {
		t.Fatal("Normalize should reject protected range outside input")
	}
}

func TestNormalizeHonorsCanceledContext(t *testing.T) {
	engine, err := Compile(Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = engine.Normalize(ctx, Request{Text: "abc"})
	if err == nil {
		t.Fatal("Normalize should return context error")
	}
}

func TestSourceMapMarksDeletion(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		RegexRule{ID: "delete-mark", Priority: 10, Match: regexp.MustCompile(`‖`), Replacement: ""},
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "A‖B"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "AB" {
		t.Fatalf("text = %q", res.Text)
	}
	found := false
	for _, span := range res.SourceMap.Spans {
		if span.Kind == SourceDeletion && span.Src == (Range{1, 2}) && span.Dst == (Range{1, 1}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("source spans = %+v, want deletion span", res.SourceMap.Spans)
	}
}
