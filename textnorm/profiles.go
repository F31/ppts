package textnorm

import "fmt"

// Built-in profile names (V3.1 §7.3 configuration combinations).
const (
	ProfileConservative = "zh.conservative"
	ProfileDateDecimal  = "zh.date-decimal"
	ProfileDatePreserve = "zh.date-preserve"
	ProfileDictDemo     = "zh.dict-demo"
)

// ProfileRules returns the candidate rules for a built-in profile name.
// Consumers may still build their own Config; these combinations are the
// versioned defaults the component ships and tests against.
func ProfileRules(name string) ([]CandidateRule, error) {
	switch name {
	case ProfileConservative:
		return []CandidateRule{
			ZHVersionPreserveRule(),
			TechnicalModelRule(),
			ZHDateRule(ModeConvert),
			ZHPercentRule(),
			ZHUnitRule(),
			ZHDecimalRule(),
		}, nil
	case ProfileDateDecimal:
		return []CandidateRule{ZHDateRule(ModeConvert), ZHDecimalRule()}, nil
	case ProfileDatePreserve:
		return []CandidateRule{ZHDateRule(ModePreserve), TechnicalModelRule(), ZHDecimalRule()}, nil
	case ProfileDictDemo:
		return []CandidateRule{
			LiteralDictRule("dict", 10, []LiteralEntry{
				{Match: "Model3", Replace: "Model三", Whole: true},
				{Match: "CUDA", Replace: "库达", Whole: true},
			}),
			ZHDecimalRule(),
		}, nil
	}
	return nil, fmt.Errorf("textnorm: unknown profile %q", name)
}
