package pronunciation

import "testing"

func TestApplyEmptyRules(t *testing.T) {
	text := "CUDA is great for ML"
	if got := Apply(text, nil); got != text {
		t.Errorf("empty rules should return original text, got %q", got)
	}
}

func TestApplyBasic(t *testing.T) {
	rules := Rules{
		{Pattern: "CUDA", Replacement: "C U D A", Enabled: true},
		{Pattern: "ML", Replacement: "machine learning", Enabled: true},
	}
	got := Apply("CUDA is used for ML", rules)
	want := "C U D A is used for machine learning"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestApplyDisabledRule(t *testing.T) {
	rules := Rules{
		{Pattern: "CUDA", Replacement: "C U D A", Enabled: false},
	}
	got := Apply("CUDA is great", rules)
	if got != "CUDA is great" {
		t.Errorf("disabled rule should not apply, got %q", got)
	}
}

func TestApplyEmptyPattern(t *testing.T) {
	rules := Rules{
		{Pattern: "", Replacement: "something", Enabled: true},
	}
	got := Apply("hello", rules)
	if got != "hello" {
		t.Errorf("empty pattern should not apply, got %q", got)
	}
}

func TestApplyOrdering(t *testing.T) {
	rules := Rules{
		{Pattern: "Kubernetes", Replacement: "Koobernetees", Enabled: true},
		{Pattern: "Koobernetees", Replacement: "K8s", Enabled: true},
	}
	got := Apply("Use Kubernetes", rules)
	want := "Use K8s"
	if got != want {
		t.Errorf("chained rules: got %q, want %q", got, want)
	}
}

func TestApplyMultipleOccurrences(t *testing.T) {
	rules := Rules{
		{Pattern: "MySQL", Replacement: "My Sequel", Enabled: true},
	}
	got := Apply("MySQL connects to MySQL", rules)
	want := "My Sequel connects to My Sequel"
	if got != want {
		t.Errorf("multi-replace: got %q, want %q", got, want)
	}
}
