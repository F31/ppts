// Package zh implements the Chinese spoken-language rules for the textnorm
// engine. Rules only depend on the shared contract; the root textnorm package
// re-exports them for callers that assemble configs from this module.
package zh

import (
	"strings"
)

// zhDigit maps an ASCII digit rune to its Chinese reading.
func zhDigit(d rune) rune {
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

// zhIntegerToSpeech reads a digit string by decimal place (quantity mode).
func zhIntegerToSpeech(s string) (string, bool) {
	if s == "" || len(s) > 12 {
		return "", false
	}
	trimmed := strings.TrimLeft(s, "0")
	if trimmed == "" {
		return "零", true
	}
	for _, d := range trimmed {
		if d < '0' || d > '9' {
			return "", false
		}
	}
	return zhQuantityWords([]rune(trimmed)), true
}

// zhQuantityWords converts a trimmed digit rune slice to quantity speech,
// inserting 零 between non-adjacent place values.
func zhQuantityWords(ds []rune) string {
	n := len(ds)
	blocks := (n + 3) / 4
	blockName := map[int]string{1: "万", 2: "亿"}
	var out []rune
	pendingZero := false
	wroteAny := false
	for bi := blocks - 1; bi >= 0; bi-- {
		lo := n - (bi+1)*4
		if lo < 0 {
			lo = 0
		}
		hi := n - bi*4
		blockWrote := false
		for i := lo; i < hi; i++ {
			v := int(ds[i] - '0')
			posWithin := hi - i - 1
			if v == 0 {
				if wroteAny {
					pendingZero = true
				}
				continue
			}
			if pendingZero {
				out = append(out, '零')
				pendingZero = false
			}
			if !(len(out) == 0 && v == 1 && posWithin == 1) {
				out = append(out, zhDigit(rune('0'+v)))
			}
			switch posWithin {
			case 3:
				out = append(out, '千')
			case 2:
				out = append(out, '百')
			case 1:
				out = append(out, '十')
			}
			blockWrote = true
			wroteAny = true
		}
		if blockWrote {
			if name, ok := blockName[bi]; ok {
				out = append(out, []rune(name)...)
			}
		}
	}
	if len(out) == 0 {
		return "零"
	}
	return string(out)
}

// zhNumberToSpeech reads a number token including an optional sign and decimal.
func zhNumberToSpeech(s string) (string, bool) {
	if strings.Contains(s, ".") {
		return zhDecimalToSpeech(s)
	}
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = strings.TrimPrefix(s, "-")
	} else {
		s = strings.TrimPrefix(s, "+")
	}
	spoken, ok := zhIntegerToSpeech(s)
	if !ok {
		return "", false
	}
	if neg {
		return "负" + spoken, true
	}
	return spoken, true
}

// zhYearToSpeech reads digits one at a time (year mode).
func zhYearToSpeech(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	var sb strings.Builder
	for _, d := range s {
		if d < '0' || d > '9' {
			return "", false
		}
		sb.WriteRune(zhDigit(d))
	}
	return sb.String(), true
}
