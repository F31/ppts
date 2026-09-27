package textnorm

import (
	"regexp"
	"strings"
	"testing"
)

// TestRegisterPattern 声明式规则：Match 识别 + Verbalize 模板产出。
func TestRegisterPattern(t *testing.T) {
	e := New()
	e.RegisterPattern(Pattern{
		Category: "percent",
		Priority: 30,
		Match:    regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?%`),
		Verbalize: func(matched string, _ *Context) (string, bool) {
			num := strings.TrimSuffix(matched, "%")
			cn, ok := decimalToCN(num)
			if !ok {
				return "", false
			}
			return "百分之" + cn, true
		},
	})
	e.Build()

	res := e.Run("同比增长 6.3%", "zh-CN")
	want := "同比增长 百分之六点三"
	if res.Text != want {
		t.Fatalf("Run(%q) = %q want %q", "同比增长 6.3%", res.Text, want)
	}
	if res.IndexMap == nil || res.IndexMap.Empty() {
		t.Fatal("expected IndexMap from pattern rewrite")
	}
	// 改动段覆盖 6.3% → 百分之六点三（src [5,9) → dst [5,11)）。
	var seg *IndexSeg
	for i := range res.IndexMap.Segments {
		s := &res.IndexMap.Segments[i]
		if s.SrcStart == 5 && s.SrcEnd == 9 {
			seg = s
		}
	}
	if seg == nil {
		t.Fatalf("no segment for '6.3%%', got %v", res.IndexMap.Segments)
	}
	if seg.DstStart != 5 || seg.DstEnd != 11 {
		t.Fatalf("percent segment = %+v, want [5,9)→[5,11)", seg)
	}
}

// decimalToCN 把数字字符串（可含小数）转中文读法（M3 会正式提供 DecimalToCN 公共库）。
func decimalToCN(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	whole := s
	frac := ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		whole, frac = s[:dot], s[dot+1:]
	}
	cnWhole, ok := NumToCN(whole)
	if !ok {
		return "", false
	}
	if frac == "" {
		return cnWhole, true
	}
	var sb strings.Builder
	sb.WriteString(cnWhole)
	sb.WriteString("点")
	for _, d := range frac {
		sb.WriteRune(digitCN(d))
	}
	return sb.String(), true
}

// TestRegisterPatternNoMatch 未命中时原样透传。
func TestRegisterPatternNoMatch(t *testing.T) {
	e := New()
	e.RegisterPattern(Pattern{
		Category: "percent",
		Priority: 30,
		Match:    regexp.MustCompile(`([0-9]+)%`),
		Verbalize: func(matched string, _ *Context) (string, bool) {
			return "百分之" + matched[:len(matched)-1], true
		},
	})
	e.Build()
	res := e.Run("普通文本", "zh-CN")
	if res.Text != "普通文本" {
		t.Fatalf("no-match: got %q", res.Text)
	}
	if !res.IndexMap.Empty() {
		t.Fatalf("no-match: unexpected IndexMap %v", res.IndexMap)
	}
}

// TestRegisterPatternAfterBuildPanics 负测：Build 后 RegisterPattern panic。
func TestRegisterPatternAfterBuildPanics(t *testing.T) {
	e := New()
	e.Build()
	defer func() {
		if recover() == nil {
			t.Fatal("RegisterPattern after Build should panic")
		}
	}()
	e.RegisterPattern(Pattern{
		Category: "x", Priority: 1,
		Match: regexp.MustCompile(`x`),
		Verbalize: func(m string, _ *Context) (string, bool) { return "y", true },
	})
}

// TestRegisterPatternNilFieldsPanics 负测：Match/Verbalize 缺失 panic。
func TestRegisterPatternNilFieldsPanics(t *testing.T) {
	for name, p := range map[string]Pattern{
		"nil-match":   {Category: "x", Priority: 1, Verbalize: func(string, *Context) (string, bool) { return "", false }},
		"nil-verbal":  {Category: "x", Priority: 1, Match: regexp.MustCompile(`x`)},
		"both-nil":    {Category: "x", Priority: 1},
	} {
		e := New()
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("RegisterPattern(%s) should panic", name)
				}
			}()
			e.RegisterPattern(p)
		}()
	}
}

// TestReplacementPattern 数据驱动「正则→模板」规则：捕获组替换 + 精确坐标上报。
func TestReplacementPattern(t *testing.T) {
	e := New()
	p, err := ReplacementPattern(`([0-9]+)年`, "一九九$1", 10)
	if err != nil {
		t.Fatalf("ReplacementPattern: %v", err)
	}
	e.RegisterPattern(p)
	e.Build()
	res := e.Run("2024年发布", "zh-CN")
	// $1 = "2024"，模板 "一九九2024"。字面拼接（捕获组是数字本身）。
	if res.Text != "一九九2024发布" {
		t.Fatalf("Run = %q", res.Text)
	}
	if res.IndexMap == nil || res.IndexMap.Empty() {
		t.Fatal("expected IndexMap")
	}
	var seg *IndexSeg
	for i := range res.IndexMap.Segments {
		s := &res.IndexMap.Segments[i]
		if s.SrcStart == 0 && s.SrcEnd == 5 {
			seg = s
		}
	}
	if seg == nil {
		t.Fatalf("no seg for 2024年, got %v", res.IndexMap.Segments)
	}
	if seg.DstStart != 0 || seg.DstEnd != 7 {
		t.Fatalf("seg = %+v, want [0,5)→[0,7)", seg)
	}
}

// TestReplacementPatternInvalidRegexp 非法正则返回 error（R1 不静默）。
func TestReplacementPatternInvalidRegexp(t *testing.T) {
	if _, err := ReplacementPattern(`([0-9]+`, "x", 10); err == nil {
		t.Fatal("invalid regexp should error")
	}
}
func TestPatternMultipleHits(t *testing.T) {
	e := New()
	e.RegisterPattern(Pattern{
		Category: "decimal",
		Priority: 30,
		Match:    regexp.MustCompile(`[0-9]+\.[0-9]+`),
		Verbalize: func(matched string, _ *Context) (string, bool) {
			parts := strings.SplitN(matched, ".", 2)
			whole, ok := NumToCN(parts[0])
			if !ok {
				return "", false
			}
			frac := parts[1]
			var sb strings.Builder
			sb.WriteString(whole)
			sb.WriteString("点")
			for _, d := range frac {
				sb.WriteRune(digitCN(d))
			}
			return sb.String(), true
		},
	})
	e.Build()
	res := e.Run("误差 1.5 和 2.75", "zh-CN")
	want := "误差 一点五 和 二点七五"
	if res.Text != want {
		t.Fatalf("multi-hit: got %q want %q", res.Text, want)
	}
	if res.IndexMap == nil || res.IndexMap.Empty() {
		t.Fatal("multi-hit: expected IndexMap")
	}
	// 两个命中段都存在："误差 1.5 和 2.75" → 1.5=[3,6)→[3,6), 2.75=[9,13)→[9,11)。
	// (1.5→"一点五" 3rune 同长；2.75→"二点七五" 4rune 异长)
	var n1, n2 bool
	for i := range res.IndexMap.Segments {
		s := &res.IndexMap.Segments[i]
		if s.SrcStart == 3 && s.SrcEnd == 6 && s.DstStart == 3 && s.DstEnd == 6 {
			n1 = true
		}
		if s.SrcStart == 9 && s.SrcEnd == 13 && s.DstStart == 9 && s.DstEnd == 13 {
			n2 = true
		}
	}
	if !n1 {
		t.Fatalf("multi-hit: missing seg for 1.5, got %v", res.IndexMap.Segments)
	}
	if !n2 {
		t.Fatalf("multi-hit: missing seg for 2.75, got %v", res.IndexMap.Segments)
	}
}
