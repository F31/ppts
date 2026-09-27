# textnorm

`pkg/textnorm` is the draft V3.1 Chinese TTS text normalization contract.

The package is intentionally independent from PPTS application concerns. It does not depend on tenants, databases, HTTP handlers, environment variables, or TTS providers.

## Semantics

- Rules inspect the original input text.
- Candidate replacements use rune-indexed half-open ranges.
- Protected ranges block automatic edits.
- Preserve candidates reserve a recognized range without changing output.
- Selected candidates do not overlap.
- Replacements are applied once; rule output is not fed into later rules.
- Source spans distinguish identity, replacement, deletion, and insertion relationships.

## Draft Chinese Rules

- `TechnicalModelRule()` preserves common letter-led technical model identifiers such as `RTX5090Ti`, `i7-11800H`, and `USB3`.
- `ZHDateRule(ModeConvert)` converts valid `yyyy-mm-dd`, `yyyy/mm/dd`, and `yyyy.mm.dd` dates.
- `ZHDateRule(ModePreserve)` recognizes and protects valid date ranges without changing output.
- `ZHPercentRule()` converts percent forms such as `13.5%` to `百分之十三点五`.
- `ZHUnitRule()` converts common digit-led unit forms such as `5GHz` and `3.5GB`.
- `ZHDecimalRule()` converts simple decimal forms such as `13.5`.

Date rules have higher priority than decimal rules, so `2024.01.28` is handled as one date candidate instead of being split into decimal fragments. In preserve mode, the date remains unchanged but still blocks lower-priority decimal edits inside the same range.

Technical model preservation is intentionally narrow: digit-led unit forms such as `5GHz` are not protected by `TechnicalModelRule()` and should be handled by unit/quantity rules instead.

Percent and unit rules convert the whole numeric+unit span as one candidate, so `13.5%` is not first split into a decimal and then re-read. Technical model preservation has higher priority than unit conversion, so `USB3.0` stays protected instead of being read as a decimal unit.

`ZHVersionPreserveRule()` preserves letter-led dotted versions such as `v3.1.2` and `M3.2.1`. It requires a letter prefix so bare year-like sequences such as `2024.01.28` stay available to date rules.

`LiteralDictRule(id, priority, entries)` migrates the PPTS pronunciation-dictionary concept into the candidate model: each occurrence of a literal is an independent candidate range with a whole/embedded boundary policy. Replacements never cascade into later rule input, so a dictionary hit like `Model3` blocks decimal rules from splitting the `3`.

## Corpus

Behavioral corpus lives in JSON under `testdata/corpus/`. Positive, negative, ambiguous, overlap, and date-specific cases are data-driven rather than Go tables, so annotators and other implementations share the same contract. See `corpus.go` for the schema (`LoadCorpus`, `CheckCorpusCase`). Engine invariants (non-cascading, protected ranges, source map kinds, cancellation) are asserted in Go tests.

## Profiles

Built-in versioned rule combinations are registered in `profiles.go` and resolved with `ProfileRules(name)`: `zh.conservative`, `zh.date-decimal`, `zh.date-preserve`, and `zh.dict-demo`.

## CLI

`cmd/textnorm` is an independent CLI (no PPTS imports) used to run corpus assertions and produce behavior difference reports:

```sh
go run ./cmd/textnorm run pkg/textnorm/testdata/corpus/positive.json
go run ./cmd/textnorm diff -base zh.date-decimal -next zh.date-preserve pkg/textnorm/testdata/corpus/date-decimal.json
```

Both commands accept `-format json` for machine-readable release artifacts.

## Minimal Example

```go
engine, err := textnorm.Compile(textnorm.Config{
    Rules: []textnorm.CandidateRule{
        textnorm.RegexRule{
            ID:          "percent",
            Category:    "percent",
            Priority:    10,
            Match:       regexp.MustCompile(`12%`),
            Replacement: "百分之十二",
        },
    },
})
if err != nil {
    return err
}

res, err := engine.Normalize(ctx, textnorm.Request{Text: "增长12%"})
if err != nil {
    return err
}

fmt.Println(res.Text) // 增长百分之十二
```

This package is currently a draft API. It is not yet the PPTS production normalization path.
