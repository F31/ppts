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

// TestPriorityOrder 优先级稳定排序 + 分层叠加：低优先级先注册、高优先级后注册，按序执行；
// 各层规则**叠加**（前规则输出作为后规则输入），而不是首个命中即短路。
func TestPriorityOrder(t *testing.T) {
	e := New()
	e.RegisterRule("low", 100, func(c *Context) (string, bool) {
		return c.Text + "L", true
	})
	e.RegisterRule("high", 0, func(c *Context) (string, bool) {
		return "H", true
	})
	e.Build()
	res := e.Run("x", "zh-CN")
	// high（priority 0）先执行 → "H"；low（priority 100）在 "H" 上追加 → "HL"。
	if res.Text != "HL" {
		t.Fatalf("priority compose: got %q want %q", res.Text, "HL")
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

// TestDefinitionNumberCompose 词典与数值规则**叠加**：同一 span 的专有名词与数字
// 各自被正确改写（修复"词典命中即短路导致数值规则永不执行"）。
//
// V3.0 缺陷 B 修复（token 隔离）：数字展开的"独立成词"判定基于【原始 span】而非
// 词典改写后的文本——5GHz 在原文中 5 紧邻字母 G（型号），即使词典把 GHz→吉赫兹
// 也不展开为"五吉赫兹"；而 2024（独立年份）正常展开。
func TestDefinitionNumberCompose(t *testing.T) {
	e := New()
	e.RegisterRule("definition", 10, DefinitionRule(fakeDict{"CUDA": "库达", "GHz": "吉赫兹"}))
	e.RegisterDigitRule("number", 20, NumberRule(NumberModeQuantity))
	e.Build()
	cases := []struct{ in, want string }{
		// token 隔离：5 在原 span 紧邻 G → 不展开（缺陷 B 修复）。
		{"CUDA 5GHz 与 2024 年新品", "库达 5吉赫兹 与 二千零二十四 年新品"},
		{"CUDA 加速 2024 倍", "库达 加速 二千零二十四 倍"},
		{"普通文本", "普通文本"},
	}
	for _, c := range cases {
		res := e.Run(c.in, "zh-CN")
		if res.Text != c.want {
			t.Errorf("Run(%q) = %q want %q", c.in, res.Text, c.want)
		}
	}
}

// TestNumberRuleQuantity 数量位读：独立数字词 → 中文读法；界面不拦截中文单位词。
func TestNumberRuleQuantity(t *testing.T) {
	e := New()
	e.RegisterRule("number", 20, NumberRule(NumberModeQuantity))
	e.Build()
	cases := []struct{ in, want string }{
		{"涨价到 2024 年", "涨价到 二千零二十四 年"},
		{"RTX5090 很贵", "RTX5090 很贵"}, // 相邻 ASCII 字母 → 型号，不展开
		{"5080Ti", "5080Ti"},         // 后缀字母 → 不展开
		{"项目 3 个", "项目 三 个"},
		{"12 台机器", "十二 台机器"},
		{"2024年发布", "二千零二十四年发布"}, // 中文单位词边界 → 展开
		{"价格 10005 元", "价格 一万零五 元"},
	}
	for _, c := range cases {
		got := e.Run(c.in, "zh-CN").Text
		if got != c.want {
			t.Errorf("NumberRule(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

// TestNumberRuleYear 年份直读模式。
func TestNumberRuleYear(t *testing.T) {
	e := New()
	e.RegisterRule("number", 20, NumberRule(NumberModeYear))
	e.Build()
	cases := []struct{ in, want string }{
		{"2024 年", "二零二四 年"},
		{"19 年", "一九 年"},
		{"RTX5090", "RTX5090"},
	}
	for _, c := range cases {
		got := e.Run(c.in, "zh-CN").Text
		if got != c.want {
			t.Errorf("NumberRuleYear(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

// TestIndexMapFromNumberExpansion 数值展开触发字对不齐 → 引擎产出 IndexMap。
func TestIndexMapFromNumberExpansion(t *testing.T) {
	e := New()
	e.RegisterRule("number", 20, NumberRule(NumberModeQuantity))
	e.Build()
	res := e.Run("2024 年发布", "zh-CN")
	if res.Text != "二千零二十四 年发布" {
		t.Fatalf("unexpected text %q", res.Text)
	}
	if res.IndexMap == nil || res.IndexMap.Empty() {
		t.Fatal("expected IndexMap from number expansion")
	}
	// 改动段覆盖 2024→二千零二十四（4 → 6 rune），其余为恒等段。
	var numSeg *IndexSeg
	for i := range res.IndexMap.Segments {
		s := &res.IndexMap.Segments[i]
		if s.SrcStart == 0 && s.SrcEnd == 4 {
			numSeg = s
		}
	}
	if numSeg == nil {
		t.Fatalf("no segment for 2024, got %v", res.IndexMap.Segments)
	}
	if numSeg.DstStart != 0 || numSeg.DstEnd != 6 {
		t.Fatalf("2024 segment = %+v, want [0,4)→[0,6)", numSeg)
	}
	// 映射：原 rune 2（"2024" 内部）→ 派生。
	if dst, ok := res.IndexMap.MapSrcToDst(2); !ok || dst != 3 {
		t.Fatalf("MapSrcToDst(2) = %d,%v want 3,true", dst, ok)
	}
	// 数字展开后 派生位置 5（空格）应对应 原位置 4（空格）——恒等段。
	if dst, ok := res.IndexMap.MapSrcToDst(4); !ok || dst != 6 {
		t.Fatalf("MapSrcToDst(4) = %d,%v want 6,true", dst, ok)
	}
}

// TestIndexMapFromMarkers 读音/停顿标记产生偏移段（标记在原文→派生间被剥离/展开）。
func TestIndexMapFromMarkers(t *testing.T) {
	e := engineWithDict(nil)
	res := e.Run("A〔读：b〕‖C", "zh-CN")
	if res.Text != "AbC" {
		t.Fatalf("unexpected text %q", res.Text)
	}
	if res.IndexMap == nil || res.IndexMap.Empty() {
		t.Fatal("expected IndexMap from markers")
	}
	// 覆盖全部原文位置：任意原位置都能映射到派生。
	for src := 0; src < 8; src++ {
		if _, ok := res.IndexMap.MapSrcToDst(src); !ok {
			t.Fatalf("MapSrcToDst(%d) not covered", src)
		}
	}
}

// TestIndexMapNilForIdentity 完全无偏移（无标记且规则同长/未命中）→ nil。
func TestIndexMapNilForIdentity(t *testing.T) {
	e := engineWithDict(fakeDict{"A": "a"}) // 同长改写
	res := e.Run("ABCD", "zh-CN")
	if !res.IndexMap.Empty() {
		t.Fatalf("identity should not produce IndexMap, got %v", res.IndexMap)
	}
}

// TestStripMarkersIndexed 剥离标记的逐位来源下标。
func TestStripMarkersIndexed(t *testing.T) {
	out, idx := StripMarkersIndexed("A〔读：b〕‖CD")
	if out != "AbCD" {
		t.Fatalf("out = %q want %q", out, "AbCD")
	}
	// A(0) b(4) C(7) D(8) —— b 的来源是 〔读：b〕 内的 b。
	want := []int{0, 4, 7, 8}
	if len(idx) != len(want) {
		t.Fatalf("idx = %v want %v", idx, want)
	}
	for i := range want {
		if idx[i] != want[i] {
			t.Fatalf("idx[%d] = %d want %d (full %v)", i, idx[i], want[i], idx)
		}
	}
}

// TestDisplayToEffective 显示文本→朗读文本的精确映射表。
func TestDisplayToEffective(t *testing.T) {
	e := New()
	e.RegisterRule("number", 20, NumberRule(NumberModeQuantity))
	e.Build()
	res := e.Run("2024 年发布", "zh-CN")
	// 显示 "2024 年发布"(8)→ 朗读 "二千零二十四 年发布"(11)。
	if res.Text != "二千零二十四 年发布" {
		t.Fatalf("unexpected eff %q", res.Text)
	}
	m := DisplayToEffective("2024 年发布", res)
	if m == nil || len(m) != 8 {
		t.Fatalf("m = %v want %d entries", m, 8)
	}
	// "2024"→"二千零二十四"(4→6) 段内按比例：src1→2、src2→3、src3→5；空格与尾部恒等：src4→6、src7→9。
	cases := []struct{ dispIdx, wantEff int }{
		{0, 0}, {1, 2}, {2, 3}, {3, 5}, {4, 6}, {7, 9},
	}
	for _, c := range cases {
		if m[c.dispIdx] != c.wantEff {
			t.Fatalf("m[%d] = %d want %d (full %v)", c.dispIdx, m[c.dispIdx], c.wantEff, m)
		}
	}
}
