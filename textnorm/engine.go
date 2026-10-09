package textnorm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Config is compiled into an immutable Engine.
type Config struct {
	Rules []CandidateRule
	// PauseMS is the default pause duration for legacy ‖ markers (default 300).
	PauseMS int
	// Profile and ProfileVersion identify a named rule combination. Custom
	// configurations may leave both fields at their zero values.
	Profile        string
	ProfileVersion int
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
	manifest := Manifest{Version: Version, Profile: cfg.Profile, ProfileVersion: cfg.ProfileVersion, Rules: ids}
	manifest.ConfigHash = configHash(manifest, rules, pauseMS)
	return &Engine{rules: rules, manifest: manifest, pauseMS: pauseMS}, nil
}

func configHash(m Manifest, rules []CandidateRule, pauseMS int) string {
	type ruleInfo struct {
		ID       string `json:"id"`
		Priority int    `json:"priority"`
	}
	payload := struct {
		Version        string     `json:"version"`
		Profile        string     `json:"profile,omitempty"`
		ProfileVersion int        `json:"profileVersion,omitempty"`
		PauseMS        int        `json:"pauseMs"`
		Rules          []ruleInfo `json:"rules"`
	}{Version: m.Version, Profile: m.Profile, ProfileVersion: m.ProfileVersion, PauseMS: pauseMS}
	for _, r := range rules {
		payload.Rules = append(payload.Rules, ruleInfo{ID: r.RuleID(), Priority: r.RulePriority()})
	}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Manifest returns a copy of the engine identity and configuration fingerprint.
// It lets callers record or compare behavior contracts without normalizing a
// sample string first.
func (e *Engine) Manifest() Manifest {
	if e == nil {
		return Manifest{}
	}
	m := e.manifest
	m.Rules = append([]string(nil), e.manifest.Rules...)
	return m
}
