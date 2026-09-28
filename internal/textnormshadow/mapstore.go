package textnormshadow

import (
	"sort"

	"github.com/F31/ppts/internal/contextrule"
	"github.com/F31/ppts/internal/pronunciation"
	oldtn "github.com/F31/ppts/internal/textnorm"
)

// FromPronunciationRules converts stored pronunciation rules to literal dict
// entries, mirroring internal/app.DictAdapter: only enabled rules with a
// non-empty pattern are applied.
func FromPronunciationRules(rules pronunciation.Rules) []DictEntry {
	if len(rules) == 0 {
		return nil
	}
	out := make([]DictEntry, 0, len(rules))
	for _, r := range rules {
		if !r.Enabled || r.Pattern == "" {
			continue
		}
		out = append(out, DictEntry{Match: r.Pattern, Replacement: r.Replacement})
	}
	return out
}

// FromContextRules converts stored contextual rule records to shadow entries,
// mirroring internal/app registration: only enabled rows, ordered by priority
// (then id) for deterministic comparison.
func FromContextRules(recs []*contextrule.Record) []ContextEntry {
	if len(recs) == 0 {
		return nil
	}
	enabled := make([]*contextrule.Record, 0, len(recs))
	for _, r := range recs {
		if r != nil && r.Enabled {
			enabled = append(enabled, r)
		}
	}
	sort.SliceStable(enabled, func(i, j int) bool {
		if enabled[i].Priority != enabled[j].Priority {
			return enabled[i].Priority < enabled[j].Priority
		}
		return enabled[i].ID < enabled[j].ID
	})
	out := make([]ContextEntry, len(enabled))
	for i, r := range enabled {
		out[i] = ContextEntry{Pattern: r.Pattern, Replacement: r.Replacement, Priority: r.Priority}
	}
	return out
}

// FromPersistence assembles shadow Options from the actual stored types plus
// the app-level switches (classes, number mode).
func FromPersistence(lang string, dict pronunciation.Rules, ctxRules []*contextrule.Record, classes []string, numMode *oldtn.NumberMode) Options {
	return Options{
		Lang:       lang,
		Dict:       FromPronunciationRules(dict),
		Context:    FromContextRules(ctxRules),
		Classes:    classes,
		NumberMode: numMode,
	}
}
