package textnorm

import (
	"github.com/F31/textnorm/rules"
	"github.com/F31/textnorm/rules/en"
	"github.com/F31/textnorm/rules/zh"
)

// LiteralEntry is one exact-match dictionary replacement.
type LiteralEntry = rules.LiteralEntry

// LiteralDictRule converts literal dictionary entries into independent
// candidate ranges over the original text. Replacements never cascade.
func LiteralDictRule(id string, priority int, entries []LiteralEntry) CandidateRule {
	return rules.LiteralDictRule(id, priority, entries)
}

// RegexRule is a minimal declarative rule for exact-range template replacements.
type RegexRule = rules.RegexRule

// ZHNumberRule converts independent integer tokens to their Chinese reading.
func ZHNumberRule(mode NumberMode) CandidateRule { return zh.NumberRule(mode) }

// ZHDateRule recognizes yyyy-mm-dd, yyyy/mm/dd, and yyyy.mm.dd date forms.
func ZHDateRule(mode Mode) CandidateRule { return zh.DateRule(mode) }

// ZHPercentRule converts percent forms such as 13.5% to 百分之十三点五.
func ZHPercentRule() CandidateRule { return zh.PercentRule() }

// ZHUnitRule converts common digit-led unit forms such as 5GHz and 3.5GB.
func ZHUnitRule() CandidateRule { return zh.UnitRule() }

// ZHDecimalRule converts simple decimal forms such as 13.5.
func ZHDecimalRule() CandidateRule { return zh.DecimalRule() }

// ZHMoneyRule converts money expressions such as ¥250 / USD 1.5 / 5万.
func ZHMoneyRule() CandidateRule { return zh.MoneyRule() }

// ZHTelephoneRule reads CN mobile numbers (1[3-9] + 9 digits) digit by digit.
func ZHTelephoneRule() CandidateRule { return zh.TelephoneRule() }

// ZHScoreRule reads X:Y as X比Y, gated on a SemanticScore hint.
func ZHScoreRule() CandidateRule { return zh.ScoreRule() }

// ZHFractionRule reads 1/3 as 三分之一, requiring a covering SemanticFraction hint.
func ZHFractionRule() CandidateRule { return zh.FractionRule() }

// ZHTimeRule reads 24-hour HH:mm[:ss] forms, requiring a covering SemanticTime hint.
func ZHTimeRule() CandidateRule { return zh.TimeRule() }

// ZHOrdinalRule reads explicit ordinals such as 第12 → 第十二.
func ZHOrdinalRule() CandidateRule { return zh.OrdinalRule() }

// ZHEnglishMoneyRule reads currency-prefixed English magnitudes in Chinese.
func ZHEnglishMoneyRule() CandidateRule { return zh.EnglishMoneyRule() }

// TechnicalModelRule preserves common letter-led technical model identifiers.
func TechnicalModelRule() CandidateRule { return zh.TechnicalModelRule() }

// ZHVersionPreserveRule preserves letter-led dotted version identifiers.
func ZHVersionPreserveRule() CandidateRule { return zh.VersionPreserveRule() }

// ENNumberRule converts independent integer tokens to their English reading.
func ENNumberRule(mode ENNumberMode) CandidateRule { return en.NumberRule(mode) }

// ENDecimalRule converts simple decimal forms such as 3.14 to English speech.
func ENDecimalRule() CandidateRule { return en.DecimalRule() }

// ENPercentRule converts percent forms such as 12.5% to English speech.
func ENPercentRule() CandidateRule { return en.PercentRule() }

// ENOrdinalRule reads explicit English ordinals such as 21st → "twenty-first".
func ENOrdinalRule() CandidateRule { return en.OrdinalRule() }

// ENMoneyRule reads currency-prefixed amounts such as $250 and USD 1.5.
func ENMoneyRule() CandidateRule { return en.MoneyRule() }

// ENDateRule reads ISO-style dates (2024-01-28) in US English order.
func ENDateRule() CandidateRule { return en.DateRule() }

// ENTimeRule reads 24-hour HH:mm[:ss] forms, requiring a covering SemanticTime hint.
func ENTimeRule() CandidateRule { return en.TimeRule() }

// ENUnitRule reads digit-led unit forms such as 5GHz and 3.5GB with plurality.
func ENUnitRule() CandidateRule { return en.UnitRule() }

// ENAbbrevRule expands explicit weekday/month abbreviations such as Mon. and Jan.
func ENAbbrevRule() CandidateRule { return en.AbbrevRule() }
