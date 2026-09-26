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
func NumberRule(mode NumberMode) Rule {
	toWords := yearRead
	if mode == NumberModeQuantity {
		toWords = quantityRead
	}
	return func(ctx *Context) (string, bool) {
		if ctx.SpanKind != SpanKindText {
			return "", false
		}
		if !asciiDigitP(ctx.Text) {
			return "", false
		}
		out, changed, subs := expandDigits(ctx.Text, toWords)
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

// expandDigits 扫描独立数字串，逐个改读。边界规则：
//   - 两侧为 ASCII 字母/数字（型号前缀/后缀，如 RTX5090、5080Ti）→ 不展开；
//   - 两侧为中文（单位词：年/台/元…）→ 允许展开（2024年 → 二千零二十四年）。
//
// 返回 (改写后文本, 是否改写, 各数字段的 原→派生 偏移，span 局部坐标)。
func expandDigits(s string, toWords func(string) (string, bool)) (string, bool, []digitSub) {
	runes := []rune(s)
	var sb strings.Builder
	changed := false
	var subs []digitSub
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
				if words, ok := toWords(string(runes[i:j])); ok {
					srcStart := i
					dstStart := runeLen(sb.String())
					sb.WriteString(words)
					changed = true
					subs = append(subs, digitSub{
						srcStart: srcStart, srcEnd: j,
						dstStart: dstStart, dstEnd: runeLen(sb.String()),
					})
					i = j
					continue
				}
			}
			sb.WriteString(string(runes[i:j]))
			i = j
			continue
		}
		sb.WriteRune(r)
		i++
	}
	return sb.String(), changed, subs
}

// asciiAlnumRune 仅 ASCII 字母/数字视为"型号字符"；汉字与标点不拦截。
func asciiAlnumRune(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// yearRead 年份直读：逐位 0-9 → 零一二三四五六七八九；前导零忽略（0024 → 二四）。
func yearRead(dec string) (string, bool) {
	if dec == "" || len(dec) > 12 {
		return "", false
	}
	trimmed := strings.TrimLeft(dec, "0")
	if trimmed == "" {
		return "零", true
	}
	var sb strings.Builder
	for _, d := range []rune(trimmed) {
		sb.WriteRune(digitCN(d))
	}
	return sb.String(), true
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

// quantityRead 数量位读：按十进制位权转中文数词（支持到 12 位）。
// 依普通话数词规则：11→十一，101→一百零一，2004→二千零四，10005→一万零五。
func quantityRead(dec string) (string, bool) {
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
func quantityWords(ds []rune) string {
	single := []rune("零一二三四五六七八九")
	n := len(ds)
	blocks := (n + 3) / 4
	blockName := map[int]string{1: "万", 2: "亿"}

	var sb strings.Builder
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
				sb.WriteRune('零')
				pendingZero = false
			}
			// 全局首位为十位的"1"：省"一"（10→十、10万→十万、10亿→十亿）。
			omitLeadingOne := false
			if sb.Len() == 0 && v == 1 && posWithin == 1 {
				omitLeadingOne = true
			}
			if !omitLeadingOne {
				sb.WriteRune(single[v])
			}
			// 位权
			switch posWithin {
			case 3:
				sb.WriteRune('千')
			case 2:
				sb.WriteRune('百')
			case 1:
				sb.WriteRune('十')
			}
			blockWrote = true
			wroteAny = true
		}
		if blockWrote {
			if name, ok := blockName[bi]; ok {
				sb.WriteString(name)
			}
		}
	}
	if sb.Len() == 0 {
		return "零"
	}
	return sb.String()
}
