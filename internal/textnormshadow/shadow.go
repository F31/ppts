// Package textnormshadow provides the PPTS-side dual-track (shadow) comparator
// between the legacy internal textnorm engine and the independent candidate
// engine (V3.1 §14.3). It is pure and DB-free: both engines are built from the
// same Options, so shadow runs produce a Report of text/control differences.
//
// This package does not change the production narration path; wiring the shadow
// into segment processing is a separate, reviewed step.
package textnormshadow

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	oldtn "github.com/F31/ppts/internal/textnorm"
	newtn "github.com/F31/textnorm"
)

// DictEntry is a literal dictionary rule, mirroring the pronunciation.Dictionary
// semantics (逐条 ReplaceAll over the span, no word boundary).
type DictEntry struct {
	Match       string
	Replacement string
}

// ContextEntry is a data-driven pattern replacement (mirrors a contextual rule
// record: Go regexp + $1/$2 template + shared priority).
type ContextEntry struct {
	Pattern     string
	Replacement string
	Priority    int
}

// Options build an equivalent engine on either side.
type Options struct {
	Lang       string
	Dict       []DictEntry
	Context    []ContextEntry
	Classes    []string
	NumberMode *oldtn.NumberMode
}

// BuildOldEngine builds the legacy engine with the same rule registration order
// and priorities as internal/app.NewTextNormEngine (minus DB loading).
func BuildOldEngine(o Options) (*oldtn.Engine, error) {
	e := oldtn.New()
	if len(o.Dict) > 0 {
		e.RegisterRule("definition", 10, oldtn.DefinitionRule(replaceAllDict(o.Dict)))
	}
	for _, c := range o.Classes {
		if fn, ok := oldClassPatterns[c]; ok {
			e.RegisterPattern(fn())
		}
	}
	if o.NumberMode != nil {
		e.RegisterDigitRule("number", 20, oldtn.NumberRule(*o.NumberMode))
	}
	for _, re := range o.Context {
		p, err := oldtn.ReplacementPattern(re.Pattern, re.Replacement, re.Priority)
		if err != nil {
			return nil, fmt.Errorf("textnormshadow: invalid context rule %q: %w", re.Pattern, err)
		}
		e.RegisterPattern(p)
	}
	e.Build()
	return e, nil
}

// BuildNewEngine builds the independent candidate engine for the same Options.
// It returns a notice list for features the candidate engine does not cover yet
// (e.g. number expansion, money), so shadow output can distinguish equivalence
// from "not comparable yet".
func BuildNewEngine(o Options) (*newtn.Engine, []string, error) {
	var rules []newtn.CandidateRule
	var notice []string
	if len(o.Dict) > 0 {
		entries := make([]newtn.LiteralEntry, len(o.Dict))
		for i, d := range o.Dict {
			entries[i] = newtn.LiteralEntry{Match: d.Match, Replace: d.Replacement, Whole: false}
		}
		rules = append(rules, newtn.LiteralDictRule("definition", 10, entries))
	}
	for _, c := range o.Classes {
		switch c {
		case "percent":
			rules = append(rules, newtn.ZHPercentRule())
		case "date":
			rules = append(rules, newtn.ZHDateRule(newtn.ModeConvert))
		case "unit":
			rules = append(rules, newtn.ZHUnitRule())
		case "decimal":
			rules = append(rules, newtn.ZHDecimalRule())
		default:
			notice = append(notice, "class not yet supported by candidate engine: "+c)
		}
	}
	if o.NumberMode != nil {
		rules = append(rules, newtn.ZHNumberRule(newtn.NumberMode(*o.NumberMode)))
	}
	for i, re := range o.Context {
		re2, err := regexp.Compile(re.Pattern)
		if err != nil {
			return nil, nil, fmt.Errorf("textnormshadow: invalid context rule %q: %w", re.Pattern, err)
		}
		rules = append(rules, newtn.RegexRule{
			ID:          fmt.Sprintf("context:%d", i),
			Category:    "context",
			Priority:    re.Priority,
			Match:       re2,
			Replacement: re.Replacement,
		})
	}
	ne, err := newtn.Compile(newtn.Config{Rules: rules})
	if err != nil {
		return nil, nil, err
	}
	return ne, notice, nil
}

// PauseRec is a comparable view of a structured pause on either engine.
type PauseRec struct {
	AfterRunes int
	DurationMS int
}

// Report captures one shadow comparison for a single input text.
type Report struct {
	Input     string
	OldText   string
	NewText   string
	OldPauses []PauseRec
	NewPauses []PauseRec
	Same      bool
	NewError  string
}

// Compare runs the legacy and candidate engines on one text and reports output
// differences. LegacyMarkup is always enabled on the candidate side so legacy
// 〔读：x〕/‖ markers are interpreted identically to the legacy engine.
func Compare(old *oldtn.Engine, ne *newtn.Engine, lang, text string) Report {
	r := Report{Input: text}
	oldRes := old.Run(text, lang)
	r.OldText = oldRes.Text
	r.OldPauses = mapOldPauses(oldRes.Pauses)
	newRes, err := ne.Normalize(context.Background(), newtn.Request{Text: text, Lang: lang, LegacyMarkup: true})
	if err != nil {
		r.NewError = err.Error()
		return r
	}
	r.NewText = newRes.Text
	r.NewPauses = mapNewControls(newRes.Controls)
	r.Same = r.OldText == r.NewText && equalPauses(r.OldPauses, r.NewPauses)
	return r
}

// Spec is a self-contained comparison job: one Options plus a list of segments.
type Spec struct {
	Lang       string
	Dict       []DictEntry
	Context    []ContextEntry
	Classes    []string
	NumberMode *oldtn.NumberMode
	Segments   []string
}

// CompareSpec builds both engines once and compares every segment, returning
// per-segment reports plus the candidate-engine notice list (unsupported
// features such as number expansion).
func CompareSpec(s Spec) ([]Report, []string, error) {
	if len(s.Segments) == 0 {
		return nil, nil, nil
	}
	opts := Options{
		Lang:       s.Lang,
		Dict:       s.Dict,
		Context:    s.Context,
		Classes:    s.Classes,
		NumberMode: s.NumberMode,
	}
	old, err := BuildOldEngine(opts)
	if err != nil {
		return nil, nil, err
	}
	ne, notice, err := BuildNewEngine(opts)
	if err != nil {
		return nil, nil, err
	}
	lang := s.Lang
	if lang == "" {
		lang = "zh-CN"
	}
	reports := make([]Report, 0, len(s.Segments))
	for _, seg := range s.Segments {
		reports = append(reports, Compare(old, ne, lang, seg))
	}
	return reports, notice, nil
}

// replaceAllDict implements the legacy textnorm.Dictionary contract with the
// same 逐条 ReplaceAll semantics as internal/app.DictAdapter.
type replaceAllDict []DictEntry

func (d replaceAllDict) Substitute(_ string, text string) (bool, string) {
	changed := false
	for _, e := range d {
		if strings.Contains(text, e.Match) {
			text = strings.ReplaceAll(text, e.Match, e.Replacement)
			changed = true
		}
	}
	return changed, text
}

// oldClassPatterns mirrors internal/app.classPatterns.
var oldClassPatterns = map[string]func() oldtn.Pattern{
	"percent": oldtn.PercentPattern,
	"decimal": oldtn.DecimalPattern,
	"date":    oldtn.DatePattern,
	"money":   oldtn.MoneyPattern,
	"unit":    oldtn.UnitPattern,
}

func mapOldPauses(ps []oldtn.Pause) []PauseRec {
	out := make([]PauseRec, len(ps))
	for i, p := range ps {
		out[i] = PauseRec{AfterRunes: p.AfterRunes, DurationMS: p.DurationMS}
	}
	return out
}

func mapNewControls(cs []newtn.Control) []PauseRec {
	out := make([]PauseRec, len(cs))
	for i, c := range cs {
		out[i] = PauseRec{AfterRunes: c.AfterRunes, DurationMS: c.DurationMS}
	}
	return out
}

func equalPauses(a, b []PauseRec) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
