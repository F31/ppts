package pricing

import (
	"os"
	"testing"
)

func TestDefaultBookAmounts(t *testing.T) {
	b := Default()
	if b.UserAmount("gen_seconds", 10) != 0.1 {
		t.Fatalf("user amount = %v want 0.1", b.UserAmount("gen_seconds", 10))
	}
	if b.SupplierAmount("gen_seconds", 10) != 0.04 {
		t.Fatalf("supplier cost = %v want 0.04", b.SupplierAmount("gen_seconds", 10))
	}
	if b.UserAmount("unknown_kind", 10) != 0 {
		t.Fatalf("unknown kind should be 0")
	}
	if b.UserAmount("gen_seconds", 0) != 0 {
		t.Fatalf("zero units should be 0")
	}
}

func TestFromEnvJSON(t *testing.T) {
	t.Setenv("PPTS_PRICE_BOOK", `{"version":"v2","currency":"USD","user":{"gen_seconds":0.02},"supplier":{"gen_seconds":0.01}}`)
	b, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if b.Version != "v2" || b.Currency != "USD" || b.UserAmount("gen_seconds", 1) != 0.02 || b.SupplierAmount("gen_seconds", 1) != 0.01 {
		t.Fatalf("book = %+v", b)
	}
}

func TestFromEnvInvalidJSON(t *testing.T) {
	t.Setenv("PPTS_PRICE_BOOK", `not-json`)
	if _, err := FromEnv(); err == nil {
		t.Fatalf("invalid PPTS_PRICE_BOOK should error")
	}
}

func TestFromEnvDefaultWhenUnset(t *testing.T) {
	os.Unsetenv("PPTS_PRICE_BOOK")
	b, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if b == nil || b.Currency == "" {
		t.Fatalf("default book = %+v", b)
	}
}
