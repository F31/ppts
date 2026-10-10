package en

import (
	"regexp"
	"strings"

	"github.com/F31/textnorm/internal/contract"
)

// OrdinalRule reads explicit English ordinal forms such as 21st and 3rd.
// The suffix disambiguates them from plain quantities, so no semantic hint is
// required.
func OrdinalRule() contract.CandidateRule {
	return ordinalRule{}
}

type ordinalRule struct{}

var ordinalRe = regexp.MustCompile(`[0-9]+(?:st|nd|rd|th)`)

func (ordinalRule) RuleID() string    { return "en.ordinal" }
func (ordinalRule) RulePriority() int { return 18 }

func (ordinalRule) FindCandidates(req contract.Request) []contract.Candidate {
	// Plain quantity words are built from the digits; the ordinal suffix is
	// produced from the spelled form of the integer. Digit runs must not be
	// adjacent to other ASCII alphanumerics.
	runes := []rune(req.Text)
	var out []contract.Candidate
	for _, loc := range ordinalRe.FindAllStringIndex(req.Text, -1) {
		token := req.Text[loc[0]:loc[1]]
		// Skip candidates embedded in a longer alphanumeric token.
		startRune := req.RuneOffset(loc[0])
		endRune := req.RuneOffset(loc[1])
		if startRune > 0 && contract.ASCIIAlnumRune(runes[startRune-1]) {
			continue
		}
		if endRune < len(runes) && contract.ASCIIAlnumRune(runes[endRune]) {
			continue
		}
		spoken, ok := enOrdinalToSpeech(token)
		if !ok {
			continue
		}
		out = append(out, contract.Candidate{
			Src:         contract.Range{Start: startRune, End: endRune},
			Replacement: spoken,
			RuleID:      "en.ordinal",
			Category:    "ordinal",
			Priority:    18,
		})
	}
	return out
}

// enOrdinalToSpeech reads "21st" → "twenty-first", "3rd" → "third".
func enOrdinalToSpeech(token string) (string, bool) {
	digits := strings.TrimRight(token, "stndrdth")
	if digits == "" || digits == token {
		return "", false
	}
	if len(digits) > 12 {
		return "", false
	}
	// Rebuild ordinal from its cardinal word by converting the final unit.
	return enCardinalToOrdinal(digits)
}

// enCardinalToOrdinal converts a cardinal digit string to its English ordinal.
func enCardinalToOrdinal(digits string) (string, bool) {
	// Special small ordinals must be handled via the cardinal→ordinal table at
	// the unit level; for teens the whole last two units are irregular.
	trimmed := strings.TrimLeft(digits, "0")
	if trimmed == "" {
		return "zeroth", true
	}
	// Split into whole words via enIntegerToWords, then fix the last word.
	cardinal, ok := enIntegerToWords(trimmed)
	if !ok {
		return "", false
	}
	words := strings.Fields(cardinal)
	// The last word is the unit (or hyphenated tail). Convert the tail.
	last := words[len(words)-1]
	ord := enLastWordOrdinal(last)
	if ord == "" {
		return "", false
	}
	words[len(words)-1] = ord
	return strings.Join(words, " "), true
}

// enLastWordOrdinal maps a cardinal English word (or hyphen tail) to its ordinal.
func enLastWordOrdinal(word string) string {
	irregular := map[string]string{
		"one": "first", "two": "second", "three": "third", "five": "fifth",
		"eight": "eighth", "nine": "ninth", "twelve": "twelfth",
		"zero": "zeroth",
	}
	if o, ok := irregular[word]; ok {
		return o
	}
	// Hyphenated forms ("twenty-one") convert the trailing unit only.
	if i := strings.LastIndex(word, "-"); i >= 0 {
		tail := word[i+1:]
		ord := enLastWordOrdinal(tail)
		if ord == "" {
			return ""
		}
		return word[:i] + "-" + ord
	}
	// Words not in the irregular table: strip a trailing "ty" → "tieth",
	// otherwise append "th".
	if strings.HasSuffix(word, "ty") {
		return strings.TrimSuffix(word, "y") + "ieth"
	}
	if strings.HasSuffix(word, "e") && len(word) > 1 {
		return strings.TrimSuffix(word, "e") + "th"
	}
	return word + "th"
}