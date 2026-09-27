package textnorm

import (
	"strings"
)

// NumberMode 是数值读法模式。
type NumberMode int

const (
	// NumberModeYear 年份/型号直读：2024 → 二零二四（逐位）。
	NumberModeYear NumberMode = iota
	// NumberModeQuantity 数量位读：2024 → 二千零二十四（按十进制位权）。
	NumberModeQuantity
)

// NumberRule 构造"数值读法"规则（V2.8 §3.4 优先级 20-29，N2 可选，默认不注册）。
//
// 语义：把普通 span 内**独立成词的阿拉伯数字**改写成中文读法；
// 与字母/数字相邻的数字串不展开（避免把型号前缀如 RTX5090 的 5090 误读）。
// 只处理 ≤ 12 位的整数；过长原样透传（不假成功）。
//
// 为什么默认不注册：数值改读会改变 effectiveText（进而改 configHash → 音频缓存键），
// 且数值语义可能与讲稿一致性冲突；由装配方显式开启（V2.8 §8 数值语义默认不改写）。
//
// 注意：装配方请用 RegisterDigitRule 注册本规则，以登记 maskHasDigit 快筛掩码。
func NumberRule(mode NumberMode) Rule {
	toWords := YearToCN
	if mode == NumberModeQuantity {
		toWords = NumToCN
	}
	return func(ctx *Context) (string, bool) {
		if ctx.SpanKind != SpanKindText {
			return "", false
		}
		if !asciiDigitP(ctx.Text) {
			return "", false
		}
		// token 隔离（缺陷 B）：以【原始 span】为准构建独立数字串白名单。
		// 词典改写（如 GHz→吉赫兹）后 ctx.Text 中数字邻接字符可能从 ASCII 字母变汉字，
		// 若不隔离会把 5GHz 误判为独立数字展开成"五吉赫兹"。
		// 仅当 Text 已被前序规则改写（≠OrigText）时白名单才有差异；否则为恒真。
		var allow func(string) bool
		if ctx.OrigText != ctx.Text {
			allow = independentWhitelist(ctx.OrigText)
		}
		out, changed, subs := expandDigits(ctx.Text, toWords, allow)
		if !changed {
			return "", false
		}
		// 上报每个数字段的精确 原→派生 位置（span 局部坐标；引擎平移为全局）。
		if ctx.Record != nil {
			for _, s := range subs {
				ctx.Record(IndexSeg{
					SrcStart: s.srcStart, SrcEnd: s.srcEnd,
					DstStart: s.dstStart, DstEnd: s.dstEnd,
				})
			}
		}
		return out, true
	}
}

// independentWhitelist 返回"文本在原始 span 中独立成词"的白名单判定函数。
// 白名单按数字串文本值判定：同一数值在不同位置一个独立一个不独立时，
// 取宽松（任一独立即放行）——比误判展开更符合"读成中文"意图，且为低频场景。
// 语义：仅放行白名单中的数字串；空白名单（原始 span 无任何独立数字，如 "支持 5GHz 频段"）
// 表示【全部拦截】——这正是 token 隔离要防的：词典改写把 5GHz 变 5吉赫兹后，
// number 不得把 5 误展开为五。绝不能把空白名单退回"全放行"。
func independentWhitelist(orig string) func(string) bool {
	allow := make(map[string]struct{}, 8)
	for _, d := range scanIndependentDigits(orig) {
		allow[d] = struct{}{}
	}
	return func(digits string) bool {
		_, ok := allow[digits]
		return ok
	}
}

func asciiDigitP(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

type digitSub struct {
	srcStart, srcEnd int
	dstStart, dstEnd int
}

// scanIndependentDigits 扫描文本中所有"独立成词"的数字串（邻接判定与 expandDigits 完全一致），
// 返回各数字串的原文。供 NumberRule 做 token 隔离：以【原始 span】为准构建白名单，
// 避免词典改写（如 GHz→吉赫兹）把数字邻接字符从 ASCII 字母变成汉字后误判为独立。
func scanIndependentDigits(s string) []string {
	runes := []rune(s)
	var out []string
	i := 0
	for i < len(runes) {
		r := runes[i]
		if r >= '0' && r <= '9' {
			j := i
			for j < len(runes) && runes[j] >= '0' && runes[j] <= '9' {
				j++
			}
			leftOK := i == 0 || !asciiAlnumRune(runes[i-1])
			rightOK := j == len(runes) || !asciiAlnumRune(runes[j])
			if leftOK && rightOK {
				out = append(out, string(runes[i:j]))
			}
			i = j
			continue
		}
		i++
	}
	return out
}

// expandDigits 扫描独立数字串，逐个改读。边界规则：
//   - 两侧为 ASCII 字母/数字（型号前缀/后缀，如 RTX5090、5080Ti）→ 不展开；
//   - 两侧为中文（单位词：年/台/元…）→ 允许展开（2024年 → 二千零二十四年）。
//   - allow 白名单：仅展开文本在白名单中的数字串（token 隔离；nil 表示全部放行）。
//
// 返回 (改写后文本, 是否改写, 各数字段的 原→派生 偏移，span 局部坐标)。
// 实现为单趟线性：rune 切片 + 游标长度计数，避免反复 runeLen(sb.String()) 的 O(n²)。
func expandDigits(s string, toWords func(string) (string, bool), allow func(string) bool) (string, bool, []digitSub) {
	runes := []rune(s)
	var out []rune
	changed := false
	var subs []digitSub
	dstCount := 0 // 输出 rune 数游标
	i := 0
	for i < len(runes) {
		r := runes[i]
		if r >= '0' && r <= '9' {
			j := i
			for j < len(runes) && runes[j] >= '0' && runes[j] <= '9' {
				j++
			}
			leftOK := i == 0 || !asciiAlnumRune(runes[i-1])
			rightOK := j == len(runes) || !asciiAlnumRune(runes[j])
			if leftOK && rightOK {
				digits := string(runes[i:j])
				if allow == nil || allow(digits) {
					if words, ok := toWords(digits); ok {
						srcStart := i
						dstStart := dstCount
						out = append(out, []rune(words)...)
						dstCount += len([]rune(words))
						changed = true
						subs = append(subs, digitSub{
							srcStart: srcStart, srcEnd: j,
							dstStart: dstStart, dstEnd: dstCount,
						})
						i = j
						continue
					}
				}
			}
			out = append(out, runes[i:j]...)
			dstCount += j - i
			i = j
			continue
		}
		out = append(out, r)
		dstCount++
		i++
	}
	return string(out), changed, subs
}

// asciiAlnumRune 仅 ASCII 字母/数字视为"型号字符"；汉字与标点不拦截。
func asciiAlnumRune(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// YearToCN 年份直读：逐位 0-9 → 零一二三四五六七八九；前导零忽略（0024 → 二四）。
// V3.0 起作为公共库（year2cn）导出，供日期等类别复用。
func YearToCN(dec string) (string, bool) {
	if dec == "" || len(dec) > 12 {
		return "", false
	}
	trimmed := strings.TrimLeft(dec, "0")
	if trimmed == "" {
		return "零", true
	}
	var sb []rune
	for _, d := range []rune(trimmed) {
		sb = append(sb, digitCN(d))
	}
	return string(sb), true
}

func digitCN(d rune) rune {
	switch d {
	case '0':
		return '零'
	case '1':
		return '一'
	case '2':
		return '二'
	case '3':
		return '三'
	case '4':
		return '四'
	case '5':
		return '五'
	case '6':
		return '六'
	case '7':
		return '七'
	case '8':
		return '八'
	case '9':
		return '九'
	}
	return d
}

// NumToCN 数量位读：按十进制位权转中文数词（支持到 12 位）。
// V3.0 起作为公共库（num2cn）导出，供百分比/金额/单位等类别复用。
// 依普通话数词规则：11→十一，101→一百零一，2004→二千零四，10005→一万零五。
func NumToCN(dec string) (string, bool) {
	if dec == "" || len(dec) > 12 {
		return "", false
	}
	num := strings.TrimLeft(dec, "0")
	if num == "" {
		return "零", true
	}
	if len(num) > 12 {
		return "", false
	}
	return quantityWords([]rune(num)), true
}

// quantityWords 按 4 位分块（亿/万/个）转换 ≤12 位整数的标准中文读法。
//
// 规则：
//   - 块内位权：3=千、2=百、1=十、0=无；块名：bi=2→亿、bi=1→万；
//   - 跨块补零：某块有输出后遇到全零或前导零，在下一个非零前补一个"零"；
//   - 首位省略"一十"：仅在全局最高非零位恰处于十位且值为 1 时省略（10→十、十万、十亿）。
//
// 单趟线性：rune 切片 + 游标计数，无 O(n²)。
func quantityWords(ds []rune) string {
	single := []rune("零一二三四五六七八九")
	n := len(ds)
	blocks := (n + 3) / 4
	blockName := map[int]string{1: "万", 2: "亿"}

	var sb []rune
	pendingZero := false // 全局：写过任何数字后遇到零，在下一非零前补"零"
	wroteAny := false
	for bi := blocks - 1; bi >= 0; bi-- {
		lo := n - (bi+1)*4
		if lo < 0 {
			lo = 0
		}
		hi := n - bi*4 // 本块数字区间 [lo,hi)
		blockWrote := false
		for i := lo; i < hi; i++ {
			v := int(ds[i] - '0')
			posWithin := hi - i - 1 // 0=块内个位
			if v == 0 {
				if wroteAny {
					pendingZero = true
				}
				continue
			}
			if pendingZero {
				sb = append(sb, '零')
				pendingZero = false
			}
			// 全局首位为十位的"1"：省"一"（10→十、10万→十万、10亿→十亿）。
			omitLeadingOne := false
			if len(sb) == 0 && v == 1 && posWithin == 1 {
				omitLeadingOne = true
			}
			if !omitLeadingOne {
				sb = append(sb, single[v])
			}
			// 位权
			switch posWithin {
			case 3:
				sb = append(sb, '千')
			case 2:
				sb = append(sb, '百')
			case 1:
				sb = append(sb, '十')
			}
			blockWrote = true
			wroteAny = true
		}
		if blockWrote {
			if name, ok := blockName[bi]; ok {
				sb = append(sb, []rune(name)...)
			}
		}
	}
	if len(sb) == 0 {
		return "零"
	}
	return string(sb)
}
