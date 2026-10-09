package textnorm

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestCrossRuleInteractions(t *testing.T) {
	e, err := Compile(Config{Rules: []CandidateRule{
		LiteralDictRule("definition", 3, []LiteralEntry{{Match: "RTX5090", Replace: "显卡"}}),
		TechnicalModelRule(), ZHVersionPreserveRule(), ZHDateRule(ModeConvert),
		ZHPercentRule(), ZHMoneyRule(), ZHUnitRule(), ZHDecimalRule(),
		ZHTelephoneRule(), ZHNumberRule(NumberModeQuantity),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ text, want string }{
		{"金额¥13.5，增长13.5%，容量3.5GB", "金额人民币十三点五，增长百分之十三点五，容量三点五吉字节"},
		{"日期2024.01.28", "日期二零二四年一月二十八日"},
		{"型号RTX5090，版本v3.1.2", "型号显卡，版本v3.1.2"},
		{"电话13812345678", "电话一三八一二三四五六七八"},
		{"😀增长13.5%", "😀增长百分之十三点五"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			res, err := e.Normalize(context.Background(), Request{Text: tc.text})
			if err != nil {
				t.Fatal(err)
			}
			if res.Text != tc.want {
				t.Fatalf("got %q; want %q", res.Text, tc.want)
			}
			// source/destination spans 必须连续，且覆盖完整文本。
			srcEnd, dstEnd := 0, 0
			for _, sp := range res.SourceMap.Spans {
				if sp.Src.Start != srcEnd || sp.Dst.Start != dstEnd {
					t.Fatal("source map gap/overlap")
				}
				srcEnd, dstEnd = sp.Src.End, sp.Dst.End
			}
			if srcEnd != len([]rune(tc.text)) || dstEnd != len([]rune(res.Text)) {
				t.Fatal("source map incomplete")
			}
		})
	}
}

func TestIndexedRulesMatchDirectCandidates(t *testing.T) {
	text := "😀中文¥13.5 13.5% 3.5GB 13812345678 2024.01.28 v3.1.2"
	rules := []CandidateRule{ZHMoneyRule(), ZHPercentRule(), ZHUnitRule(), ZHTelephoneRule(), ZHDateRule(ModeConvert), ZHDecimalRule(), ZHVersionPreserveRule()}
	req := Request{Text: text}
	req.IndexBytes()
	for _, r := range rules {
		if !reflect.DeepEqual(r.FindCandidates(Request{Text: text}), r.FindCandidates(req)) {
			t.Errorf("indexed offsets changed %s candidates", r.RuleID())
		}
	}
}

func TestCompiledDictionaryAndManifestAreSnapshots(t *testing.T) {
	entries := []LiteralEntry{{Match: "CUDA", Replace: "库达"}}
	e, err := Compile(Config{Rules: []CandidateRule{LiteralDictRule("dict", 3, entries)}})
	if err != nil {
		t.Fatal(err)
	}
	entries[0].Replace = "changed"
	res, err := e.Normalize(context.Background(), Request{Text: "CUDA"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "库达" {
		t.Fatal("caller mutation changed compiled dictionary")
	}
	res.Manifest.Rules[0] = "changed"
	if e.Manifest().Rules[0] != "dict" {
		t.Fatal("result manifest aliases cached engine state")
	}
}

func BenchmarkNormalize(b *testing.B) {
	e, err := Compile(Config{Rules: []CandidateRule{ZHMoneyRule(), ZHPercentRule(), ZHUnitRule(), ZHDecimalRule(), ZHNumberRule(NumberModeQuantity)}})
	if err != nil {
		b.Fatal(err)
	}
	for _, size := range []struct {
		name    string
		repeats int
	}{{"short", 1}, {"medium", 50}, {"long", 500}} {
		b.Run(size.name, func(b *testing.B) {
			text := strings.Repeat("中文😀增长13.5%，金额¥13.5，容量3.5GB，数量42。", size.repeats)
			b.ReportAllocs()
			b.SetBytes(int64(len(text)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := e.Normalize(context.Background(), Request{Text: text}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
