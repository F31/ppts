package textnorm

import (
	"context"
	"testing"
)

func TestEnglishConservativeProfile(t *testing.T) {
	engine, err := CompileProfile(ProfileEnglishConservative)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		in, want string
	}{
		{"GPU RTX5090Ti and CPU 12 cores", "GPU RTX5090Ti and CPU twelve cores"},
		{"ratio 12.5% vs 13%", "ratio twelve point five percent vs thirteen percent"},
		{"version v3.1.2 stays", "version v3.1.2 stays"},
		{"size 3.5 GB is a decimal", "size three point five GB is a decimal"},
	} {
		res, err := engine.Normalize(context.Background(), Request{Text: tc.in})
		if err != nil {
			t.Fatal(err)
		}
		if res.Text != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, res.Text, tc.want)
		}
	}
}

func TestEnglishProfileInterplay(t *testing.T) {
	// Percent (priority 12) must win over decimal (priority 20) for 12.5%,
	// and a model token must be preserved over the quantity number rule.
	engine, err := Compile(Config{Rules: []CandidateRule{
		TechnicalModelRule(),
		ENPercentRule(),
		ENDecimalRule(),
		ENNumberRule(ENNumberModeQuantity),
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "RTX5090Ti cost 12.5% of 2048"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "RTX5090Ti cost twelve point five percent of two thousand forty-eight" {
		t.Fatalf("text = %q", res.Text)
	}
}
