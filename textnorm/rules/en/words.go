// Package en implements the English spoken-language rules for the textnorm
// engine. The rules only depend on the shared contract; the root textnorm
// package re-exports them for callers that assemble configs from this module.
package en

import (
	"strings"
)

var enSmall = []string{
	"zero", "one", "two", "three", "four", "five", "six", "seven", "eight",
	"nine", "ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen",
	"sixteen", "seventeen", "eighteen", "nineteen",
}

var enTens = []string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}

var enScales = []string{"", "thousand", "million", "billion"}

func enDigitWord(d byte) string { return enSmall[int(d-'0')] }

// enChunkToWords reads a 1-3 digit chunk.
func enChunkToWords(s string) (string, bool) {
	switch len(s) {
	case 1:
		return enDigitWord(s[0]), true
	case 2:
		return enTwoDigits(s)
	default:
		if s[0] == '0' {
			return enChunkToWords(s[1:])
		}
		h := enDigitWord(s[0])
		rest := s[1:]
		if rest == "00" {
			return h + " hundred", true
		}
		r, _ := enTwoDigits(rest)
		return h + " hundred " + r, true
	}
}

func enTwoDigits(s string) (string, bool) {
	if s[0] == '0' {
		return enDigitWord(s[1]), true
	}
	v := int(s[0]-'0')*10 + int(s[1]-'0')
	if v < 20 {
		return enSmall[v], true
	}
	ten := int(s[0] - '0')
	if s[1] == '0' {
		return enTens[ten], true
	}
	return enTens[ten] + "-" + enDigitWord(s[1]), true
}

// enIntegerToWords reads a trimmed digit string by decimal place, up to the
// billion scale (12 digits).
func enIntegerToWords(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return "", false
		}
	}
	n := len(s)
	chunks := (n + 2) / 3
	var parts []string
	for ci := chunks - 1; ci >= 0; ci-- {
		hi := n - ci*3
		lo := hi - 3
		if lo < 0 {
			lo = 0
		}
		chunk := s[lo:hi]
		if chunkWords, ok := enChunkToWords(chunk); ok && chunkWords != "" && chunkWords != "zero" {
			parts = append(parts, chunkWords)
			if ci > 0 {
				parts = append(parts, enScales[ci])
			}
		}
	}
	if len(parts) == 0 {
		return "zero", true
	}
	return strings.Join(parts, " "), true
}

// enNumberToSpeech reads a digit token in quantity/year/digits mode.
func enNumberToSpeech(digits string, mode int) (string, bool) {
	if len(digits) > 12 {
		return "", false
	}
	trimmed := strings.TrimLeft(digits, "0")
	switch mode {
	case 1: // year
		return enYearToSpeech(digits), true
	case 2: // digits, preserving leading zeros
		if strings.TrimLeft(digits, "0") == "" {
			return "zero", true
		}
		words := make([]string, 0, len(digits))
		for i := 0; i < len(digits); i++ {
			words = append(words, enDigitWord(digits[i]))
		}
		return strings.Join(words, " "), true
	default: // quantity
		return enIntegerToWords(trimmed)
	}
}

// enYearToSpeech reads four-digit years by pairs (2024 → twenty twenty-four,
// 1905 → nineteen oh five, 2000 → two thousand).
func enYearToSpeech(digits string) string {
	trimmed := strings.TrimLeft(digits, "0")
	if len(trimmed) != 4 {
		w, _ := enIntegerToWords(trimmed)
		return w
	}
	first := trimmed[:2]
	second := trimmed[2:]
	if second == "00" {
		h, _ := enIntegerToWords(first)
		return h + " hundred"
	}
	f, _ := enIntegerToWords(first)
	var s string
	if second[0] == '0' {
		s = "oh " + enDigitWord(second[1])
	} else {
		s, _ = enTwoDigits(second)
	}
	return f + " " + s
}

// enDecimalSpeech reads a decimal form "3.14" as "three point one four".
func enDecimalSpeech(s string) (string, bool) {
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign = "minus "
		s = strings.TrimPrefix(s, "-")
	} else {
		s = strings.TrimPrefix(s, "+")
	}
	parts := strings.Split(s, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	whole, ok := enIntegerToWords(strings.TrimLeft(parts[0], "0"))
	if !ok {
		return "", false
	}
	words := []string{sign + whole, "point"}
	for i := 0; i < len(parts[1]); i++ {
		words = append(words, enDigitWord(parts[1][i]))
	}
	return strings.Join(words, " "), true
}

// enPercentSpeech reads a percent form such as "12.5%" as "twelve point five
// percent" and "12%" as "twelve percent".
func enPercentSpeech(s string) (string, bool) {
	num := strings.TrimSuffix(s, "%")
	spoken, ok := enDecimalSpeech(num)
	if !ok {
		spoken, ok = enIntegerToWords(strings.TrimLeft(num, "+-"))
	}
	if !ok {
		return "", false
	}
	return spoken + " percent", true
}
