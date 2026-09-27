package textnorm

import (
	"strings"
	"testing"
)

// 场景回归语料库（V3.0 §4.3，对标 itntext 的 FULL_SCENARIO_COMPARE.csv）。
//
// 每行 = PPT 高频讲稿句式 + 期望 effectiveText（送 TTS 的朗读文本）。
// 引擎 = 词典 + 全部类别（percent/decimal/date/money/unit）+ 数值（quantity），
// 即生产开启全能力的组合形态。CI 每次全量跑，失败即回归阻断。
//
// 覆盖维度：
//   - 标记（读音/停顿）
//   - 词典（专有名词）
//   - 类别（百分比/小数/日期/金额/单位）
//   - 数值（裸整数兜底）
//   - 组合（同段多类别不吞字）
//   - 边界（型号不吞、未命中透传）

type corpusCase struct {
	in   string
	want string
}

var scenarioCorpus = []corpusCase{
	// --- 标记 ---
	{"RTX5090‖性能强劲", "RTX5090性能强劲"},
	{"首都〔读：shǒu dū〕北京", "首都shǒu dū北京"},

	// --- 词典 ---
	{"CUDA 加速推理", "库达 加速推理"},
	{"支持 5GHz 频段", "支持 5吉赫兹 频段"}, // token 隔离：5 不展开（缺陷 B 修复）
	{"搭载 RTX5090", "搭载 RTX5090"},       // 型号不吞

	// --- 类别 ---
	{"同比增长 6.3%", "同比增长 百分之六点三"},
	{"毛利率 25%", "毛利率 百分之二十五"},
	{"误差 1.5 毫米", "误差 一点五 毫米"},
	{"2024/03/15 上线", "二零二四年三月十五日 上线"},
	{"2002-1-28 发布", "二零零二年一月二十八日 发布"},
	{"售价 ¥250", "售价 人民币二百五十"},
	{"收入 13.5万元", "收入 人民币十三点五万元"},
	{"重达 25kg", "重达 二十五kg"},

	// --- 数值（裸整数兜底） ---
	{"涨价到 2024 年", "涨价到 二千零二十四 年"},
	{"项目 3 个", "项目 三 个"},

	// --- 组合（同段多类别不吞字） ---
	{"2024/03/15 营收 ¥250 增长 6.3%", "二零二四年三月十五日 营收 人民币二百五十 增长 百分之六点三"},
	{"2024/03/15 增长 6.3% 重达 25kg", "二零二四年三月十五日 增长 百分之六点三 重达 二十五kg"},

	// --- 边界：未命中透传 ---
	{"普通文本没有命中词", "普通文本没有命中词"},
}

// engineAllFeatures 构造生产全能力引擎：词典 + 全部类别 + 数值。
func engineAllFeatures() *Engine {
	e := New()
	e.RegisterRule("definition", 10, DefinitionRule(fakeDict{"CUDA": "库达", "GHz": "吉赫兹"}))
	e.RegisterPattern(PercentPattern())
	e.RegisterPattern(DatePattern())
	e.RegisterPattern(MoneyPattern())
	e.RegisterPattern(UnitPattern())
	e.RegisterPattern(DecimalPattern())
	e.RegisterDigitRule("number", 20, NumberRule(NumberModeQuantity))
	e.Build()
	return e
}

// TestScenarioCorpus 全语料回归：逐条断言 effectiveText 逐字节一致。
func TestScenarioCorpus(t *testing.T) {
	e := engineAllFeatures()
	for i, c := range scenarioCorpus {
		got := e.Run(c.in, "zh-CN").Text
		if got != c.want {
			t.Errorf("corpus[%d] %q: got %q want %q", i, c.in, got, c.want)
		}
	}
}

// TestScenarioCorpusIndexMapCoverage 组合场景必须产出 IndexMap 且全覆盖原文位置
// （逐字高亮依赖精确映射；V3.0 §1.2.4）。
func TestScenarioCorpusIndexMapCoverage(t *testing.T) {
	e := engineAllFeatures()
	for _, c := range scenarioCorpus {
		res := e.Run(c.in, "zh-CN")
		if res.Text != c.want {
			continue // 文本错误已由 TestScenarioCorpus 捕获
		}
		// 发生字长变化（显示字数 ≠ 朗读字数）的用例必须可精确映射。
		// 等长改写（如 GHz→吉赫兹）不产生 IndexMap，属 V2.8 既有设计。
		display := StripMarkers(c.in)
		if runeLen(display) == runeLen(res.Text) {
			continue
		}
		if res.IndexMap == nil || res.IndexMap.Empty() {
			t.Errorf("corpus %q: effectiveText changed but no IndexMap", c.in)
			continue
		}
		// 每个显示位置都可映射到朗读文本（原→派生 全覆盖）。
		for src := 0; src < runeLen(display); src++ {
			if _, ok := res.IndexMap.MapSrcToDst(src); !ok {
				t.Errorf("corpus %q: MapSrcToDst(%d) not covered (segs %v)", c.in, src, res.IndexMap.Segments)
			}
		}
	}
}

// TestScenarioCorpusNoRegressMarkers 标记剥离与类别不互相破坏：读音内容不被类别规则改写。
func TestScenarioCorpusNoRegressMarkers(t *testing.T) {
	e := engineAllFeatures()
	res := e.Run("支持〔读：5GHz〕频段", "zh-CN")
	if !strings.Contains(res.Text, "5GHz") {
		t.Fatalf("reading span content was rewritten: %q", res.Text)
	}
}
