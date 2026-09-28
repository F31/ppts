package textnormshadow

import (
	"testing"

	oldtn "github.com/F31/ppts/internal/textnorm"
)

func TestCompareDictEquivalence(t *testing.T) {
	opts := Options{Dict: []DictEntry{{Match: "CUDA", Replacement: "库达"}, {Match: "SDK", Replacement: "软件开发包"}}}
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
	r := Compare(old, ne, "zh-CN", "CUDA与SDK")
	if !r.Same {
		t.Fatalf("report = %+v, want same", r)
	}
	if r.OldText != "库达与软件开发包" || r.NewText != "库达与软件开发包" {
		t.Fatalf("texts = %q / %q", r.OldText, r.NewText)
	}
}

// TestCompareDetectsTechnicalModelPreserve 验证 shadow 对 S4 模型的检测能力：
// 候选引擎会保留 "Model3"（技术型号，见 textnorm/model.go），旧引擎则按词典改写
// 为 "Model三"——这是真实分歧，应被报告为 Same=false，而不是被压掉。
func TestCompareDetectsTechnicalModelPreserve(t *testing.T) {
	opts := Options{Dict: []DictEntry{{Match: "Model3", Replacement: "Model三"}}}
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
	r := Compare(old, ne, "zh-CN", "Model3与CUDA")
	if r.Same {
		t.Fatalf("report = %+v, want diverged (technical model preserve)", r)
	}
	if r.OldText == r.NewText {
		t.Fatalf("unexpected equal texts: %q", r.OldText)
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

func TestCompareMoneyEquivalence(t *testing.T) {
	opts := Options{Classes: []string{"money"}}
	old, err := BuildOldEngine(opts)
	if err != nil {
		t.Fatal(err)
	}
	ne, notice, err := BuildNewEngine(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(notice) != 0 {
		t.Fatalf("notice = %v, want empty (money now covered)", notice)
	}
	r := Compare(old, ne, "zh-CN", "营收 ¥250")
	if !r.Same {
		t.Fatalf("report = %+v, want same", r)
	}
	if r.OldText != "营收 人民币二百五十" || r.NewText != "营收 人民币二百五十" {
		t.Fatalf("texts = %q / %q", r.OldText, r.NewText)
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
