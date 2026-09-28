// Command textnorm-shadow runs the legacy vs candidate engine shadow comparison
// (V3.1 §14.3) over a JSON spec of segments and reports text/control diffs.
// It never changes PPTS production output; it is the batch replay tool for
// evaluating equivalence before any switch.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	oldtn "github.com/F31/ppts/internal/textnorm"
	"github.com/F31/ppts/internal/textnormshadow"
)

type dictSpec struct {
	Match       string `json:"match"`
	Replacement string `json:"replacement"`
}

type contextSpec struct {
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
	Priority    int    `json:"priority"`
}

type specJSON struct {
	Lang       string        `json:"lang"`
	Dict       []dictSpec    `json:"dict"`
	Context    []contextSpec `json:"context"`
	Classes    []string      `json:"classes"`
	NumberMode string        `json:"number_mode"`
	Segments   []string      `json:"segments"`
}

func main() {
	fs := flag.NewFlagSet("textnorm-shadow", flag.ExitOnError)
	format := fs.String("format", "text", "output format: text|json")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	files := fs.Args()
	if len(files) != 1 {
		fmt.Fprintln(os.Stderr, "usage: textnorm-shadow [-format text|json] spec.json")
		os.Exit(2)
	}
	sp, err := loadSpec(files[0])
	if err != nil {
		fatal(err)
	}
	reports, notice, err := runSpec(sp)
	if err != nil {
		fatal(err)
	}
	emit(*format, sp, reports, notice)
}

func loadSpec(path string) (specJSON, error) {
	var sp specJSON
	f, err := os.Open(path)
	if err != nil {
		return sp, err
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&sp); err != nil {
		return sp, fmt.Errorf("textnorm-shadow: decode spec: %w", err)
	}
	return sp, nil
}

func runSpec(sp specJSON) ([]textnormshadow.Report, []string, error) {
	mode, err := parseMode(sp.NumberMode)
	if err != nil {
		return nil, nil, err
	}
	spec := textnormshadow.Spec{
		Lang:       sp.Lang,
		Classes:    sp.Classes,
		NumberMode: mode,
		Segments:   sp.Segments,
	}
	for _, d := range sp.Dict {
		spec.Dict = append(spec.Dict, textnormshadow.DictEntry{Match: d.Match, Replacement: d.Replacement})
	}
	for _, c := range sp.Context {
		spec.Context = append(spec.Context, textnormshadow.ContextEntry{Pattern: c.Pattern, Replacement: c.Replacement, Priority: c.Priority})
	}
	return textnormshadow.CompareSpec(spec)
}

func parseMode(s string) (*oldtn.NumberMode, error) {
	switch s {
	case "", "none":
		return nil, nil
	case "quantity":
		m := oldtn.NumberModeQuantity
		return &m, nil
	case "year":
		m := oldtn.NumberModeYear
		return &m, nil
	}
	return nil, fmt.Errorf("textnorm-shadow: unknown number_mode %q", s)
}

func emit(format string, sp specJSON, reports []textnormshadow.Report, notice []string) {
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]interface{}{
			"lang":    sp.Lang,
			"notice":  notice,
			"reports": reports,
		}); err != nil {
			fatal(err)
		}
	default:
		same, diff := 0, 0
		for i, r := range reports {
			mark := "SAME"
			if !r.Same {
				mark = "DIFF"
				diff++
			} else {
				same++
			}
			fmt.Printf("[%d] %-4s %q\n", i, mark, r.Input)
			fmt.Printf("     old: %q  pauses=%v\n", r.OldText, r.OldPauses)
			fmt.Printf("     new: %q  pauses=%v\n", r.NewText, r.NewPauses)
			if r.NewError != "" {
				fmt.Printf("     new error: %s\n", r.NewError)
			}
		}
		if len(notice) > 0 {
			fmt.Printf("notice: %v\n", notice)
		}
		fmt.Printf("summary: %d same, %d diff\n", same, diff)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "textnorm-shadow: %v\n", err)
	os.Exit(1)
}
