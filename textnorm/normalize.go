package textnorm

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

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
	req.IndexBytes()
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
				Src: Range{Start: p, End: p + 1}, Replacement: "",
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
			if g, ok := r.(interface {
				CandidateRule
				SemanticGate() SemanticKind
			}); ok && !gatedRuleCovered(c.Src, g.SemanticGate(), hints) {
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
		Manifest:    e.Manifest(),
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
		pr := Range{Start: p, End: p + 1}
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
