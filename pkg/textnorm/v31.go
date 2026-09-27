// Package textnorm provides the V3.1 Chinese TTS text normalization contract.
//
// The package is intentionally independent from PPTS application concerns: it
// does not know about tenants, databases, HTTP handlers, environment variables,
// or TTS providers. Rules observe the original input text and produce candidate
// replacements over rune-indexed ranges; selected replacements are applied once.
package textnorm

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Range is a rune-indexed half-open interval [Start, End).
type Range struct {
	Start int
	End   int
}

// Request is the independent text normalization request contract.
// Rules observe the original Text only; replacements are not fed into later rules.
type Request struct {
	Text      string
	Lang      string
	Protected []Range
}

// Result contains the normalized text and explainable transformation records.
type Result struct {
	Text        string
	SourceMap   SourceMap
	Edits       []Edit
	Diagnostics []Diagnostic
	Manifest    Manifest
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
	Version string
	Rules   []string
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

// Config is compiled into an immutable Engine.
type Config struct {
	Rules []CandidateRule
}

// Engine is the V3.1 non-cascading normalization engine.
type Engine struct {
	rules    []CandidateRule
	manifest Manifest
}

// Compile validates and freezes a V3.1 normalization configuration.
func Compile(cfg Config) (*Engine, error) {
	seen := map[string]struct{}{}
	rules := make([]CandidateRule, 0, len(cfg.Rules))
	ids := make([]string, 0, len(cfg.Rules))
	for i, r := range cfg.Rules {
		if r == nil {
			return nil, fmt.Errorf("textnorm: nil rule at index %d", i)
		}
		id := r.RuleID()
		if id == "" {
			return nil, fmt.Errorf("textnorm: empty rule id at index %d", i)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("textnorm: duplicate rule id %q", id)
		}
		seen[id] = struct{}{}
		rules = append(rules, r)
		ids = append(ids, id)
	}
	sort.SliceStable(rules, func(i, j int) bool {
		return rules[i].RulePriority() < rules[j].RulePriority()
	})
	sort.Strings(ids)
	return &Engine{
		rules:    rules,
		manifest: Manifest{Version: "textnorm-v3.1-draft", Rules: ids},
	}, nil
}

// Normalize applies V3.1 semantics: rules read original text, selected candidates are
// non-overlapping, protected ranges are preserved, and output is built once.
func (e *Engine) Normalize(ctx context.Context, req Request) (Result, error) {
	if e == nil {
		return Result{}, errors.New("textnorm: nil engine")
	}
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}
	runes := []rune(req.Text)
	protected, err := normalizeRanges(req.Protected, len(runes))
	if err != nil {
		return Result{}, err
	}

	candidates := make([]Candidate, 0)
	for _, r := range e.rules {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		default:
		}
		for _, c := range r.FindCandidates(req) {
			if c.RuleID == "" {
				c.RuleID = r.RuleID()
			}
			if c.Priority == 0 {
				c.Priority = r.RulePriority()
			}
			if !validRange(c.Src, len(runes)) || c.Src.Start == c.Src.End {
				return Result{}, fmt.Errorf("textnorm: invalid candidate range from %q", r.RuleID())
			}
			candidates = append(candidates, c)
		}
	}

	selected, diagnostics := selectCandidates(candidates, protected)
	text, spans, edits := buildOutput(runes, selected)
	return Result{
		Text:        text,
		SourceMap:   SourceMap{Spans: spans},
		Edits:       edits,
		Diagnostics: diagnostics,
		Manifest:    e.manifest,
	}, nil
}

func selectCandidates(candidates []Candidate, protected []Range) ([]Candidate, []Diagnostic) {
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Src.Start != b.Src.Start {
			return a.Src.Start < b.Src.Start
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if la, lb := a.Src.End-a.Src.Start, b.Src.End-b.Src.Start; la != lb {
			return la > lb
		}
		return a.RuleID < b.RuleID
	})
	selected := make([]Candidate, 0, len(candidates))
	diagnostics := []Diagnostic{}
	occupied := []Range{}
	for _, c := range candidates {
		if overlapsAny(c.Src, protected) {
			diagnostics = append(diagnostics, Diagnostic{Range: c.Src, Code: "protected", Message: "candidate overlaps protected range", RuleID: c.RuleID})
			continue
		}
		if overlapsAny(c.Src, occupied) {
			diagnostics = append(diagnostics, Diagnostic{Range: c.Src, Code: "conflict", Message: "candidate overlaps selected candidate", RuleID: c.RuleID})
			continue
		}
		selected = append(selected, c)
		occupied = append(occupied, c.Src)
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].Src.Start < selected[j].Src.Start })
	return selected, diagnostics
}

func buildOutput(src []rune, editsIn []Candidate) (string, []SourceSpan, []Edit) {
	var sb strings.Builder
	spans := make([]SourceSpan, 0, len(editsIn)*2+1)
	edits := make([]Edit, 0, len(editsIn))
	srcCursor, dstCursor := 0, 0
	for _, c := range editsIn {
		if srcCursor < c.Src.Start {
			gap := string(src[srcCursor:c.Src.Start])
			sb.WriteString(gap)
			gapLen := len([]rune(gap))
			spans = append(spans, SourceSpan{Src: Range{srcCursor, c.Src.Start}, Dst: Range{dstCursor, dstCursor + gapLen}, Kind: SourceIdentity})
			dstCursor += gapLen
		}
		if c.Preserve {
			preserved := string(src[c.Src.Start:c.Src.End])
			sb.WriteString(preserved)
			preservedLen := len([]rune(preserved))
			spans = append(spans, SourceSpan{Src: c.Src, Dst: Range{dstCursor, dstCursor + preservedLen}, Kind: SourceIdentity, RuleID: c.RuleID})
			dstCursor += preservedLen
			srcCursor = c.Src.End
			continue
		}
		dstStart := dstCursor
		sb.WriteString(c.Replacement)
		replLen := len([]rune(c.Replacement))
		dstCursor += replLen
		kind := SourceReplacement
		if replLen == 0 {
			kind = SourceDeletion
		}
		spans = append(spans, SourceSpan{Src: c.Src, Dst: Range{dstStart, dstCursor}, Kind: kind, RuleID: c.RuleID})
		edits = append(edits, Edit{Src: c.Src, Dst: Range{dstStart, dstCursor}, Replacement: c.Replacement, RuleID: c.RuleID, Category: c.Category})
		srcCursor = c.Src.End
	}
	if srcCursor < len(src) {
		gap := string(src[srcCursor:])
		sb.WriteString(gap)
		gapLen := len([]rune(gap))
		spans = append(spans, SourceSpan{Src: Range{srcCursor, len(src)}, Dst: Range{dstCursor, dstCursor + gapLen}, Kind: SourceIdentity})
	}
	return sb.String(), spans, edits
}

func normalizeRanges(ranges []Range, max int) ([]Range, error) {
	out := make([]Range, 0, len(ranges))
	for _, r := range ranges {
		if !validRange(r, max) {
			return nil, fmt.Errorf("textnorm: invalid protected range [%d,%d)", r.Start, r.End)
		}
		if r.Start == r.End {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	merged := out[:0]
	for _, r := range out {
		if len(merged) == 0 || merged[len(merged)-1].End < r.Start {
			merged = append(merged, r)
			continue
		}
		if r.End > merged[len(merged)-1].End {
			merged[len(merged)-1].End = r.End
		}
	}
	return merged, nil
}

func validRange(r Range, max int) bool {
	return r.Start >= 0 && r.Start <= r.End && r.End <= max
}

func overlapsAny(r Range, ranges []Range) bool {
	for _, x := range ranges {
		if r.Start < x.End && x.Start < r.End {
			return true
		}
	}
	return false
}

// RegexRule is a minimal declarative V3.1 rule for exact-range template replacements.
type RegexRule struct {
	ID          string
	Category    string
	Priority    int
	Match       *regexp.Regexp
	Replacement string
}

func (r RegexRule) RuleID() string    { return r.ID }
func (r RegexRule) RulePriority() int { return r.Priority }

func (r RegexRule) FindCandidates(req Request) []Candidate {
	if r.Match == nil {
		return nil
	}
	locs := r.Match.FindAllStringSubmatchIndex(req.Text, -1)
	out := make([]Candidate, 0, len(locs))
	for _, loc := range locs {
		if loc[0] == loc[1] {
			continue
		}
		matched := req.Text[loc[0]:loc[1]]
		out = append(out, Candidate{
			Src:         Range{Start: runeOffset(req.Text, loc[0]), End: runeOffset(req.Text, loc[1])},
			Replacement: r.Match.ReplaceAllString(matched, r.Replacement),
			RuleID:      r.ID,
			Category:    r.Category,
			Priority:    r.Priority,
		})
	}
	return out
}

func runeOffset(src string, bytePos int) int {
	if bytePos <= 0 {
		return 0
	}
	if bytePos >= len(src) {
		return len([]rune(src))
	}
	return utf8.RuneCountInString(src[:bytePos])
}
