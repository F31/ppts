package main

import "testing"

func TestRunSpecEquivalence(t *testing.T) {
	sp := specJSON{
		Lang:     "zh-CN",
		Dict:     []dictSpec{{Match: "CUDA", Replacement: "库达"}},
		Segments: []string{"CUDA运行", "普通文本"},
	}
	reports, notice, err := runSpec(sp)
	if err != nil {
		t.Fatal(err)
	}
	if len(notice) != 0 {
		t.Fatalf("notice = %v", notice)
	}
	if len(reports) != 2 {
		t.Fatalf("reports = %d", len(reports))
	}
	if !reports[0].Same || reports[0].OldText != "库达运行" || reports[0].NewText != "库达运行" {
		t.Fatalf("report[0] = %+v", reports[0])
	}
	if !reports[1].Same || reports[1].OldText != "普通文本" {
		t.Fatalf("report[1] = %+v", reports[1])
	}
}

func TestRunSpecNumberGapSurfaces(t *testing.T) {
	sp := specJSON{
		Lang:       "zh-CN",
		Dict:       []dictSpec{{Match: "元", Replacement: "人民币"}},
		NumberMode: "quantity",
		Segments:   []string{"价格5元"},
	}
	reports, notice, err := runSpec(sp)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range notice {
		if n == "number expansion not yet supported by candidate engine" {
			found = true
		}
	}
	if !found {
		t.Fatalf("notice = %v", notice)
	}
	r := reports[0]
	if r.Same {
		t.Fatalf("report = %+v, want diff", r)
	}
	if r.OldText != "价格五人民币" || r.NewText != "价格5人民币" {
		t.Fatalf("texts = %q / %q", r.OldText, r.NewText)
	}
}

func TestParseMode(t *testing.T) {
	if m, err := parseMode(""); err != nil || m != nil {
		t.Fatalf("empty mode = %v, %v", m, err)
	}
	if m, err := parseMode("quantity"); err != nil || m == nil {
		t.Fatalf("quantity mode err = %v", err)
	}
	if _, err := parseMode("bogus"); err == nil {
		t.Fatal("parseMode should reject unknown mode")
	}
}
