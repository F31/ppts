package zh

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/F31/textnorm/internal/contract"
)

// DateRule recognizes yyyy-mm-dd, yyyy/mm/dd, and yyyy.mm.dd date forms.
// In preserve mode it protects the whole date span without changing output.
func DateRule(mode contract.Mode) contract.CandidateRule {
	return dateRule{mode: mode}
}

type dateRule struct {
	mode contract.Mode
}

var dateRe = regexp.MustCompile(`[0-9]{4}[-/.][0-9]{1,2}[-/.][0-9]{1,2}`)

func (r dateRule) RuleID() string {
	if r.mode == contract.ModePreserve {
		return "zh.date.preserve"
	}
	return "zh.date"
}

func (r dateRule) RulePriority() int { return 10 }

func (r dateRule) FindCandidates(req contract.Request) []contract.Candidate {
	locs := dateRe.FindAllStringIndex(req.Text, -1)
	out := make([]contract.Candidate, 0, len(locs))
	for _, loc := range locs {
		matched := req.Text[loc[0]:loc[1]]
		repl, ok := zhDateToSpeech(matched)
		if !ok {
			continue
		}
		c := contract.Candidate{
			Src:         contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(loc[1])},
			Replacement: repl,
			RuleID:      r.RuleID(),
			Category:    "date",
			Priority:    r.RulePriority(),
		}
		if r.mode == contract.ModePreserve {
			c.Preserve = true
			c.Replacement = ""
		}
		out = append(out, c)
	}
	return out
}

func zhDateToSpeech(s string) (string, bool) {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '/' || r == '.' })
	if len(parts) != 3 || len(parts[0]) != 4 {
		return "", false
	}
	year, err := strconv.Atoi(parts[0])
	if err != nil {
		return "", false
	}
	month, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", false
	}
	day, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", false
	}
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if date.Year() != year || int(date.Month()) != month || date.Day() != day {
		return "", false
	}
	y, ok := zhYearToSpeech(parts[0])
	if !ok {
		return "", false
	}
	m, ok := zhIntegerToSpeech(parts[1])
	if !ok {
		return "", false
	}
	d, ok := zhIntegerToSpeech(parts[2])
	if !ok {
		return "", false
	}
	return y + "年" + m + "月" + d + "日", true
}
