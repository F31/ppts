package main

import (
	"context"
	"testing"

	"github.com/F31/ppts/internal/contextrule"
	"github.com/F31/ppts/internal/pronunciation"
	newtn "github.com/F31/textnorm"
)

type emptyTextNormDict struct{}

func (emptyTextNormDict) LoadTenantDefault(context.Context, string) (pronunciation.Rules, error) {
	return nil, nil
}
func (emptyTextNormDict) LoadPlatformDefault(context.Context) (pronunciation.Rules, error) {
	return nil, nil
}

type testContextRules struct{}

func (testContextRules) LoadAllEffective(context.Context, string) ([]*contextrule.Record, error) {
	return []*contextrule.Record{{ID: "test", Pattern: "CUDA", Replacement: "库达", Priority: 3}}, nil
}

func TestTextNormBuildCombinesAllOptions(t *testing.T) {
	t.Setenv("PPTS_TEXT_NORM_NUMBERS", "quantity")
	t.Setenv("PPTS_TEXT_NORM_CLASSES", "percent")
	a := appTextNorm{store: emptyTextNormDict{}, contextRules: testContextRules{}}
	e, err := a.Build(context.Background(), "tenant", "zh")
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Normalize(context.Background(), newtn.Request{Text: "CUDA 13.5% 42"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "库达 百分之十三点五 四十二" {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestTextNormBuildSelectsRulesByLanguage(t *testing.T) {
	a := appTextNorm{store: emptyTextNormDict{}, contextRules: testContextRules{}}
	// en → English rules (quantity number, percent) + shared dict/context.
	en, err := a.Build(context.Background(), "tenant", "en-US")
	if err != nil {
		t.Fatal(err)
	}
	res, err := en.Normalize(context.Background(), newtn.Request{Text: "CUDA 2048, 12.5%"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "库达 two thousand forty-eight, twelve point five percent" {
		t.Fatalf("en text = %q", res.Text)
	}
	// zh → Chinese rules (percent via class env, quantity number).
	zh, err := a.Build(context.Background(), "tenant", "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	res, err = zh.Normalize(context.Background(), newtn.Request{Text: "CUDA 2048, 12.5%"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "库达 2048, 12.5%" {
		t.Fatalf("zh text = %q (expected conservative no-class/no-number fallback)", res.Text)
	}
	// The two engines must not share rule sets.
	if en.Manifest().ConfigHash == zh.Manifest().ConfigHash {
		t.Fatal("zh and en engines produced identical config hashes")
	}
}
