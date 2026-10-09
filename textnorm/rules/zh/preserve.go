package zh

import (
	"regexp"

	"github.com/F31/textnorm/internal/contract"
)

// TechnicalModelRule preserves common letter-led technical model identifiers,
// such as RTX5090Ti and i7-11800H. It intentionally does not match digit-led
// unit forms such as 5GHz; those belong to unit/quantity rules.
func TechnicalModelRule() contract.CandidateRule {
	return technicalModelRule{}
}

type technicalModelRule struct{}

var technicalModelRe = regexp.MustCompile(`[A-Za-z]+[0-9][A-Za-z0-9]*(?:-[A-Za-z0-9]+)*`)

// VersionPreserveRule preserves letter-led dotted version identifiers such as
// v3.1.2 and M3.2.1. It requires a letter prefix so bare year-like sequences
// (for example 2024.01.28) remain available to date rules instead of being
// preserved as versions.
func VersionPreserveRule() contract.CandidateRule {
	return versionPreserveRule{}
}

type versionPreserveRule struct{}

var versionRe = regexp.MustCompile(`(?:[Vv][0-9]+|[A-Za-z]+[0-9]+)(?:\.[0-9]+)+`)

func (versionPreserveRule) RuleID() string    { return "zh.version.preserve" }
func (versionPreserveRule) RulePriority() int { return 4 }

func (versionPreserveRule) FindCandidates(req contract.Request) []contract.Candidate {
	locs := versionRe.FindAllStringIndex(req.Text, -1)
	out := make([]contract.Candidate, 0, len(locs))
	for _, loc := range locs {
		if !contract.ASCIIBoundary(req.Text, loc[0], loc[1]) {
			continue
		}
		out = append(out, contract.Candidate{
			Src:      contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(loc[1])},
			RuleID:   "zh.version.preserve",
			Category: "version",
			Priority: 4,
			Preserve: true,
		})
	}
	return out
}

func (technicalModelRule) RuleID() string    { return "technical.model.preserve" }
func (technicalModelRule) RulePriority() int { return 5 }

func (technicalModelRule) FindCandidates(req contract.Request) []contract.Candidate {
	locs := technicalModelRe.FindAllStringIndex(req.Text, -1)
	out := make([]contract.Candidate, 0, len(locs))
	for _, loc := range locs {
		if !contract.ASCIIBoundary(req.Text, loc[0], loc[1]) {
			continue
		}
		out = append(out, contract.Candidate{
			Src:      contract.Range{Start: req.RuneOffset(loc[0]), End: req.RuneOffset(loc[1])},
			RuleID:   "technical.model.preserve",
			Category: "technical_model",
			Priority: 5,
			Preserve: true,
		})
	}
	return out
}
