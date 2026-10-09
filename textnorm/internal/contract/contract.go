// Package contract holds the minimal types shared between the textnorm engine
// and its rule packages. Rules live in separate packages (rules/zh, rules/en)
// and must not import the root textnorm package, so this package owns the
// candidate/range/request types and the small helpers rules rely on.
package contract

import (
	"unicode/utf8"
)

// Range is a rune-indexed half-open interval [Start, End).
type Range struct {
	Start int
	End   int
}

// Request is the rule-side normalization request contract. Rules observe the
// original Text only; replacements are not fed into later rules.
type Request struct {
	Text      string
	Lang      string
	Protected []Range
	Hints     []Hint
	// LegacyMarkup opts into the legacy 〔读：x〕 and ‖ compatibility parser.
	LegacyMarkup bool
	// byteToRune is a request-local UTF-8 byte-to-rune index built by IndexBytes.
	// Regex-based rules convert byte positions to rune ranges in O(1) with it.
	byteToRune []int
}

// IndexBytes builds the request-local byte-to-rune index. It is called once by
// the engine at the start of Normalize; rules only read via RuneOffset.
func (r *Request) IndexBytes() {
	idx := make([]int, len(r.Text)+1)
	ri := 0
	for bytePos := range r.Text {
		idx[bytePos] = ri
		ri++
	}
	idx[len(r.Text)] = ri
	r.byteToRune = idx
}

// RuneOffset converts a UTF-8 byte position to a rune index using the index
// built by IndexBytes; falls back to a linear count when the index is absent
// (direct FindCandidates calls outside the engine).
func (r Request) RuneOffset(bytePos int) int {
	if r.byteToRune != nil && bytePos >= 0 && bytePos < len(r.byteToRune) {
		return r.byteToRune[bytePos]
	}
	if bytePos <= 0 {
		return 0
	}
	if bytePos >= len(r.Text) {
		return utf8.RuneCountInString(r.Text)
	}
	return utf8.RuneCountInString(r.Text[:bytePos])
}

// HintKind is the kind of an explicit caller constraint.
type HintKind int

const (
	// HintVerbatim preserves a range exactly as written.
	HintVerbatim HintKind = iota
	// HintReading replaces a range with caller-provided reading text.
	HintReading
	// HintSemantic marks a range with a semantic category; only rules whose
	// Category matches may convert it, otherwise the range is preserved.
	HintSemantic
)

// SemanticKind is a category label for a semantic hint. It must match the
// Category of a rule that knows how to read the range.
type SemanticKind string

const (
	SemanticNumber    SemanticKind = "number"
	SemanticDate      SemanticKind = "date"
	SemanticTelephone SemanticKind = "telephone"
	SemanticRatio     SemanticKind = "ratio"
	SemanticScore     SemanticKind = "score"
	SemanticFraction  SemanticKind = "fraction"
	SemanticTime      SemanticKind = "time"
)

// ValidSemanticKind reports whether k is a known semantic category.
func ValidSemanticKind(k SemanticKind) bool {
	switch k {
	case SemanticNumber, SemanticDate, SemanticTelephone, SemanticRatio,
		SemanticScore, SemanticFraction, SemanticTime:
		return true
	}
	return false
}

// Hint is an explicit caller constraint on a rune range. Hints must not overlap
// each other or any protected range; contradictory explicit hints are rejected.
type Hint struct {
	Range    Range
	Kind     HintKind
	Text     string       // replacement text for reading/verbatim hints
	Semantic SemanticKind // for semantic hints
}

// Candidate is a rule proposal over the original input text.
type Candidate struct {
	Src         Range
	Replacement string
	RuleID      string
	Category    string
	Priority    int
	Preserve    bool
}

// CandidateRule finds replacement candidates in the original request text.
type CandidateRule interface {
	RuleID() string
	RulePriority() int
	FindCandidates(Request) []Candidate
}

// SemanticGated is an optional rule capability: rules implementing it only emit
// candidates when a matching semantic hint covers the candidate range
// (ambiguous categories like score/ratio are disambiguated by hints).
type SemanticGated interface {
	CandidateRule
	SemanticGate() SemanticKind
}

// NumberMode controls how an independent Chinese integer token is read aloud.
type NumberMode int

const (
	// NumberModeYear reads digits one at a time (2024 → 二零二四).
	NumberModeYear NumberMode = iota
	// NumberModeQuantity reads by decimal place (2024 → 二千零二十四).
	NumberModeQuantity
)

// ENNumberMode controls how an independent English integer token is read aloud.
type ENNumberMode int

const (
	// ENNumberModeQuantity reads by decimal place (2048 → two thousand forty-eight).
	ENNumberModeQuantity ENNumberMode = iota
	// ENNumberModeYear reads four-digit years by pairs (2024 → twenty twenty-four).
	ENNumberModeYear
	// ENNumberModeDigits reads each digit including leading zeros (001 → zero zero one).
	ENNumberModeDigits
)

// Mode controls whether a recognized span is converted or only preserved.
type Mode int

const (
	ModeConvert Mode = iota
	ModePreserve
)

// ASCIIBoundary reports whether the span [start,end) in s is not directly
// adjacent to ASCII alphanumerics or a hyphen (i.e. it is a standalone token).
func ASCIIBoundary(s string, start, end int) bool {
	if start > 0 {
		left := s[start-1]
		if ASCIIAlnumByte(left) || left == '-' {
			return false
		}
	}
	if end < len(s) {
		right := s[end]
		if ASCIIAlnumByte(right) || right == '-' {
			return false
		}
	}
	return true
}

// ASCIIAlnumByte reports whether b is an ASCII letter or digit.
func ASCIIAlnumByte(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// ASCIIAlnumRune reports whether r is an ASCII letter or digit. Han characters
// and punctuation never count as "model characters".
func ASCIIAlnumRune(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}
