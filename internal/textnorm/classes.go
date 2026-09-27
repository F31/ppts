package textnorm

import (
	"regexp"
	"strings"
)

// 本文件提供 PPT 高频领域的声明式类别规则（V3.0 §2.2，对标 itntext 的类别 grammar）。
//
// 每类 = 一条 Pattern：Match 识别（tagger）+ Verbalize 产出中文读法（verbalizer）。
// 类别规则默认不注册，由装配方按开关注册（V3.0 §2.2 独立开关，零回退）。
// 与 NumberRule 的边界（只处理独立成词数字）不同：类别规则的正则自带上下文边界
// （如 "12%" 的 % 后缀、日期分隔符），不依赖邻接字符判定。

// DecimalToCN 把数字字符串（整数或小数）转中文读法。
//   - 整数部分复用 NumToCN（数量位读，≤12 位）；
//   - 小数部分逐位读（13.5 → 十三点五）；
//   - 支持前导正负号（-3.5 → 负三点五）。
//
// V3.0 起作为公共库（decimal2cn）导出，供 percent/money 等类别复用。
func DecimalToCN(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	} else if s[0] == '+' {
		s = s[1:]
	}
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
	var sb strings.Builder
	if neg {
		sb.WriteString("负")
	}
	sb.WriteString(cnWhole)
	if frac != "" {
		sb.WriteString("点")
		for _, d := range frac {
			sb.WriteRune(digitCN(d))
		}
	}
	return sb.String(), true
}

// PercentPattern 百分比：13.5% → 百分之十三点五。
func PercentPattern() Pattern {
	return Pattern{
		Category: "percent",
		Priority: 12, // 先于 number(20)：避免数字规则先吞掉百分比中的数字
		Match:    regexp.MustCompile(`[+-]?[0-9]+(?:\.[0-9]+)?%`),
		Verbalize: func(matched string, _ *Context) (string, bool) {
			num := strings.TrimSuffix(matched, "%")
			cn, ok := DecimalToCN(num)
			if !ok {
				return "", false
			}
			return "百分之" + cn, true
		},
	}
}

// DecimalPattern 小数：13.5 → 十三点五。
// 仅匹配"小数点两侧均有数字"的形式。
//
// 与日期/百分比的冲突靠【优先级编排】解决（Go regexp RE2 无 lookahead）：
// date(11) < percent(12) < money(13) < unit(14) < decimal(15)——date/percent/money
// 先消费 `2024/01/28`、`6.3%`、`¥13.5`，转中文后 decimal 不再匹配。
// 全部类别先于 number(20)，避免数字规则破坏日期/百分比/金额。
// 仅注册 decimal 而不注册 date/percent 时，日期会被吞为小数（组合约束，装配方应配套注册）。
func DecimalPattern() Pattern {
	return Pattern{
		Category: "decimal",
		Priority: 15, // 最末类别（先于 number 20，但日期/百分比/金额/单位优先，见各注释）
		Match:    regexp.MustCompile(`[+-]?[0-9]+\.[0-9]+`),
		Verbalize: func(matched string, _ *Context) (string, bool) {
			cn, ok := DecimalToCN(matched)
			if !ok {
				return "", false
			}
			return cn, true
		},
	}
}

// DatePattern 日期：2002/01/28、2002-1-28、2002.01.28 → 二零零二年一月二十八日。
// 仅匹配完整"年/月/日"三部分（V3.0 §2.2 优先级 P1）。
func DatePattern() Pattern {
	return Pattern{
		Category: "date",
		Priority: 11, // 最优先类别：先于 percent/decimal，避免 `2024.01.28` 被吞为小数
		Match:    regexp.MustCompile(`[0-9]{4}[-/\.][0-9]{1,2}[-/\.][0-9]{1,2}`),
		Verbalize: func(matched string, _ *Context) (string, bool) {
			sep := sepOf(matched)
			if sep == 0 {
				return "", false
			}
			parts := strings.Split(matched, string(sep))
			if len(parts) != 3 {
				return "", false
			}
			year, ok1 := YearToCN(parts[0])
			month, ok2 := NumToCN(strings.TrimLeft(parts[1], "0"))
			day, ok3 := NumToCN(strings.TrimLeft(parts[2], "0"))
			if !ok1 || !ok2 || !ok3 {
				return "", false
			}
			return year + "年" + month + "月" + day + "日", true
		},
	}
}

// sepOf 检测日期分隔符（/ - .）；非法（无分隔符）返回 0。
func sepOf(s string) byte {
	for _, sep := range []byte{'-', '/', '.'} {
		if strings.IndexByte(s, sep) >= 0 {
			return sep
		}
	}
	return 0
}

// MoneyPattern 金额：¥13.5、$250、13.5万元 → 人民币十三点五 / 美元二百五十 / 人民币十三点五万。
// 币种映射表（对标 itntext 的 money graph）：¥/￥→人民币、$→美元、A$→澳元、HKD/港元→港元。
// "数字+万"（无币种前缀）按人民币读（"万"是人民币高频量词，PPT 场景常见）。
// 其余无币种纯数字不展开（与 decimal/quantity 区分，避免与 NumberRule 冲突）。
// RE2 无 lookahead：用 alternation 表达「币种 或 万 至少其一」。
func MoneyPattern() Pattern {
	return Pattern{
		Category: "money",
		Priority: 13, // 先于 decimal(15)：`¥13.5` 的 13.5 不被小数类单独吞掉
		Match: regexp.MustCompile(
			`(?:(?:¥|￥|\$|A\$|HKD|USD|港币|人民币)\s*[+-]?[0-9]+(?:\.[0-9]+)?(?:万)?|[+-]?[0-9]+(?:\.[0-9]+)?万)`,
		),
		Verbalize: func(matched string, _ *Context) (string, bool) {
			m := strings.TrimSpace(matched)
			// 提取币种前缀与数字。
			cur := ""
			rest := m
			for _, c := range []string{"人民币", "港币", "HKD", "USD", "A$", "$", "¥", "￥"} {
				if strings.HasPrefix(rest, c) {
					cur = c
					rest = strings.TrimSpace(strings.TrimPrefix(rest, c))
					break
				}
			}
			if rest == "" {
				return "", false
			}
			wan := false
			if strings.HasSuffix(rest, "万") {
				wan = true
				rest = strings.TrimSuffix(rest, "万")
			}
			cn, ok := DecimalToCN(rest)
			if !ok {
				return "", false
			}
			name := currencyName(cur)
			if name == "" {
				name = "人民币" // 无币种前缀的"万"按人民币
			}
			var sb strings.Builder
			sb.WriteString(name)
			sb.WriteString(cn)
			if wan {
				sb.WriteString("万")
			}
			return sb.String(), true
		},
	}
}

// currencyName 币种符号 → 中文币种名（数据驱动映射表，对标 itntext TSV；M5 可挪运营后台）。
func currencyName(sym string) string {
	switch sym {
	case "¥", "￥", "人民币":
		return "人民币"
	case "$", "USD":
		return "美元"
	case "A$":
		return "澳元"
	case "HKD", "港币":
		return "港元"
	}
	return ""
}

// UnitPattern 数值+单位：25kg → 二十五千克（数字部分展开，单位词保持）。
// 覆盖 PPT 高频物理单位（kg/g/t/°C/°F/m/cm/km/mm/ms/Hz/万/kWh），避免吞入型号。
// 注意：GHz/MHz/kHz 等由平台种子词典处理（字面替换），不入本正则。
func UnitPattern() Pattern {
	return Pattern{
		Category: "unit",
		Priority: 14, // 先于 decimal(15)：`25kg` 的 25 不被小数类单独吞掉
		Match:    regexp.MustCompile(`[+-]?[0-9]+(?:\.[0-9]+)?\s*(kg|g|t|°C|°F|m|cm|km|mm|ms|Hz|万|kWh)\b`),
		Verbalize: func(matched string, _ *Context) (string, bool) {
			// 拆数字与单位：数字部分转中文，单位保持原样。
			trimmed := strings.TrimSpace(matched)
			i := 0
			for i < len(trimmed) && ((trimmed[i] >= '0' && trimmed[i] <= '9') || trimmed[i] == '.' || trimmed[i] == '-' || trimmed[i] == '+') {
				i++
			}
			num, unit := trimmed[:i], trimmed[i:]
			cn, ok := DecimalToCN(num)
			if !ok {
				return "", false
			}
			return cn + unit, true
		},
	}
}
