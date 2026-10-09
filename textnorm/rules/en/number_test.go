package en

import (
	"testing"

	"github.com/F31/textnorm/internal/contract"
)

func TestNumberRuleQuantity(t *testing.T) {
	r := NumberRule(contract.ENNumberModeQuantity)
	req := contract.Request{Text: "storage 2048 GB"}
	req.IndexBytes()
	cands := r.FindCandidates(req)
	if len(cands) != 1 {
		t.Fatalf("candidates = %d, want 1", len(cands))
	}
	if cands[0].Replacement != "two thousand forty-eight" {
		t.Fatalf("replacement = %q", cands[0].Replacement)
	}
	if cands[0].Src.Start != 8 || cands[0].Src.End != 12 {
		t.Fatalf("src range = %+v", cands[0].Src)
	}
}

func TestNumberRuleBoundaries(t *testing.T) {
	r := NumberRule(contract.ENNumberModeQuantity)
	req := contract.Request{Text: "A123 123B 999999999999 1000000000000"}
	req.IndexBytes()
	cands := r.FindCandidates(req)
	if len(cands) != 1 {
		t.Fatalf("candidates = %d, want 1 (only standalone 12-digit)", len(cands))
	}
	if cands[0].Replacement != "nine hundred ninety-nine billion nine hundred ninety-nine million nine hundred ninety-nine thousand nine hundred ninety-nine" {
		t.Fatalf("replacement = %q", cands[0].Replacement)
	}
}

func TestNumberRuleYearAndDigits(t *testing.T) {
	year := NumberRule(contract.ENNumberModeYear)
	req := contract.Request{Text: "in 2024 1905 2000"}
	req.IndexBytes()
	if got := year.FindCandidates(req); len(got) != 3 {
		t.Fatalf("year candidates = %d, want 3", len(got))
	} else if got[0].Replacement != "twenty twenty-four" {
		t.Fatalf("2024 = %q", got[0].Replacement)
	}
	digits := NumberRule(contract.ENNumberModeDigits)
	req = contract.Request{Text: "code 001"}
	req.IndexBytes()
	got := digits.FindCandidates(req)
	if len(got) != 1 || got[0].Replacement != "zero zero one" {
		t.Fatalf("digits 001 = %+v", got)
	}
}

func TestDecimalAndPercent(t *testing.T) {
	req := contract.Request{Text: "value 3.14"}
	req.IndexBytes()
	dec := DecimalRule().FindCandidates(req)
	if len(dec) != 1 || dec[0].Replacement != "three point one four" {
		t.Fatalf("decimal = %+v", dec)
	}
	req = contract.Request{Text: "rate 12.5%"}
	req.IndexBytes()
	pct := PercentRule().FindCandidates(req)
	if len(pct) != 1 || pct[0].Replacement != "twelve point five percent" {
		t.Fatalf("percent = %+v", pct)
	}
	// Integer percent without a fractional part.
	req = contract.Request{Text: "rate 12%"}
	req.IndexBytes()
	got := PercentRule().FindCandidates(req)
	if len(got) != 1 || got[0].Replacement != "twelve percent" {
		t.Fatalf("12%% = %+v", got)
	}
}
