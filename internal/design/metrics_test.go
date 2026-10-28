package design

import (
	"math"
	"reflect"
	"testing"
)

func TestContrastKnownValues(t *testing.T) {
	black := MustParseHex("#000000")
	white := MustParseHex("#FFFFFF")
	if got := ContrastRatio(black, white); math.Abs(got-21) > 0.01 {
		t.Errorf("黑白对比度 = %.2f，期望 21", got)
	}
	if got := ContrastRatio(white, white); got != 1 {
		t.Errorf("同色对比度 = %.2f，期望 1", got)
	}
	if ContrastRatio(black, white) != ContrastRatio(white, black) {
		t.Error("对比度应对称")
	}
	// 已知灰：白底 #767676 约 4.54:1（WCAG 的经典临界值）。
	if got := ContrastRatio(MustParseHex("#767676"), white); math.Abs(got-4.54) > 0.03 {
		t.Errorf("#767676 对白 = %.2f，期望 ≈4.54", got)
	}
}

func TestLuminanceExtremes(t *testing.T) {
	if got := MustParseHex("#000000").Luminance(); got != 0 {
		t.Errorf("黑色亮度 = %v，期望 0", got)
	}
	if got := MustParseHex("#FFFFFF").Luminance(); math.Abs(got-1) > 1e-9 {
		t.Errorf("白色亮度 = %v，期望 1", got)
	}
}

func TestCheckPageClean(t *testing.T) {
	r := CheckPage(PageFacts{
		SlideID: "s1", DurationMS: 6000, ElementCount: 5,
		MinBodyFontPx: 32, MinTextContrast: 7.9, OccupiedRatio: 0.5,
		ClipCount: 6, BeatCount: 4,
	})
	if len(r.Findings) != 0 {
		t.Fatalf("干净页不应有发现项，实际 %+v", r.Findings)
	}
	if !r.OK() {
		t.Error("干净页应通过")
	}
}

func TestCheckPageFontSizeError(t *testing.T) {
	r := CheckPage(PageFacts{SlideID: "s1", MinBodyFontPx: 28})
	if r.OK() {
		t.Fatal("字号不达标应阻断")
	}
	f := r.Findings[0]
	if f.Code != RuleFontSize || f.Severity != SeverityError {
		t.Errorf("应为 %s/error，实际 %s/%s", RuleFontSize, f.Code, f.Severity)
	}
	if f.Limit != MinBodyFontPx {
		t.Errorf("阈值 = %v，期望 %v", f.Limit, MinBodyFontPx)
	}
	if f.Hint == "" {
		t.Error("应给出修正建议")
	}
}

func TestCheckPageContrastError(t *testing.T) {
	r := CheckPage(PageFacts{SlideID: "s1", MinTextContrast: 3.1})
	if r.OK() {
		t.Fatal("对比度不足应阻断")
	}
	if r.Findings[0].Code != RuleContrast {
		t.Errorf("规则 = %s，期望 %s", r.Findings[0].Code, RuleContrast)
	}
}

func TestCheckPageWarningsDoNotBlock(t *testing.T) {
	cases := []struct {
		name  string
		facts PageFacts
		code  string
	}{
		{"元素过多", PageFacts{SlideID: "s", ElementCount: 12, DurationMS: 6000, ClipCount: 6, BeatCount: 4}, RuleElementCount},
		{"留白不足", PageFacts{SlideID: "s", OccupiedRatio: 0.8}, RuleWhitespace},
		{"节奏偏密", PageFacts{SlideID: "s", DurationMS: 6000, ClipCount: 24, BeatCount: 4}, RuleDensity},
		{"节拍过多", PageFacts{SlideID: "s", DurationMS: 6000, BeatCount: 7}, RuleBeat},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := CheckPage(c.facts)
			if !r.OK() {
				t.Fatalf("warn 不应阻断，实际 %+v", r.Findings)
			}
			if got := r.BySeverity(SeverityWarn); len(got) != 1 || got[0].Code != c.code {
				t.Fatalf("期望 1 条 %s warn，实际 %+v", c.code, got)
			}
		})
	}
}

func TestCheckPageDensityHardLimit(t *testing.T) {
	r := CheckPage(PageFacts{SlideID: "s", DurationMS: 6000, ClipCount: 42, BeatCount: 4})
	if r.OK() {
		t.Fatal("7 clips/s 应阻断")
	}
	f := r.Findings[0]
	if f.Code != RuleDensity || f.Severity != SeverityError {
		t.Errorf("应为 %s/error，实际 %s/%s", RuleDensity, f.Code, f.Severity)
	}
	if f.Limit != HardMaxClipsPerSecond {
		t.Errorf("阈值 = %v，期望 %v", f.Limit, HardMaxClipsPerSecond)
	}
}

func TestCheckPageUnknownFactsSkipped(t *testing.T) {
	r := CheckPage(PageFacts{SlideID: "s"})
	if len(r.Findings) != 0 {
		t.Fatalf("全部未知时不应臆断，实际 %+v", r.Findings)
	}
	if !r.OK() {
		t.Error("全未知应放行")
	}
}

func TestCheckPageShortPageSkipsBeat(t *testing.T) {
	// 3s 页只有 2 拍是合理的，不该报警。
	r := CheckPage(PageFacts{SlideID: "s", DurationMS: 3000, BeatCount: 2})
	if len(r.Findings) != 0 {
		t.Errorf("短页不应检查节拍数，实际 %+v", r.Findings)
	}
}

func TestCheckPageDeterministic(t *testing.T) {
	f := PageFacts{
		SlideID: "s9", DurationMS: 6400, ElementCount: 11,
		MinBodyFontPx: 30, MinTextContrast: 4.1, OccupiedRatio: 0.72,
		ClipCount: 30, BeatCount: 8,
	}
	a, b := CheckPage(f), CheckPage(f)
	if !reflect.DeepEqual(a, b) {
		t.Error("同一输入两次度量结果不一致（度量必须纯函数）")
	}
	if len(a.Findings) < 5 {
		t.Errorf("这一页应命中多条规则，实际 %d 条: %+v", len(a.Findings), a.Findings)
	}
}

func TestCheckAllAndSummary(t *testing.T) {
	reports := CheckAll([]PageFacts{
		{SlideID: "a", MinBodyFontPx: 28},
		{SlideID: "b", OccupiedRatio: 0.9},
		{SlideID: "c", OccupiedRatio: 0.9},
	})
	if len(reports) != 3 {
		t.Fatalf("报告数 = %d，期望 3", len(reports))
	}
	sum := Summary(reports)
	if sum[RuleWhitespace] != 2 {
		t.Errorf("留白规则命中 %d 次，期望 2", sum[RuleWhitespace])
	}
	if codes := SortedFindingCodes(sum); len(codes) != 2 || codes[0] != RuleFontSize {
		t.Errorf("规则编号排序异常: %v", codes)
	}
}
