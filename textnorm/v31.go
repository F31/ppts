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
	Hints     []Hint
	// LegacyMarkup opts into the legacy 〔读：x〕 and ‖ compatibility parser
	// (V3.1 §10.2). Plain text input does not auto-interpret those characters.
	LegacyMarkup bool
}

// HintKind is the kind of an explicit caller constraint (V3.1 §5.1 / §10.1).
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
)

var knownSemanticKinds = map[SemanticKind]bool{
	SemanticNumber: true, SemanticDate: true, SemanticTelephone: true,
	SemanticRatio: true, SemanticScore: true,
}

// Hint is an explicit caller constraint on a rune range. Hints must not overlap
// each other or any protected range; contradictory explicit hints are rejected.
type Hint struct {
	Range    Range
	Kind     HintKind
	Text     string       // replacement text for reading/verbatim hints
	Semantic SemanticKind // for semantic hints
}

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
	// PauseMS is the default pause duration for legacy ‖ markers (default 300).
	PauseMS int
}

// Engine is the V3.1 non-cascading normalization engine.
type Engine struct {
	rules    []CandidateRule
	manifest Manifest
	pauseMS  int
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
	pauseMS := cfg.PauseMS
	if pauseMS <= 0 {
		pauseMS = 300
	}
	return &Engine{
		rules:    rules,
		manifest: Manifest{Version: Version, Rules: ids},
		pauseMS:  pauseMS,
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
	hints, err := validateHints(req.Hints, len(runes), protected)
	if err != nil {
		return Result{}, err
	}

	// Explicit hint candidates take precedence over automatic candidates.
	var candidates []Candidate
	for _, h := range hints {
		switch h.Kind {
		case HintReading:
			candidates = append(candidates, Candidate{
				Src: h.Range, Replacement: h.Text, RuleID: "hint.reading",
				Category: "hint.reading", Priority: -2,
			})
		case HintVerbatim:
			candidates = append(candidates, Candidate{
				Src: h.Range, RuleID: "hint.verbatim",
				Category: "hint.verbatim", Priority: -1, Preserve: true,
			})
		}
	}

	// Legacy markup compatibility parser: readings become replacement candidates
	// over the full marker span, pauses become deletion candidates.
	var markup *Markup
	if req.LegacyMarkup {
		parsed := ParseLegacyMarkers(req.Text)
		markup = &parsed
		for _, rd := range parsed.Readings {
			candidates = append(candidates, Candidate{
				Src: rd.Outer, Replacement: rd.Text,
				RuleID: "markup.reading", Category: "markup.reading", Priority: 0,
			})
		}
		for _, p := range parsed.Pauses {
			candidates = append(candidates, Candidate{
				Src: Range{p, p + 1}, Replacement: "",
				RuleID: "markup.pause", Category: "markup.pause", Priority: -4,
			})
		}
	}

	// Automatic candidates: semantic hints gate which category may touch a range.
	var blocked []Diagnostic
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
			if sem, ok := semanticHintFor(c, hints); ok && c.Category != string(sem) {
				blocked = append(blocked, Diagnostic{
					Range: c.Src, Code: "hint",
					Message: "candidate category does not match semantic hint", RuleID: c.RuleID,
				})
				continue
			}
			if g, ok := r.(semanticGated); ok && !gatedRuleCovered(c.Src, g.SemanticGate(), hints) {
				// 歧义类别（比分/比率等）仅由匹配的语义提示启用；无提示时保守保留（不产出改写）。
				continue
			}
			candidates = append(candidates, c)
		}
	}

	selected, selDiags := selectCandidates(candidates, protected)
	// A semantic hint with no covering selected candidate is preserved so the
	// intended range stays untouched by any later rule.
	for _, h := range hints {
		if h.Kind != HintSemantic || overlapsAny(h.Range, selectedRanges(selected)) {
			continue
		}
		selected = append(selected, Candidate{
			Src: h.Range, RuleID: "hint.semantic",
			Category: "hint.semantic", Priority: -1, Preserve: true,
		})
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].Src.Start < selected[j].Src.Start })

	text, spans, edits := buildOutput(runes, selected)
	controls := buildControls(markup, spans, e.pauseMS)
	return Result{
		Text:        text,
		SourceMap:   SourceMap{Spans: spans},
		Edits:       edits,
		Diagnostics: append(blocked, selDiags...),
		Controls:    controls,
		Manifest:    e.manifest,
	}, nil
}

// buildControls derives structured pause events from selected legacy pause
// deletions. The pause position is the effective output boundary recorded by the
// deletion span (V3.1 §10.3); pauses that were blocked (e.g. protected) emit none.
func buildControls(markup *Markup, spans []SourceSpan, pauseMS int) []Control {
	if markup == nil {
		return nil
	}
	var controls []Control
	for _, p := range markup.Pauses {
		pr := Range{p, p + 1}
		for _, sp := range spans {
			if sp.Src == pr && sp.Kind == SourceDeletion {
				controls = append(controls, Control{AfterRunes: sp.Dst.Start, DurationMS: pauseMS})
				break
			}
		}
	}
	sort.SliceStable(controls, func(i, j int) bool {
		return controls[i].AfterRunes < controls[j].AfterRunes
	})
	return controls
}

func validateHints(hints []Hint, max int, protected []Range) ([]Hint, error) {
	for i, h := range hints {
		if !validRange(h.Range, max) {
			return nil, fmt.Errorf("textnorm: invalid hint range [%d,%d)", h.Range.Start, h.Range.End)
		}
		switch h.Kind {
		case HintReading:
			if h.Text == "" {
				return nil, fmt.Errorf("textnorm: reading hint %d has empty text", i)
			}
		case HintSemantic:
			if !knownSemanticKinds[h.Semantic] {
				return nil, fmt.Errorf("textnorm: unknown semantic hint %q", h.Semantic)
			}
		}
		if overlapsAny(h.Range, protected) {
			return nil, fmt.Errorf("textnorm: hint [%d,%d) overlaps protected range", h.Range.Start, h.Range.End)
		}
		for j := 0; j < i; j++ {
			if overlapsAny(h.Range, []Range{hints[j].Range}) {
				return nil, fmt.Errorf("textnorm: hints %d and %d overlap", j, i)
			}
		}
	}
	return hints, nil
}

func semanticHintFor(c Candidate, hints []Hint) (SemanticKind, bool) {
	for _, h := range hints {
		if h.Kind == HintSemantic && overlapsAny(c.Src, []Range{h.Range}) {
			return h.Semantic, true
		}
	}
	return "", false
}

// semanticGated 是可选的规则能力：实现它的规则只在存在匹配类别的语义提示覆盖候选区间时
// 才产出候选（V3.1 §6.4/§10.1，歧义类别如比分/比率由提示消歧，无提示则保守保留）。
type semanticGated interface {
	SemanticGate() SemanticKind
}

func gatedRuleCovered(src Range, gate SemanticKind, hints []Hint) bool {
	for _, h := range hints {
		if h.Kind == HintSemantic && h.Semantic == gate && overlapsAny(src, []Range{h.Range}) {
			return true
		}
	}
	return false
}

func selectedRanges(selected []Candidate) []Range {
	ranges := make([]Range, len(selected))
	for i, s := range selected {
		ranges[i] = s.Src
	}
	return ranges
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
