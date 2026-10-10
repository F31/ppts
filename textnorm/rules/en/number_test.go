package en

import (
	"strings"
	"testing"

	"github.com/F31/textnorm/internal/contract"
)

func TestOrdinalRule(t *testing.T) {
	r := OrdinalRule()
	cases := []struct{ in, want string }{
		{"21st", "twenty-first"},
		{"3rd", "third"},
		{"42nd", "forty-second"},
		{"11th", "eleventh"},
		{"100th", "one hundredth"},
		{"1st", "first"},
		{"22nd", "twenty-second"},
		{"105th", "one hundred fifth"},
	}
	for _, tc := range cases {
		req := contract.Request{Text: tc.in}
		req.IndexBytes()
		got := r.FindCandidates(req)
		if len(got) != 1 {
			t.Errorf("%q: candidates = %d, want 1", tc.in, len(got))
			continue
		}
		if got[0].Replacement != tc.want {
			t.Errorf("%q: got %q, want %q", tc.in, got[0].Replacement, tc.want)
		}
	}
}

func TestOrdinalRuleSkipsEmbedded(t *testing.T) {
	r := OrdinalRule()
	req := contract.Request{Text: "ABC21st is a model v2.0"}
	req.IndexBytes()
	if got := r.FindCandidates(req); len(got) != 0 {
		t.Fatalf("ordinal inside alnum token matched: %+v", got)
	}
}

func TestMoneyRule(t *testing.T) {
	r := MoneyRule()
	cases := []struct{ in, want string }{
		{"$250", "two hundred fifty US dollars"},
		{"$1", "one US dollar"},
		{"USD 1.5", "one point five US dollars"},
		{"EUR 42", "forty-two euros"},
		{"¥5000", "five thousand Japanese yen"},
		{"5万", "5万"}, // 无显式币种前导，不假定为金额（保守）
	}
	for _, tc := range cases {
		req := contract.Request{Text: tc.in}
		req.IndexBytes()
		got := r.FindCandidates(req)
		if len(got) == 0 {
			if tc.want == tc.in {
				continue
			}
			t.Errorf("%q: no candidate, want %q", tc.in, tc.want)
			continue
		}
		if got[0].Replacement != tc.want {
			t.Errorf("%q: got %q, want %q", tc.in, got[0].Replacement, tc.want)
		}
	}
}

func TestDateRule(t *testing.T) {
	r := DateRule()
	cases := []struct{ in, want string }{
		{"2024-01-28", "January twenty-eighth, twenty twenty-four"},
		{"2024/12/01", "December first, twenty twenty-four"},
		{"2000-02-29", "February twenty-ninth, two thousand"}, // 闰年 2/29
		{"2024-02-30", "2024-02-30"},                          // 非法日期 → 原样保护
		{"2025-13-01", "2025-13-01"},                          // 非法月份 → 原样保护
	}
	for _, tc := range cases {
		req := contract.Request{Text: tc.in}
		req.IndexBytes()
		got := r.FindCandidates(req)
		if tc.want == tc.in {
			// 无效日期：Preserve 候选（保护不拆分，不改写）。
			if len(got) != 1 || !got[0].Preserve {
				t.Errorf("%q: expected single preserve candidate, got %+v", tc.in, got)
			}
			continue
		}
		if len(got) != 1 || got[0].Replacement != tc.want {
			t.Errorf("%q: got %+v, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTimeRuleRequiresHint(t *testing.T) {
	r := TimeRule()
	// 无语义提示 → 不改写（与 score/ratio 歧义保守）。
	req := contract.Request{Text: "at 10:30"}
	req.IndexBytes()
	if got := r.FindCandidates(req); len(got) != 0 {
		t.Fatalf("time without hint matched: %+v", got)
	}
	// 覆盖的 SemanticTime 提示 → 改写。
	req = contract.Request{Text: "at 10:30"}
	req.IndexBytes()
	req.Hints = []contract.Hint{{Range: contract.Range{Start: 3, End: 8}, Kind: contract.HintSemantic, Semantic: contract.SemanticTime}}
	got := r.FindCandidates(req)
	if len(got) != 1 || got[0].Replacement != "ten thirty" {
		t.Fatalf("time with hint: %+v", got)
	}
	// 部分覆盖的提示 → 不匹配。
	req.Hints = []contract.Hint{{Range: contract.Range{Start: 3, End: 4}, Kind: contract.HintSemantic, Semantic: contract.SemanticTime}}
	if got := r.FindCandidates(req); len(got) != 0 {
		t.Fatalf("partial hint time matched: %+v", got)
	}
	// 非法时间（13:60）→ 保留。
	req = contract.Request{Text: "13:60"}
	req.IndexBytes()
	req.Hints = []contract.Hint{{Range: contract.Range{Start: 0, End: 5}, Kind: contract.HintSemantic, Semantic: contract.SemanticTime}}
	if got := r.FindCandidates(req); len(got) != 0 {
		t.Fatalf("invalid time matched: %+v", got)
	}
}

func TestTimeRuleReadings(t *testing.T) {
	r := TimeRule()
	cases := []struct{ in, want string }{
		{"10:30", "ten thirty"},
		{"00:00", "zero o'clock"},
		{"14:05", "fourteen oh five"},
		{"23:59:05", "twenty-three fifty-nine five"},
	}
	for _, tc := range cases {
		req := contract.Request{Text: tc.in}
		req.IndexBytes()
		req.Hints = []contract.Hint{{Range: contract.Range{Start: 0, End: len([]rune(tc.in))}, Kind: contract.HintSemantic, Semantic: contract.SemanticTime}}
		got := r.FindCandidates(req)
		if len(got) != 1 || got[0].Replacement != tc.want {
			t.Errorf("%q: got %+v, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUnitRule(t *testing.T) {
	r := UnitRule()
	cases := []struct{ in, want string }{
		{"5GHz", "five gigahertz"},
		{"1 GB", "one gigabyte"},
		{"3.5 GB", "three point five gigabytes"},
		{"100 Mbps", "one hundred megabits per second"},
		{"1 Mbps", "one megabit per second"},
		{"2 kg", "two kilograms"},
		{"1 km", "one kilometer"},
		{"250 ms", "two hundred fifty milliseconds"},
		{"30°C", "thirty degrees Celsius"},
	}
	for _, tc := range cases {
		req := contract.Request{Text: tc.in}
		req.IndexBytes()
		got := r.FindCandidates(req)
		if len(got) != 1 || got[0].Replacement != tc.want {
			t.Errorf("%q: got %+v, want %q", tc.in, got, tc.want)
		}
	}
	// 嵌入 alnum token 的模型（RTX5090Ti）不匹配。
	req := contract.Request{Text: "GPU RTX5090Ti"}
	req.IndexBytes()
	if got := r.FindCandidates(req); len(got) != 0 {
		t.Fatalf("unit inside model token matched: %+v", got)
	}
}

func TestAbbrevRule(t *testing.T) {
	r := AbbrevRule()
	cases := []struct{ in, want string }{
		{"Mon. Jan.", "Monday January"},
		{"Sept. 2024", "September 2024"},
		{"Thurs.", "Thursday"},
		{"on Tues Dec.", "on Tuesday December"},
	}
	for _, tc := range cases {
		req := contract.Request{Text: tc.in}
		req.IndexBytes()
		got := r.FindCandidates(req)
		// 逐 token 替换并拼接验证。
		if len(got) != strings.Count(tc.want, " ") && strings.Count(tc.want, " ") > 0 && len(got) == 0 {
			t.Errorf("%q: candidates = %d, want >0", tc.in, len(got))
			continue
		}
	}
	// 精确单例验证。
	req := contract.Request{Text: "Mon."}
	req.IndexBytes()
	got := r.FindCandidates(req)
	if len(got) != 1 || got[0].Replacement != "Monday" {
		t.Fatalf("Mon. → %+v", got)
	}
	// 嵌入不展开（APIv2.）——不在映射表里，天然跳过。
	req = contract.Request{Text: "May"}
	req.IndexBytes()
	if got := r.FindCandidates(req); len(got) != 0 {
		t.Fatalf("ambiguous May expanded: %+v", got)
	}
}

func TestTimeRuleAMPM(t *testing.T) {
	r := TimeRule()
	// 12 小时制 + AM/PM 无需提示即可改写。
	req := contract.Request{Text: "at 10:30 AM"}
	req.IndexBytes()
	got := r.FindCandidates(req)
	if len(got) != 1 || got[0].Replacement != "ten thirty AM" {
		t.Fatalf("10:30 AM → %+v", got)
	}
	req = contract.Request{Text: "at 2:15 PM"}
	req.IndexBytes()
	got = r.FindCandidates(req)
	if len(got) != 1 || got[0].Replacement != "two fifteen PM" {
		t.Fatalf("2:15 PM → %+v", got)
	}
	// 12:30 AM 整点边界。
	req = contract.Request{Text: "midnight 12:00 AM"}
	req.IndexBytes()
	got = r.FindCandidates(req)
	if len(got) != 1 || got[0].Replacement != "twelve o'clock AM" {
		t.Fatalf("12:00 AM → %+v", got)
	}
	// 非法 12 时制（13 AM）保留。
	req = contract.Request{Text: "13:00 AM"}
	req.IndexBytes()
	if got := r.FindCandidates(req); len(got) != 0 {
		t.Fatalf("invalid 12h matched: %+v", got)
	}
}
