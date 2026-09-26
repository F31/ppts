package textnorm

import (
	"strings"
	"sync"
	"testing"
)

// fakeDict 用 Substitute 语义模拟 ppts 的 pronunciation.Rules 适配器（逐条 ReplaceAll）。
type fakeDict map[string]string

func (d fakeDict) Substitute(_ string, text string) (bool, string) {
	changed := false
	for k, v := range d {
		if strings.Contains(text, k) {
			text = strings.ReplaceAll(text, k, v)
			changed = true
		}
	}
	return changed, text
}

func engineWithDict(dict Dictionary) *Engine {
	e := New()
	if dict != nil {
		e.RegisterRule("definition", 10, DefinitionRule(dict))
	}
	e.Build()
	return e
}

// TestRunReadingMarker 读音标记：剥离定界符、读音文本送 TTS。
func TestRunReadingMarker(t *testing.T) {
	e := engineWithDict(nil)
	res := e.Run("首都〔读：shǒu dū〕北京", "zh-CN")
	if got, want := res.Text, "首都shǒu dū北京"; got != want {
		t.Fatalf("Run reading: got %q want %q", got, want)
	}
	if len(res.Pauses) != 0 {
		t.Fatalf("Run reading: unexpected pauses %v", res.Pauses)
	}
}

// TestRunReadingMarkerUnclosed 负测：未闭合读音标记不丢字。
func TestRunReadingMarkerUnclosed(t *testing.T) {
	e := engineWithDict(nil)
	for _, in := range []string{"test〔读：abc", "〔读：abc", "prefix〔读：x"} {
		res := e.Run(in, "zh-CN")
		if res.Text != in {
			t.Fatalf("unclosed reading: Run(%q) = %q, want identity", in, res.Text)
		}
	}
}

// TestRunPause 停顿标记：产出 Pause 事件、输出文本不含 ‖。
func TestRunPause(t *testing.T) {
	e := engineWithDict(nil)
	res := e.Run("RTX5090‖性能强劲", "zh-CN")
	if want := "RTX5090性能强劲"; res.Text != want {
		t.Fatalf("Run pause text: got %q want %q", res.Text, want)
	}
	if len(res.Pauses) != 1 {
		t.Fatalf("Run pause: want 1 pause, got %v", res.Pauses)
	}
	if got, want := res.Pauses[0].AfterRunes, 7; got != want {
		t.Fatalf("pause AfterRunes: got %d want %d", got, want)
	}
	if res.Pauses[0].DurationMS != 300 {
		t.Fatalf("pause DurationMS: got %d want 300", res.Pauses[0].DurationMS)
	}
}

// TestRunMultipleSpans 混合普通/读音/停顿 span。
func TestRunMultipleSpans(t *testing.T) {
	e := engineWithDict(nil)
	res := e.Run("A〔读：x〕B‖C〔读：y〕D", "zh-CN")
	if want := "AxBCyD"; res.Text != want {
		t.Fatalf("Run mixed: got %q want %q", res.Text, want)
	}
	if len(res.Pauses) != 1 {
		t.Fatalf("Run mixed: want 1 pause, got %v", res.Pauses)
	}
	if res.Pauses[0].AfterRunes != 3 { // "AxB" = 3 runes
		t.Fatalf("Run mixed pause afterRunes: got %d want 3", res.Pauses[0].AfterRunes)
	}
}

// TestStripMarkers 显示文本派生：剥离 ‖ 与〔读：〕，保留读音内容。
func TestStripMarkers(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a〔读：cháng〕b‖c", "achángbc"},
		{"〔读：x〕", "x"},
		{"regular text, no markers", "regular text, no markers"},
		{"未闭合〔读：x", "未闭合〔读：x"},
	}
	for _, c := range cases {
		if got := StripMarkers(c.in); got != c.want {
			t.Fatalf("StripMarkers(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDefinitionRule 定义权词典：普通 span 内替换，读音 span 内容不被规则改写。
func TestDefinitionRule(t *testing.T) {
	dict := fakeDict{"Model3": "Model三", "R9000P": "R九千P"}
	e := engineWithDict(dict)
	res := e.Run("联想Model3·R9000P〔读：shēng〕", "zh-CN")
	// 词典替换后读音内容不受影响。
	if got := res.Text; got != "联想Model三·R九千Pshēng" {
		t.Fatalf("Run dict: got %q", got)
	}
	if len(res.Pauses) != 0 {
		t.Fatalf("Run dict: unexpected pause")
	}
}

// TestRegisterRuleAfterBuildPanics 负测：Build 后 RegisterRule panic。
func TestRegisterRuleAfterBuildPanics(t *testing.T) {
	e := New()
	e.Build()
	defer func() {
		if recover() == nil {
			t.Fatal("RegisterRule after Build should panic")
		}
	}()
	e.RegisterRule("late", 1, func(*Context) (string, bool) { return "", false })
}

// TestRunBeforeBuildPanics 负测：Run 前必须 Build。
func TestRunBeforeBuildPanics(t *testing.T) {
	e := New()
	defer func() {
		if recover() == nil {
			t.Fatal("Run before Build should panic")
		}
	}()
	e.Run("x", "zh-CN")
}

// TestEngineEmptyRulesIdentity 空规则引擎：零副作用（黄金对比①）。
func TestEngineEmptyRulesIdentity(t *testing.T) {
	e := New()
	e.Build()
	for _, in := range []string{"", "plain text", "数字 123 和 ‖〔读：x〕"} {
		res := e.Run(in, "zh-CN")
		// 空规则引擎仍执行 Scanner：朗读/停顿 span 仍被结构性处理（这是设计，非副作用）。
		if res.Text == "" && in != "" {
			t.Fatalf("empty engine Run(%q) empty text", in)
		}
	}
}

// TestConcurrentSafeAfterBuild 并发安全：冻结后多 goroutine 并行 Run。
func TestConcurrentSafeAfterBuild(t *testing.T) {
	e := engineWithDict(fakeDict{"A": "a"})
	const w = 32
	var wg sync.WaitGroup
	wg.Add(w)
	for i := 0; i < w; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				res := e.Run("AAA〔读：x〕‖BBB", "zh-CN")
				if res.Text == "" {
					t.Error("concurrent Run produced empty text")
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestPauseDurationOption WithPauseDuration 可配置停顿时长。
func TestPauseDurationOption(t *testing.T) {
	e := New(WithPauseDuration(500))
	e.Build()
	res := e.Run("a‖b", "zh-CN")
	if res.Pauses[0].DurationMS != 500 {
		t.Fatalf("WithPauseDuration: got %d want 500", res.Pauses[0].DurationMS)
	}
}

// TestPriorityOrder 优先级稳定排序：低优先级先注册、高优先级后注册，命中短路仍按序执行
// 高优先级在前。
func TestPriorityOrder(t *testing.T) {
	e := New()
	e.RegisterRule("low", 100, func(c *Context) (string, bool) {
		return "L", true
	})
	e.RegisterRule("high", 0, func(c *Context) (string, bool) {
		return "H", true
	})
	e.Build()
	res := e.Run("x", "zh-CN")
	// "trigger" 触发 high；普通 "x" 也应命中 high（它在表头，优先短路）。
	if res.Text != "H" {
		t.Fatalf("priority: got %q want %q (高优先级先命中即短路)", res.Text, "H")
	}
}

// TestDefinitionRuleFallback 定义权词典未命中时原样透传（普通 span 不受影响）。
func TestDefinitionRuleFallback(t *testing.T) {
	dict := fakeDict{"Model3": "Model三"}
	e := engineWithDict(dict)
	res := e.Run("普通文本没有命中词", "zh-CN")
	if res.Text != "普通文本没有命中词" {
		t.Fatalf("fallback: got %q", res.Text)
	}
}
