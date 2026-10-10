package en

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/F31/textnorm/internal/contract"
)

// DateRule reads yyyy-mm-dd / yyyy/mm/dd / yyyy.mm.dd date forms into English
// speech. US order: month day(th), year (e.g. "January 28th, 2024" → read as
// "January twenty eighth, twenty twenty four"). A leading explicit year makes
// the form unambiguous, so no semantic hint is required. A regex-recognized
// date that fails conversion (invalid calendar date) is preserved as-is so
// number rules do not split its digits.
func DateRule() contract.CandidateRule {
	return dateRule{}
}

type dateRule struct{}

func (dateRule) RuleID() string    { return "en.date" }
func (dateRule) RulePriority() int { return 10 }

func (dateRule) FindCandidates(req contract.Request) []contract.Candidate {
	var out []contract.Candidate
	for _, loc := range enDateRe.FindAllStringIndex(req.Text, -1) {
		if !contract.ASCIIBoundary(req.Text, loc[0], loc[1]) {
			continue
		}
		src := contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(loc[1])}
		matched := req.Text[loc[0]:loc[1]]
		repl, ok := enDateToSpeech(matched)
		if !ok {
			// 无效历法日期：原样保护，避免 number 规则拆开 2024-02-30 的数字。
			out = append(out, contract.Candidate{Src: src, Preserve: true, RuleID: "en.date", Category: "date", Priority: 10})
			continue
		}
		out = append(out, contract.Candidate{
			Src: src, Replacement: repl, RuleID: "en.date", Category: "date", Priority: 10,
		})
	}
	return out
}

var enDateRe = regexp.MustCompile(`[0-9]{4}[-/.][0-9]{1,2}[-/.][0-9]{1,2}`)

var enMonths = []string{"", "January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December"}

// enDateToSpeech converts an ISO-style date to English speech in US order.
func enDateToSpeech(s string) (string, bool) {
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
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return "", false
	}
	// Sanity-check the month/day against a real calendar.
	if !validDate(year, month, day) {
		return "", false
	}
	monthName := enMonths[month]
	dayOrdinal, ok := enCardinalToOrdinal(strconv.Itoa(day))
	if !ok {
		return "", false
	}
	yearSpeech := enYearToSpeech(parts[0])
	return monthName + " " + dayOrdinal + ", " + yearSpeech, true
}

func validDate(year, month, day int) bool {
	if month < 1 || month > 12 {
		return false
	}
	daysInMonth := []int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	max := daysInMonth[month-1]
	if month == 2 && isLeapYear(year) {
		max = 29
	}
	return day >= 1 && day <= max
}

func isLeapYear(y int) bool {
	return y%4 == 0 && (y%100 != 0 || y%400 == 0)
}
