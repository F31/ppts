package en

import (
	"regexp"
	"strings"

	"github.com/F31/textnorm/internal/contract"
)

// MoneyRule reads currency-prefixed amounts such as $250 and USD 1.5 into
// English speech with singular/plural currency names. The amount is read as a
// quantity (integer part) or a decimal; the currency name follows the amount
// with correct plurality. Bare integers are never assumed to be money — an
// explicit currency symbol/ISO prefix is required (conservative).
func MoneyRule() contract.CandidateRule {
	return expressionRule{
		id: "en.money", category: "money", priority: 13,
		re: enMoneyRe, convert: enMoneyToSpeech,
	}
}

var enMoneyRe = regexp.MustCompile(`(?:[$¥￥]|USD|EUR|GBP|JPY|CNY|AUD|港币|人民币)\s*[+-]?[0-9]+(?:\.[0-9]+)?`)

// enMoneyToSpeech converts a currency-prefixed amount to English speech.
func enMoneyToSpeech(matched string) (string, bool) {
	s := strings.TrimSpace(matched)
	cur := ""
	rest := s
	for _, c := range []string{"人民币", "港币", "USD", "EUR", "GBP", "JPY", "CNY", "AUD", "¥", "￥", "$"} {
		if strings.HasPrefix(rest, c) {
			cur = c
			rest = strings.TrimSpace(strings.TrimPrefix(rest, c))
			break
		}
	}
	if rest == "" {
		return "", false
	}
	// Read the number part; decimals keep their fractional digits and force
	// plural currency (non-unit fractional amounts are not "one").
	spoken, ok := enMoneyAmount(rest)
	if !ok {
		return "", false
	}
	if strings.Contains(rest, ".") {
		curName, _, _ := enCurrencyName(cur)
		return spoken + " " + curName + "s", true
	}
	return spoken + " " + pluralMoney(cur, rest), true
}

// enMoneyAmount reads the numeric part of a currency amount.
func enMoneyAmount(rest string) (string, bool) {
	neg := false
	if strings.HasPrefix(rest, "-") {
		neg = true
		rest = strings.TrimPrefix(rest, "-")
	} else {
		rest = strings.TrimPrefix(rest, "+")
	}
	if strings.Contains(rest, ".") {
		// Reuse decimal speech; strip trailing zeros so $1.50 → "one point five".
		whole, frac, _ := strings.Cut(rest, ".")
		frac = strings.TrimRight(frac, "0")
		if frac == "" {
			return enIntegerToWords(strings.TrimLeft(whole, "0"))
		}
		spoken, ok := enDecimalSpeech(whole + "." + frac)
		if !ok {
			return "", false
		}
		if neg {
			return "minus " + spoken, true
		}
		return spoken, true
	}
	spoken, ok := enIntegerToWords(strings.TrimLeft(rest, "0"))
	if !ok {
		return "", false
	}
	if neg {
		return "minus " + spoken, true
	}
	return spoken, true
}

// enCurrencyName returns the base currency name (singular form).
func enCurrencyName(sym string) (string, bool, bool) {
	switch sym {
	case "$", "USD":
		return "US dollar", true, true
	case "¥", "￥", "JPY":
		return "Japanese yen", false, false
	case "EUR":
		return "euro", true, true
	case "GBP":
		return "British pound", true, true
	case "CNY", "人民币":
		return "Chinese yuan", true, true
	case "AUD":
		return "Australian dollar", true, true
	case "港币":
		return "Hong Kong dollar", true, true
	}
	return "", false, false
}

// pluralMoney returns the currency name adjusted for the amount's plurality.
func pluralMoney(sym, rest string) string {
	base, hasPlural, _ := enCurrencyName(sym)
	if !hasPlural {
		// Uncountable currencies (yen) never inflect.
		return base
	}
	if strings.TrimLeft(rest, "+-") == "1" {
		return base
	}
	return base + "s"
}
