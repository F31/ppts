package auditcoverage

import (
	"path/filepath"
	"testing"
)

// TestScanFixtureRecognizesAllThreeStyles 要求三种埋点写法都能被认出来。
//
// 这不是在测解析器"能不能工作"，而是在锁住"漏一类写法"这个失效模式：
// 只认字面量 → 常量写法全丢；不建助手形参索引 → 经助手下传的动作全丢
// （真实仓库里这种写法占了一大半，且肉眼盘点最难发现）。
func TestScanFixtureRecognizesAllThreeStyles(t *testing.T) {
	sites, err := Scan(filepath.Join("testdata"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := map[string]int{
		"widget.create": 1, // 直接字面量
		"widget.delete": 2, // 常量（直接构造 1 处 + 经过助手 1 处）
		"widget.update": 1, // 经过助手的字面量
	}
	got := map[string]int{}
	for _, s := range sites {
		got[s.Action]++
	}
	for action, n := range want {
		if got[action] != n {
			t.Errorf("action %q: got %d site(s), want %d", action, got[action], n)
		}
	}
	// 反方向：不该多出没写过的动作名。多出来说明解析器把别的字符串误当成动作了
	// （首版就把 surface 参数 "shared.meta" 误报成动作名）。
	for action := range got {
		if action == DynamicAction {
			continue
		}
		if _, ok := want[action]; !ok {
			t.Errorf("unexpected action %q — extractor is picking up non-action strings", action)
		}
	}
}

// TestScanFixtureUsesExplicitPlaceholderForDynamic 要求"解析不出来"必须显式占位。
func TestScanFixtureUsesExplicitPlaceholderForDynamic(t *testing.T) {
	sites, err := Scan(filepath.Join("testdata"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var n int
	for _, s := range sites {
		if s.Action == DynamicAction {
			n++
			if s.DynamicName != "action" {
				t.Errorf("dynamic site at %s:%d has DynamicName %q, want %q", s.File, s.Line, s.DynamicName, "action")
			}
		}
	}
	// 3 处而非 2 处：① 助手 record 自己的 Action: action；② dynamic 里直接构造的 Action: action；
	// ③ dynamic 调用助手时动作名也是形参。助手定义体本身也算一处 —— 它确实写着动态 Action，
	// 列出来才能提醒"这里的取值要在调用点看"。
	if n != 3 {
		t.Errorf("dynamic sites = %d, want 3（助手定义体 + 直接构造 + 经助手调用）", n)
	}
}

// TestScanFixtureCallRangeCoversWholeCall 要求经过助手的埋点，区间覆盖整段调用。
// 调用方要在这段原文上做二级检查（匿名访问有没有把 token 一起写进去），
// 区间只包住动作实参就什么都查不到。
func TestScanFixtureCallRangeCoversWholeCall(t *testing.T) {
	sites, err := Scan(filepath.Join("testdata"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var found bool
	for _, s := range sites {
		if s.Kind != KindCall || s.Action != "widget.update" {
			continue
		}
		found = true
		width := s.End - s.Start
		// `record(ctx, rec, p.TenantID, p.UserID, "widget.update", "w-1")` 远长于实参本身。
		if width < len(`record(ctx, rec, p.TenantID, p.UserID, "widget.update", "w-1")`) {
			t.Errorf("call site range too narrow: %d bytes at %s:%d", width, s.File, s.Line)
		}
	}
	if !found {
		t.Fatal("no KindCall site with widget.update found")
	}
}
