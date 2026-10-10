package en

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/F31/textnorm/internal/contract"
)

// TimeRule reads clock times into English speech.
//
// Two forms are supported:
//   - 12-hour with explicit AM/PM suffix (`10:30 AM` → `ten thirty AM`). The
//     suffix fully disambiguates clock notation from score/ratio forms, so no
//     semantic hint is required.
//   - 24-hour `HH:mm[:ss]` (`14:05` → `fourteen oh five`, `10:30` → `ten
//     thirty`). Without a suffix the bare `H:MM` form is ambiguous with
//     scores, so a covering `SemanticTime` hint is required.
//
// 12-hour forms with AM/PM never rewrite to 24-hour readings; the hour is read
// as-is (1..12) and the suffix is spoken (`AM` → "a m").
func TimeRule() contract.CandidateRule {
	return timeRule{}
}

type timeRule struct{}

func (timeRule) RuleID() string    { return "en.time" }
func (timeRule) RulePriority() int { return 14 }

var enTimeRe = regexp.MustCompile(`(?i)[0-9]{1,2}:[0-9]{2}(?::[0-9]{2})?(?:\s*(?:AM|PM))?`)

func (timeRule) FindCandidates(req contract.Request) []contract.Candidate {
	var out []contract.Candidate
	for _, loc := range enTimeRe.FindAllStringIndex(req.Text, -1) {
		if !contract.ASCIIBoundary(req.Text, loc[0], loc[1]) {
			continue
		}
		src := contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(loc[1])}
		// 忽略四组件时钟（如 10:30:20:10）。
		if loc[0] > 0 && req.Text[loc[0]-1] == ':' {
			continue
		}
		matched := req.Text[loc[0]:loc[1]]
		hasSuffix := hasAMPM(matched)
		if !hasSuffix {
			// 无 AM/PM 后缀：24 时制，需覆盖的 SemanticTime 提示（比分歧义保守）。
			if !gatedRuleCovered(src, contract.SemanticTime, req.Hints) {
				continue
			}
		}
		spoken, ok := enTimeToSpeech(matched)
		if !ok {
			continue
		}
		out = append(out, contract.Candidate{
			Src: src, Replacement: spoken, RuleID: "en.time", Category: "time", Priority: 14,
		})
	}
	return out
}

func hasAMPM(s string) bool {
	upper := strings.ToUpper(strings.TrimSpace(s))
	return strings.HasSuffix(upper, "AM") || strings.HasSuffix(upper, "PM")
}

// gatedRuleCovered 判断是否有该语义的提示覆盖整个区间。
func gatedRuleCovered(src contract.Range, gate contract.SemanticKind, hints []contract.Hint) bool {
	for _, h := range hints {
		if h.Kind == contract.HintSemantic && h.Semantic == gate &&
			h.Range.Start <= src.Start && h.Range.End >= src.End {
			return true
		}
	}
	return false
}

// enTimeToSpeech converts a time token to English speech.
// "10:30 AM" → "ten thirty AM", "14:05" → "fourteen oh five".
func enTimeToSpeech(s string) (string, bool) {
	upper := strings.ToUpper(strings.TrimSpace(s))
	suffix := ""
	if strings.HasSuffix(upper, "AM") {
		suffix = " AM"
		upper = strings.TrimSpace(strings.TrimSuffix(upper, "AM"))
	} else if strings.HasSuffix(upper, "PM") {
		suffix = " PM"
		upper = strings.TrimSpace(strings.TrimSuffix(upper, "PM"))
	}
	parts := strings.Split(upper, ":")
	vals := make([]int, len(parts))
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || (i == 0 && n > 23) || (i > 0 && n > 59) {
			return "", false
		}
		vals[i] = n
	}
	// 12 小时制：hour 1..12（AM/PM）。24 时制：hour 0..23。
	if suffix != "" && (vals[0] < 1 || vals[0] > 12) {
		return "", false
	}
	hour, ok := enIntegerToWords(strconv.Itoa(vals[0]))
	if !ok {
		return "", false
	}
	if len(parts) == 2 && vals[1] == 0 && suffix == "" {
		return "zero o'clock", true
	}
	var spoken string
	if len(parts) == 2 && vals[1] == 0 {
		// 12 时制整点：保留数字 + 后缀（"tonight AM" 直觉难读，直接读整点）。
		spoken = hour + " o'clock" + suffix
		return spoken, true
	}
	minute, ok := enClockMinute(vals[1])
	if !ok {
		return "", false
	}
	spoken = hour + " " + minute
	if len(parts) == 3 {
		sec, ok := enIntegerToWords(strconv.Itoa(vals[2]))
		if !ok {
			return "", false
		}
		spoken += " " + sec
	}
	return spoken + suffix, true
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
