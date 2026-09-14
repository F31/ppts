package gateway

import "testing"

func TestMaskKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"short", "****"},
		{"12345678", "****"},
		{"sk-abcdefghij", "sk-****hij"},
	}
	for _, c := range cases {
		if got := MaskKey(c.in); got != c.want {
			t.Errorf("MaskKey(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestDefaultIfEmpty(t *testing.T) {
	if defaultIfEmpty("", "fb") != "fb" {
		t.Errorf("empty should return fallback")
	}
	if defaultIfEmpty("  ", "fb") != "fb" {
		t.Errorf("blank should return fallback")
	}
	if defaultIfEmpty("a/", "fb") != "a" {
		t.Errorf("should trim trailing slash, got %q", defaultIfEmpty("a/", "fb"))
	}
	if defaultIfEmpty("a", "fb") != "a" {
		t.Errorf("should keep value")
	}
}
