package en

import (
	"strings"

	"github.com/F31/textnorm/internal/contract"
)

// AbbrevRule expands common English month and weekday abbreviations to their
// full names for TTS. Ambiguous bare forms (May, Mar, Sun as part of a name)
// are left untouched; only explicit dotted abbreviations (Mon., Jan.) and the
// unambiguous long weekday/month short forms (Thu, Sept) are expanded, bounded
// to standalone tokens so they never touch model/identifier spans.
func AbbrevRule() contract.CandidateRule {
	return abbrevRule{}
}

type abbrevRule struct{}

func (abbrevRule) RuleID() string    { return "en.abbrev" }
func (abbrevRule) RulePriority() int { return 16 }

var enAbbrevExpansions = map[string]string{
	// 星期（点号缩写 & 明确三字母）。
	"Mon.": "Monday", "Tues.": "Tuesday", "Tue.": "Tuesday", "Wed.": "Wednesday",
	"Thurs.": "Thursday", "Thu.": "Thursday", "Fri.": "Friday",
	"Sat.": "Saturday", "Sun.": "Sunday",
	// 月份（点号缩写；不展开 Jun/Jul 以免 June/July 拼写歧义之外仍安全，故列出全部明确形式）。
	"Jan.": "January", "Feb.": "February", "Mar.": "March", "Apr.": "April",
	"Jun.": "June", "Jul.": "July", "Aug.": "August", "Sept.": "September",
	"Sep.": "September", "Oct.": "October", "Nov.": "November", "Dec.": "December",
	// 无点号的明确短形式（避免 May/Mar 歧义，只收无歧义项）。
	"Sept": "September", "Thurs": "Thursday", "Tues": "Tuesday",
}

func (abbrevRule) FindCandidates(req contract.Request) []contract.Candidate {
	runes := []rune(req.Text)
	var out []contract.Candidate
	for token, full := range enAbbrevExpansions {
		// 手动扫描以精确控制边界（避免 regexp 反向引用歧义）。
		start := 0
		for {
			idx := strings.Index(req.Text[start:], token)
			if idx < 0 {
				break
			}
			pos := start + idx
			end := pos + len(token)
			startR := req.RuneOffset(pos)
			endR := req.RuneOffset(end)
			// 独立 token：两侧不得邻接 ASCII 字母/数字。
			if startR > 0 && contract.ASCIIAlnumRune(runes[startR-1]) {
				start = end
				continue
			}
			if endR < len(runes) && contract.ASCIIAlnumRune(runes[endR]) {
				start = end
				continue
			}
			out = append(out, contract.Candidate{
				Src: contract.Range{Start: startR, End: endR}, Replacement: full,
				RuleID: "en.abbrev", Category: "abbrev", Priority: 16,
			})
			start = end
		}
	}
	return out
}
