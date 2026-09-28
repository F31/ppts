package textnormshadow

import (
	"strings"
	"testing"

	oldtn "github.com/F31/ppts/internal/textnorm"
)

func TestCompareDictEquivalence(t *testing.T) {
	opts := Options{Dict: []DictEntry{{Match: "CUDA", Replacement: "库达"}, {Match: "Model3", Replacement: "Model三"}}}
	old, err := BuildOldEngine(opts)
	if err != nil {
		t.Fatal(err)
	}
	ne, notice, err := BuildNewEngine(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(notice) != 0 {
		t.Fatalf("unexpected notice: %v", notice)
	}
	r := Compare(old, ne, "zh-CN", "CUDA与Model3")
	if !r.Same {
		t.Fatalf("report = %+v, want same", r)
	}
	if r.OldText != "库达与Model三" || r.NewText != "库达与Model三" {
		t.Fatalf("texts = %q / %q", r.OldText, r.NewText)
	}
}

func TestCompareContextEquivalence(t *testing.T) {
	opts := Options{Context: []ContextEntry{{Pattern: "2024年", Replacement: "二零二四年", Priority: 20}}}
	old, err := BuildOldEngine(opts)
	if err != nil {
		t.Fatal(err)
	}
	ne, notice, err := BuildNewEngine(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(notice) != 0 {
		t.Fatalf("unexpected notice: %v", notice)
	}
	r := Compare(old, ne, "zh-CN", "财报2024年")
	if !r.Same {
		t.Fatalf("report = %+v, want same", r)
	}
	if r.OldText != "财报二零二四年" || r.NewText != "财报二零二四年" {
		t.Fatalf("texts = %q / %q", r.OldText, r.NewText)
	}
}

func TestCompareNumberEquivalence(t *testing.T) {
	mode := oldtn.NumberModeQuantity
	opts := Options{Dict: []DictEntry{{Match: "元", Replacement: "人民币"}}, NumberMode: &mode}
	old, err := BuildOldEngine(opts)
	if err != nil {
		t.Fatal(err)
	}
	ne, notice, err := BuildNewEngine(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(notice) != 0 {
		t.Fatalf("notice = %v, want empty (number now covered)", notice)
	}
	r := Compare(old, ne, "zh-CN", "价格5元")
	if !r.Same {
		t.Fatalf("report = %+v, want same", r)
	}
	if r.OldText != "价格五人民币" || r.NewText != "价格五人民币" {
		t.Fatalf("texts = %q / %q", r.OldText, r.NewText)
	}
}

func TestCompareMoneyGapDivergence(t *testing.T) {
	opts := Options{Classes: []string{"money"}}
	old, err := BuildOldEngine(opts)
	if err != nil {
		t.Fatal(err)
	}
	ne, notice, err := BuildNewEngine(opts)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range notice {
		if strings.Contains(n, "money") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notice = %v, want money gap notice", notice)
	}
	r := Compare(old, ne, "zh-CN", "营收 ¥250")
	if r.Same {
		t.Fatalf("report = %+v, want known diff (money unsupported by candidate)", r)
	}
	if r.OldText != "营收 人民币二百五十" {
		t.Fatalf("old text = %q", r.OldText)
	}
	if r.NewText != "营收 ¥250" {
		t.Fatalf("new text = %q", r.NewText)
	}
}

func TestComparePauseEquivalence(t *testing.T) {
	opts := Options{}
	old, err := BuildOldEngine(opts)
	if err != nil {
		t.Fatal(err)
	}
	ne, notice, err := BuildNewEngine(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(notice) != 0 {
		t.Fatalf("unexpected notice: %v", notice)
	}
	r := Compare(old, ne, "zh-CN", "冷链‖运输")
	if !r.Same {
		t.Fatalf("report = %+v, want same", r)
	}
	if r.OldText != "冷链运输" || r.NewText != "冷链运输" {
		t.Fatalf("texts = %q / %q", r.OldText, r.NewText)
	}
	if len(r.OldPauses) != 1 || len(r.NewPauses) != 1 {
		t.Fatalf("pauses = %+v / %+v", r.OldPauses, r.NewPauses)
	}
	if r.OldPauses[0] != (PauseRec{AfterRunes: 2, DurationMS: 300}) {
		t.Fatalf("old pause = %+v", r.OldPauses[0])
	}
	if r.NewPauses[0] != (PauseRec{AfterRunes: 2, DurationMS: 300}) {
		t.Fatalf("new pause = %+v", r.NewPauses[0])
	}
}
