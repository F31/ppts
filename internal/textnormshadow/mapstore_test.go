package textnormshadow

import (
	"testing"

	"github.com/F31/ppts/internal/contextrule"
	"github.com/F31/ppts/internal/pronunciation"

	oldtn "github.com/F31/ppts/internal/textnorm"
)

func TestFromPronunciationRulesFilters(t *testing.T) {
	rules := pronunciation.Rules{
		{Pattern: "CUDA", Replacement: "库达", Enabled: true},
		{Pattern: "OFF", Replacement: "x", Enabled: false},
		{Pattern: "", Replacement: "空", Enabled: true},
	}
	got := FromPronunciationRules(rules)
	if len(got) != 1 || got[0] != (DictEntry{Match: "CUDA", Replacement: "库达"}) {
		t.Fatalf("got = %+v", got)
	}
}

func TestFromContextRulesFiltersAndOrders(t *testing.T) {
	recs := []*contextrule.Record{
		{ID: "b", Priority: 20, Pattern: "X", Replacement: "x", Enabled: true},
		{ID: "a", Priority: 10, Pattern: "Y", Replacement: "y", Enabled: true},
		{ID: "off", Priority: 5, Pattern: "Z", Replacement: "z", Enabled: false},
		nil,
	}
	got := FromContextRules(recs)
	want := []ContextEntry{{Pattern: "Y", Replacement: "y", Priority: 10}, {Pattern: "X", Replacement: "x", Priority: 20}}
	if len(got) != len(want) {
		t.Fatalf("got = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestFromPersistenceShadowCompare(t *testing.T) {
	mode := oldtn.NumberModeQuantity
	dict := pronunciation.Rules{{Pattern: "CUDA", Replacement: "库达", Enabled: true}}
	ctx := []*contextrule.Record{{ID: "c1", Pattern: "2024年", Replacement: "二零二四年", Priority: 20, Enabled: true}}
	opts := FromPersistence("zh-CN", dict, ctx, nil, &mode)

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

	r := Compare(old, ne, "zh-CN", "CUDA发布2024年，单价5元")
	if r.Same {
		t.Fatalf("report = %+v, want known diff", r)
	}
	// Legacy engine: number rule (priority 20) runs before the contextual rule
	// (same priority, registered later), expanding 2024/5 before the context
	// pattern "2024年" can match.
	// Candidate engine now has number expansion too, but its non-cascading length
	// tie-break gives the longer context candidate "2024年" priority over the
	// contained number "2024". This ordering divergence is real and surfaced.
	if r.OldText != "库达发布二千零二十四年，单价五元" {
		t.Fatalf("old text = %q", r.OldText)
	}
	if r.NewText != "库达发布二零二四年，单价五元" {
		t.Fatalf("new text = %q", r.NewText)
	}
}
