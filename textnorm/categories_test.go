package textnorm

import (
	"context"
	"testing"
)

func TestZHTelephone(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHTelephoneRule(), ZHNumberRule(NumberModeQuantity)}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "拨打13800138000"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "拨打一三八零零一三八零零零" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 1 || res.Edits[0].RuleID != "zh.telephone" {
		t.Fatalf("edits = %+v, want single telephone edit", res.Edits)
	}
	// quantity number rule must lose to telephone at the same start.
	if len(res.Diagnostics) != 1 || res.Diagnostics[0].RuleID != "zh.number.quantity" {
		t.Fatalf("diagnostics = %+v", res.Diagnostics)
	}
}

func TestZHTelephoneRejectsLongRunAndBoundaryAdjacent(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHTelephoneRule()}})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"2313800138000", "X1380013800Y"} {
		res, err := engine.Normalize(context.Background(), Request{Text: in})
		if err != nil {
			t.Fatal(err)
		}
		if res.Text != in || len(res.Edits) != 0 {
			t.Fatalf("Run(%q) = %q edits=%+v, want unchanged", in, res.Text, res.Edits)
		}
	}
}

func TestZHScoreRequiresSemanticHint(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHScoreRule()}})
	if err != nil {
		t.Fatal(err)
	}
	// 无提示：歧义形式保守保留。
	res, err := engine.Normalize(context.Background(), Request{Text: "比分3:2"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "比分3:2" || len(res.Edits) != 0 {
		t.Fatalf("no-hint Run = %q edits=%+v, want unchanged", res.Text, res.Edits)
	}
	// SemanticScore 提示：比分读法启用。
	res, err = engine.Normalize(context.Background(), Request{
		Text:  "比分3:2",
		Hints: []Hint{{Range: Range{2, 5}, Kind: HintSemantic, Semantic: SemanticScore}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "比分三比二" || len(res.Edits) != 1 || res.Edits[0].RuleID != "zh.score" {
		t.Fatalf("hinted Run = %q edits=%+v", res.Text, res.Edits)
	}
}

func TestZHScoreMismatchedHintKeepsPreserving(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHScoreRule(), ZHNumberRule(NumberModeQuantity)}})
	if err != nil {
		t.Fatal(err)
	}
	// SemanticNumber 提示覆盖 "3:2"：score 门控（要求 SemanticScore）不满足 → 不产出"比"。
	// 非门控的 number 规则按"分支独立数字"照常展开 → "比分三:二"（不是"三比二"）。
	res, err := engine.Normalize(context.Background(), Request{
		Text:  "比分3:2",
		Hints: []Hint{{Range: Range{2, 5}, Kind: HintSemantic, Semantic: SemanticNumber}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "比分三:二" {
		t.Fatalf("mismatched-hint Run = %q, want %q (score gated off)", res.Text, "比分三:二")
	}
	for _, e := range res.Edits {
		if e.RuleID == "zh.score" {
			t.Fatalf("score rule must not fire under SemanticNumber hint: %+v", res.Edits)
		}
	}
}
