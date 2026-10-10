package en

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/F31/textnorm/internal/contract"
)

// TimeRule reads 24-hour HH:mm[:ss] forms into English speech. Clock notation
// is ambiguous with score/ratio forms, so a fully covering SemanticTime hint
// is required (conservative: without the hint nothing is rewritten).
func TimeRule() contract.CandidateRule {
	return gatedExpressionRule{
		expressionRule{
			id: "en.time", category: "time", priority: 14,
			re: enTimeRe, convert: enTimeToSpeech, sep: ':',
		},
		contract.SemanticTime,
	}
}

var enTimeRe = regexp.MustCompile(`[0-9]{1,2}:[0-9]{2}(?::[0-9]{2})?`)

// enTimeToSpeech converts "10:30" → "ten thirty", "14:05:20" → "fourteen oh five twenty".
func enTimeToSpeech(s string) (string, bool) {
	parts := strings.Split(s, ":")
	vals := make([]int, len(parts))
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || (i == 0 && n > 23) || (i > 0 && n > 59) {
			return "", false
		}
		vals[i] = n
	}
	hour, ok := enIntegerToWords(strconv.Itoa(vals[0]))
	if !ok {
		return "", false
	}
	if len(parts) == 2 && vals[1] == 0 {
		return hour + " o'clock", true
	}
	minute, ok := enClockMinute(vals[1])
	if !ok {
		return "", false
	}
	spoken := hour + " " + minute
	if len(parts) == 3 {
		sec, ok := enIntegerToWords(strconv.Itoa(vals[2]))
		if !ok {
			return "", false
		}
		spoken += " " + sec
	}
	return spoken, true
}

// enClockMinute reads a two-digit minute with a leading-zero "oh" glide
// (05 → "oh five", 30 → "thirty", 59 → "fifty-nine").
func enClockMinute(m int) (string, bool) {
	if m < 0 || m > 59 {
		return "", false
	}
	if m < 10 {
		return "oh " + enSmall[m], true
	}
	return enIntegerToWords(strconv.Itoa(m))
}