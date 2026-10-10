# textnorm

`textnorm` is an independent Go module (module path `github.com/F31/textnorm`) hosting the draft V3.1 Chinese TTS text normalization contract.

The module is intentionally independent from PPTS application concerns. It does not depend on tenants, databases, HTTP handlers, environment variables, or TTS providers, and it carries its own `go.mod`, corpus, and CI job. It can be built and tested on its own:

```sh
cd textnorm
go test ./...
```

## Semantics

- Rules inspect the original input text.
- Candidate replacements use rune-indexed half-open ranges.
- Protected ranges block automatic edits.
- Preserve candidates reserve a recognized range without changing output.
- Selected candidates do not overlap.
- Replacements are applied once; rule output is not fed into later rules.
- Source spans distinguish identity, replacement, deletion, and insertion relationships.

## Hints

`Request.Hints` carries explicit caller constraints (`Hint{Kind, Range, Text, Semantic}`):

- `HintVerbatim` keeps a range exactly as written.
- `HintReading` replaces a range with caller-provided reading text.
- `HintSemantic` marks a range with a category (`SemanticNumber`, `SemanticDate`, `SemanticTelephone`, `SemanticRatio`, `SemanticScore`, `SemanticFraction`, `SemanticTime`); only rules whose `Category` matches may convert it, otherwise the range is preserved.

Hints must not overlap each other or protected ranges; contradictory hints are rejected instead of silently guessed. Explicit hints take precedence over automatic candidates.

## Legacy Markup

`Request.LegacyMarkup` opts into the legacy compatibility parser (V3.1 §10.2):

- `〔读：x〕` expands to reading text `x`; the full marker span is one replacement candidate, so rules cannot re-read the content.
- `‖` emits a `Control{AfterRunes, DurationMS}` pause event and is removed from the output.

Plain text does not auto-interpret these characters unless `LegacyMarkup` is true. Unclosed reading markers are kept as literal text. `StripLegacyMarkers(text)` returns display text plus per-rune source indices, and `DisplayToEffective(original, result)` bridges display runes to effective rune positions for subtitle/timeline alignment (approximate inside unequal-length replacement spans).

## Package layout

The module is split by responsibility so language rule sets stay independent of the engine:

```text
textnorm/
├── internal/contract     shared request/candidate/rule contract
├── rules/                language-neutral rules (literal dict, regex)
│   ├── zh/               Chinese spoken-language rules
│   └── en/               English spoken-language rules
├── corpus/               JSON corpus loader + assertion runner
├── engine.go             Compile / Engine
├── normalize.go          Normalize orchestration
├── selection.go          candidate selection + output build
├── validation.go         hints/ranges validation
├── markup.go             legacy 〔读：x〕/‖ compatibility + display mapping
├── types.go              public aliases + result types
├── rules.go              re-exported rule constructors (backward-compatible)
└── profiles.go           versioned rule combinations
```

Rules depend only on `internal/contract`; the root package re-exports every
rule constructor (`ZH*`, `EN*`, `LiteralDictRule`, `RegexRule`, ...) so existing
PPTS adapters keep compiling unchanged.

## Draft Chinese Rules

- `TechnicalModelRule()` preserves common letter-led technical model identifiers such as `RTX5090Ti`, `i7-11800H`, and `USB3`.
- `ZHDateRule(ModeConvert)` converts valid `yyyy-mm-dd`, `yyyy/mm/dd`, and `yyyy.mm.dd` dates.
- `ZHDateRule(ModePreserve)` recognizes and protects valid date ranges without changing output.
- `ZHPercentRule()` converts percent forms such as `13.5%` to `百分之十三点五`.
- `ZHMoneyRule()` converts money expressions such as `¥250`, `USD 1.5`, and `5万`.
- `ZHUnitRule()` converts common digit-led unit forms such as `5GHz` and `3.5GB`.
- `ZHDecimalRule()` converts simple decimal forms such as `13.5`.
- `ZHTelephoneRule()` reads CN mobile numbers (`1[3-9]` + 9 digits) digit by digit.
- `ZHScoreRule()` reads `X:Y` as `X比Y`, gated on a `SemanticScore` hint (ambiguous forms are conservative without the hint).
- `ZHFractionRule()` reads `1/3` as `三分之一`, requiring a covering `SemanticFraction` hint; zero denominators and slash-separated dates are not converted.
- `ZHTimeRule()` reads 24-hour `HH:mm[:ss]` forms, requiring a covering `SemanticTime` hint (`10:30` → `十点三十分`). Invalid clock times stay unchanged inside the hint.
- `ZHOrdinalRule()` reads explicit ordinals (`第12章` → `第十二章`).
- `ZHEnglishMoneyRule()` reads currency-prefixed English magnitudes in Chinese (`USD 1.5 million` → `美元一百五十万`, `A$500K` → `澳元五十万`). Supported scales: thousand/million/billion and K/M/B. Decimal shifting is exact; bare `500K` is not assumed to be money. Register with `ZHMoneyRule()` to let the longer magnitude candidate win over the shorter currency candidate.

Date rules have higher priority than decimal rules, so `2024.01.28` is handled as one date candidate instead of being split into decimal fragments. In preserve mode, the date remains unchanged but still blocks lower-priority decimal edits inside the same range.

Technical model preservation is intentionally narrow: digit-led unit forms such as `5GHz` are not protected by `TechnicalModelRule()` and should be handled by unit/quantity rules instead.

Percent and unit rules convert the whole numeric+unit span as one candidate, so `13.5%` is not first split into a decimal and then re-read. Technical model preservation has higher priority than unit conversion, so `USB3.0` stays protected instead of being read as a decimal unit.

`ZHVersionPreserveRule()` preserves letter-led dotted versions such as `v3.1.2` and `M3.2.1`. It requires a letter prefix so bare year-like sequences such as `2024.01.28` stay available to date rules.

`ZHNumberRule(mode)` expands independent integer tokens to Chinese readings (`NumberModeQuantity` reads by decimal place, `NumberModeYear` digit-by-digit, up to 12 digits). Digit runs bounded by ASCII letters/digits are left intact so model/unit forms such as `RTX5090Ti` and `5GHz` are not bisected.

`LiteralDictRule(id, priority, entries)` migrates the PPTS pronunciation-dictionary concept into the candidate model: each occurrence of a literal is an independent candidate range with a whole/embedded boundary policy. Replacements never cascade into later rule input, so a dictionary hit like `Model3` blocks decimal rules from splitting the `3`.

## English Rules

English rules live in `rules/en` and are exposed through the same root API:

- `ENNumberRule(ENNumberModeQuantity)` reads independent integers by place (`2048` → `two thousand forty-eight`, up to 12 digits).
- `ENNumberRule(ENNumberModeYear)` reads four-digit years by pairs (`2024` → `twenty twenty-four`, `1905` → `nineteen oh five`, `2000` → `two thousand`).
- `ENNumberRule(ENNumberModeDigits)` reads each digit and preserves leading zeros (`001` → `zero zero one`).
- `ENDecimalRule()` reads `3.14` → `three point one four` (fractional digits read one by one).
- `ENPercentRule()` reads `12.5%` → `twelve point five percent`, `12%` → `twelve percent`.
- `ENOrdinalRule()` reads explicit ordinals (`21st` → `twenty-first`, `3rd` → `third`).
- `ENMoneyRule()` reads currency-prefixed amounts (`$250` → `two hundred fifty US dollars`, `USD 1.5` → `one point five US dollars`, `¥5000` → `five thousand Japanese yen`). Uncountable currencies (`yen`) never inflect; a decimal amount always takes the plural currency.
- `ENDateRule()` reads ISO dates in US order (`2024-01-28` → `January twenty-eighth, twenty twenty-four`). A calendar-invalid date (e.g. `2024-02-30`) is preserved as-is so number rules do not split its digits.
- `ENTimeRule()` reads clock times. AM/PM suffixed 12-hour forms write as-is with the suffix spoken (`10:30 AM` → `ten thirty AM`) and need no hint; bare 24-hour `HH:mm[:ss]` forms (`10:30` → `ten thirty`, `14:05` → `fourteen oh five`) require a covering `SemanticTime` hint because clock notation is ambiguous with score/ratio.
- `ENUnitRule()` reads digit-led unit forms with plurality (`5GHz` → `five gigahertz`, `1 GB` → `one gigabyte`, `3.5 GB` → `three point five gigabytes`, `100 Mbps` → `one hundred megabits per second`). Uncountable units (hertz, bps-spelled) never inflect.
- `ENAbbrevRule()` expands explicit weekday/month abbreviations (`Mon.` → `Monday`, `Jan.` → `January`, `Sept` → `September`). Ambiguous bare forms (`May`, `Mar`) are left untouched.

Digit runs bounded by ASCII letters/digits are left intact (`RTX5090Ti`, `5GHz`), matching the Chinese number rule. The `en.conservative` profile combines technical-model/version preservation with English number, decimal, percent, ordinal, money, date, unit, and abbreviation rules (bare-time is hint-gated).

## Language Selection

Language-specific rule sets are chosen at assembly time, not at runtime per-token:

- `ppts` resolves engines per `(tenant, lang)` through its `TextPreparationFactory` / `TextPreparer` interface; the adapter passes the language into the builder.
- The ppts adapter routes `en*` languages to the English base rules and all other languages to the Chinese rule set (they are never mixed in one engine).
- Callers using this module directly assemble a `Config{Rules: ...}` or call `CompileProfile("zh.conservative" | "en.conservative")`; the compiled engine is language-fixed and immutable.

## Corpus

Behavioral corpus lives in JSON under `testdata/corpus/`. Positive, negative, ambiguous, overlap, and date-specific cases are data-driven rather than Go tables, so annotators and other implementations share the same contract. See `corpus.go` for the schema (`LoadCorpus`, `CheckCorpusCase`). Engine invariants (non-cascading, protected ranges, source map kinds, cancellation) are asserted in Go tests.

## Profiles

Built-in versioned rule combinations are registered in `profiles.go` and resolved with `ProfileRules(name)` or `CompileProfile(name)`: `zh.conservative`, `zh.date-decimal`, `zh.date-preserve`, `zh.dict-demo`, and `zh.quantity` (independent integer quantities). Existing profiles do not automatically enable the new expression rules. `CompileProfile` stamps `Result.Manifest.Profile`, `ProfileVersion`, and `ConfigHash`, which is the recommended release/audit path for external consumers.

## Performance and PPTS integration

### Dependency boundary and releases

PPTS narration depends on application-owned `TextPreparationFactory`, `TextPreparer`, and `PreparedText` contracts (`internal/app/textpreparation.go`), not on `*textnorm.Engine` or component request/result types. The adapter in `internal/app/textnorm.go` owns component construction, legacy-marker handling, pause conversion, and display-to-spoken mapping. Tenant/database loading and cache freshness remain application responsibilities. The existing `WithTextNorm` builder wiring is retained as a compatibility adapter; alternative implementations can use `WithTextPreparation` directly.

This is still a compile-time library dependency, intentionally not a network service. Runtime enablement is optional, but failures in an enabled preparation path are returned rather than silently bypassed.

The root `go.mod` names the published baseline `github.com/F31/textnorm v0.1.0`. Its local `replace` remains necessary because the current optimization and new rule APIs are not yet published. To switch production to release-based builds: publish a textnorm version containing these changes, update the root `require` to that version, remove the root `replace`, and verify with `GOWORK=off go test ./...` and `GOWORK=off go build ./...`. Local co-development can continue through the existing `go.work` entry for `./textnorm`. Until that switch, the actual build uses local source, not the published baseline.

`Normalize` computes a request-local UTF-8 byte-to-rune index for built-in regex rules. Candidate selection uses a sorted sweep and binary search over normalized protected ranges; source-map coordinates remain rune-indexed. Direct `FindCandidates(Request)` calls remain supported. Run `go test -run '^$' -bench BenchmarkNormalize -benchmem` for short, medium, and long mixed Chinese text workloads.

PPTS registers optional rules via `PPTS_TEXT_NORM_CLASSES` (`ordinal`, `english_money`, `fraction`, `time` in addition to the original classes). Its narration path does not automatically generate fraction/time hints; module callers can pass those explicitly. The production combination version is `ppts.v3.1` version 2, including the fix that combines number, class, and contextual-rule options.

PPTS workers cache immutable engines by tenant, language, and environment configuration. `PPTS_TEXT_NORM_ENGINE_TTL` defaults to `5m`; `PPTS_TEXT_NORM_ENGINE_MAX` defaults to 256 FIFO entries per worker. TTL expiry reloads dictionary/contextual rules, bounding staleness after edits across processes. Setting TTL to `0` or capacity to `0` disables caching. Errors are not cached; concurrent same-key builds share work and waiting callers can cancel. Cache hit/miss counts are exposed through `ppts_textnorm_engine_cache_total`.

## CLI

`cmd/textnorm` is an independent CLI (no PPTS imports) used to run corpus assertions and produce behavior difference reports:

```sh
go run ./cmd/textnorm run testdata/corpus/positive.json
go run ./cmd/textnorm diff -base zh.date-decimal -next zh.date-preserve testdata/corpus/date-decimal.json
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

This package is currently a draft API. PPTS uses it when `PPTS_TEXT_NORM_ENABLED=true`.

## Versioning

Module version is exposed as `Version` (currently `v0.2.2`) and carried on every `Result.Manifest`. `Manifest.ConfigHash` is a deterministic SHA-256 over module version, profile metadata, pause settings, and rule IDs/priorities. Compatibility policy (V3.1 §13):

- Within a major module version, the public API stays backward compatible.
- Behavioral changes to a named profile must bump that profile's combination version (`Profiles()` / `ProfileVersion`), independently of the module version.
- Same input + same profile version + same module version produces deterministic output.
- `ModulePath` is `github.com/F31/textnorm`; the monorepo keeps a local `replace` for CI stability until a pinned release is adopted.
