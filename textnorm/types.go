// Package textnorm provides the V3.1 Chinese/English TTS text normalization
// contract.
//
// The package is intentionally independent from PPTS application concerns: it
// does not know about tenants, databases, HTTP handlers, environment variables,
// or TTS providers. Rules observe the original input text and produce candidate
// replacements over rune-indexed ranges; selected replacements are applied once.
//
// The rule implementations live in language packages (rules/zh, rules/en) and
// depend only on internal/contract; this root package re-exports them as the
// public API and owns the engine, selection, output, and profile assembly.
package textnorm

import (
	"github.com/F31/textnorm/internal/contract"
)

// Range is a rune-indexed half-open interval [Start, End).
type Range = contract.Range

// Request is the independent text normalization request contract.
// Rules observe the original Text only; replacements are not fed into later rules.
type Request = contract.Request

// Candidate is a rule proposal over the original input text.
type Candidate = contract.Candidate

// CandidateRule finds replacement candidates in the original request text.
type CandidateRule = contract.CandidateRule

// Hint is an explicit caller constraint on a rune range.
type Hint = contract.Hint

// HintKind is the kind of an explicit caller constraint.
type HintKind = contract.HintKind

const (
	// HintVerbatim preserves a range exactly as written.
	HintVerbatim = contract.HintVerbatim
	// HintReading replaces a range with caller-provided reading text.
	HintReading = contract.HintReading
	// HintSemantic marks a range with a semantic category.
	HintSemantic = contract.HintSemantic
)

// SemanticKind is a category label for a semantic hint.
type SemanticKind = contract.SemanticKind

const (
	SemanticNumber    = contract.SemanticNumber
	SemanticDate      = contract.SemanticDate
	SemanticTelephone = contract.SemanticTelephone
	SemanticRatio     = contract.SemanticRatio
	SemanticScore     = contract.SemanticScore
	SemanticFraction  = contract.SemanticFraction
	SemanticTime      = contract.SemanticTime
)

// NumberMode controls how an independent Chinese integer token is read aloud.
type NumberMode = contract.NumberMode

const (
	// NumberModeYear reads digits one at a time (2024 → 二零二四).
	NumberModeYear = contract.NumberModeYear
	// NumberModeQuantity reads by decimal place (2024 → 二千零二十四).
	NumberModeQuantity = contract.NumberModeQuantity
)

// ENNumberMode controls how an independent English integer token is read aloud.
type ENNumberMode = contract.ENNumberMode

const (
	ENNumberModeQuantity = contract.ENNumberModeQuantity
	ENNumberModeYear     = contract.ENNumberModeYear
	ENNumberModeDigits   = contract.ENNumberModeDigits
)

// Mode controls whether a recognized span is converted or only preserved.
type Mode = contract.Mode

const (
	ModeConvert  = contract.ModeConvert
	ModePreserve = contract.ModePreserve
)

// Result contains the normalized text and explainable transformation records.
type Result struct {
	Text        string
	SourceMap   SourceMap
	Edits       []Edit
	Diagnostics []Diagnostic
	Controls    []Control
	Manifest    Manifest
}

// Control is a structured speech control event attached to the output text
// (V3.1 §10.3), such as a pause after a rune boundary.
type Control struct {
	AfterRunes int // stop after output rune index N
	DurationMS int // desired pause milliseconds
}

// SourceMap records source relationships between original text and normalized text.
type SourceMap struct {
	Spans []SourceSpan
}

// SourceKind describes the precision of one source relationship.
type SourceKind string

const (
	SourceIdentity    SourceKind = "identity"
	SourceReplacement SourceKind = "replacement"
	SourceInsertion   SourceKind = "insertion"
	SourceDeletion    SourceKind = "deletion"
)

// SourceSpan maps an original range to an output range.
type SourceSpan struct {
	Src    Range
	Dst    Range
	Kind   SourceKind
	RuleID string
}

// Edit records one selected replacement candidate.
type Edit struct {
	Src         Range
	Dst         Range
	Replacement string
	RuleID      string
	Category    string
}

// Diagnostic records non-fatal normalization decisions, such as skipped conflicts.
type Diagnostic struct {
	Range   Range
	Code    string
	Message string
	RuleID  string
}

// Manifest identifies the engine and rule set that produced a result.
type Manifest struct {
	Version        string
	Profile        string
	ProfileVersion int
	ConfigHash     string
	Rules          []string
}
