package textnorm

import "testing"

func TestProfileRulesBuiltins(t *testing.T) {
	for _, name := range []string{ProfileConservative, ProfileDateDecimal, ProfileDatePreserve} {
		rules, err := ProfileRules(name)
		if err != nil {
			t.Fatalf("ProfileRules(%q): %v", name, err)
		}
		if len(rules) == 0 {
			t.Fatalf("ProfileRules(%q): empty rule set", name)
		}
	}
}

func TestProfileRulesUnknownRejected(t *testing.T) {
	if _, err := ProfileRules("does-not-exist"); err == nil {
		t.Fatal("ProfileRules should reject unknown profile")
	}
}
