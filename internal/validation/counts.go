package validation

// 数字形态：把「目标里多出来的数字」按形态分流，而不是一刀切判违规。
//
// 背景（2026-11-02）：slide-262 的页面列了四个用例（企业培训 / 在线课程 / 销售路演 /
// 无障碍），模型写成「覆盖 4 类场景」，校验器把它判成「新增数字 4」，两轮修补都过不了，
// 整页回退成页面原文——用户拿到的是一堆分点标签，而不是能念的讲稿。
//
// 问题不在"该不该严"，而在**一条规则管了两件不同的事**：
//   · 实测事实（64GB → 96GB、40% → 60%）改一个数就是错，必须零容忍；
//   · 概括计数（「4 类场景」）是模型对页面已列条目的转述，来源在素材里，只是没有以数字形式出现。
// 把两者混在一起，只能二选一：要么放过真实的数字篡改，要么把正常概括判成编造。
//
// 因此形态在**抽取阶段**判定一次（numberForm），处置在**规则表**里声明（rules.json 的 counts），
// 两者都不散落在调用方：换语言、换领域、换严格程度都只改配置。
//
// 为什么**不**挪进 textnorm（2026-11-02 评审，见 docs/adr/ADR-027）：那边按「怎么念」分类
// （date / decimal / percent / telephone，依据是读音歧义），这边按「该不该拦」分类
// （fact / count / ordinal，依据是业务裁定）。同一批 token 上的两条正交分类轴，合并成一个
// 枚举谁都用不顺手：textnorm 永远用不到 count，门禁也永远用不到 telephone。另外两侧的
// 变更驱动不同（这边是门禁口径 + 回放基线，那边是公开契约 + profile 版本 + ConfigHash），
// 合并后改一句后缀表也要触发 TTS 侧的版本流程。真正重复的只有**词表**（序号前缀 / 单位 /
// 量词族），词表可以各自演进，判定逻辑不行——`FormatCountHint` 与校验器同源是这套门禁的
// 核心不变量，拆开必然漂移。
//
// 若将来出现第二个消费者（别的服务也要做源/目标保真判定），正确做法是把 validation
// **整体**抽成独立模块（判定 + rules.json + 回放语料一起走），而不是拆一半给 textnorm。

import (
	"strconv"
	"strings"
)

// ordinalPrefix 报告数字前（跳过空白）是否紧邻序数标记。
// 「第 2 步」「第2步」「No.3」都算；「3 步」不算——后者没有前缀，按计数处理。
func ordinalPrefix(s string, start int) bool {
	i := start
	for i > 0 && (s[i-1] == ' ' || s[i-1] == '\t') {
		i--
	}
	for _, p := range ordinalPrefixes {
		if len(p) <= i && strings.EqualFold(s[i-len(p):i], p) {
			return true
		}
	}
	return false
}

// countSuffix 报告数字后（跳过空白）是否紧邻计数后缀。
// 量词（个/项/条/种/类…）由 measureAfter 覆盖；这里只处理量词之外的写法，
// 如「4 大场景」「3 个维度」中的「大」「维度」。
func countSuffix(s string, numEnd int) bool {
	i := numEnd
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	rest := s[i:]
	for _, suf := range countSuffixes {
		if strings.HasPrefix(rest, suf) {
			return true
		}
	}
	return false
}

// numberForm 判定一个数字的形态。**带单位的一律是事实**：单位把数字绑定到了可测量的
// 物理量上（64GB、12ms、40%），这类数字无论前后缀如何都不该被"概括计数"豁免掉。
//
// 判定顺序即优先级：事实 → 序号 → 计数 → 事实（裸数字按保守侧处理）。
func numberForm(s string, start, numEnd int, hasUnit bool, measure string) string {
	if hasUnit {
		return FormFact
	}
	if ordinalPrefix(s, start) {
		return FormOrdinal
	}
	if measure != "" || countSuffix(s, numEnd) {
		return FormCount
	}
	return FormFact
}

// toleratedInsertion 判断"目标里有、素材里没有"的数字是否按策略放行。
//
// 注意：**永远只处理插入侧**。素材里的数字在目标里消失（missing）一律判违规——
// 那才是"事实被删"的方向。计数的改动（3 → 4）会同时表现为 missing 3，仍被拦住。
func toleratedInsertion(e Entity, p Counts) bool {
	if e.Kind != "number" {
		return false
	}
	switch e.Form {
	case FormOrdinal:
		return p.InsertedOrdinals == ModeWarn || p.InsertedOrdinals == ModeAllow
	case FormCount:
		if p.InsertedCounts != ModeWarn && p.InsertedCounts != ModeAllow {
			return false
		}
		if p.MaxValue <= 0 {
			// 规则表校验要求启用豁免时必须给上限；这里对应"不限制"的显式配置。
			return true
		}
		v, err := strconv.ParseFloat(e.Text, 64)
		return err == nil && v <= p.MaxValue
	default:
		return false
	}
}

// FormatCountHint 返回「数字」这条硬约束的提示词文本，**与校验器共用同一份策略**。
//
// 为什么要由校验器生成：指令与校验器各写一份，迟早漂移（M1/M3 的教训）。策略放开了
// 概括计数，指令却仍写"不要计数"，模型就会把「四个用例」硬拆成不含数字的别扭句子；
// 反过来策略没放开、指令却允许，则会出现"照着指令写却被判违规"的假失败。
func FormatCountHint() string {
	countsOK := counts.InsertedCounts == ModeWarn || counts.InsertedCounts == ModeAllow
	ordinalsOK := counts.InsertedOrdinals == ModeWarn || counts.InsertedOrdinals == ModeAllow

	switch {
	case countsOK && ordinalsOK:
		return "② 原文没有出现的数字不得用于陈述事实；概括本页已有条目时可以用「4 类」「3 个方面」" +
			"这类计数与必要的序号，但数量必须与页面条目一致，带单位的实测数值一律不得新增；"
	case countsOK:
		return "② 原文没有出现的数字不得用于陈述事实；概括本页已有条目时可以用「4 类」「3 个方面」" +
			"这类计数，但数量必须与页面条目一致；不要自行添加分步序号（如「第 1 步」）；" +
			"带单位的实测数值一律不得新增；"
	case ordinalsOK:
		return "② 原文没有出现的数字原则上不得新增；确有必要时可按顺序编号，但不要替换或改动" +
			"原文已有的数字、单位与型号；"
	default:
		return "② 原文没有出现的数字一律不得新增——不要自行编号或计数（例如不要写「第 1 步」「3 个要点」）；"
	}
}
