// Command textnorm is an independent CLI for the V3.1 Chinese TTS text
// normalization component. It runs JSON corpus files against built-in profiles
// and reports text-level behavior, so release tooling can produce behavior
// difference reports without a Go consumer.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	textnorm "github.com/F31/textnorm"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	switch args[0] {
	case "run":
		runCmd(args[1:])
	case "diff":
		diffCmd(args[1:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "textnorm: unknown command %q\n", args[0])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: textnorm <command> [flags] corpus.json...")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  run   normalize corpus cases and assert expectations")
	fmt.Fprintln(os.Stderr, "  diff  compare two profiles and report text differences")
}

func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	profile := fs.String("profile", "", "override profile name (default: corpus file profile)")
	format := fs.String("format", "text", "output format: text|json")
	fs.Parse(args)
	files := sortFiles(fs.Args())
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "textnorm run: no corpus files")
		os.Exit(2)
	}

	ctx := context.Background()
	type caseRow struct {
		ID          string   `json:"id"`
		Input       string   `json:"input"`
		Output      string   `json:"output"`
		Edits       []string `json:"edits"`
		Diagnostics int      `json:"diagnostics"`
		Pass        bool     `json:"pass"`
		Error       string   `json:"error,omitempty"`
	}
	type fileRow struct {
		File     string            `json:"file"`
		Profile  string            `json:"profile"`
		Manifest textnorm.Manifest `json:"manifest"`
		Cases    []caseRow         `json:"cases"`
	}
	var report []fileRow
	totalPass, totalFail := 0, 0

	for _, f := range files {
		corpus, err := loadCorpusFile(f)
		if err != nil {
			fatalErr(fmt.Errorf("run: %v", err))
		}
		name := *profile
		if name == "" {
			name = corpus.Profile
		}
		engine := compileProfile(name)
		rows := make([]caseRow, 0, len(corpus.Cases))
		for _, cs := range corpus.Cases {
			res, errs := textnorm.CheckCorpusCase(ctx, engine, cs)
			row := caseRow{
				ID:          cs.ID,
				Input:       cs.Text,
				Output:      res.Text,
				Edits:       edits(res.Edits),
				Diagnostics: len(res.Diagnostics),
			}
			if len(errs) == 0 {
				row.Pass = true
				totalPass++
			} else {
				totalFail++
				row.Error = errs[0].Error()
			}
			rows = append(rows, row)
		}
		report = append(report, fileRow{File: f, Profile: name, Manifest: engine.Manifest(), Cases: rows})
	}

	switch *format {
	case "json":
		out := map[string]interface{}{"files": report, "pass": totalPass, "fail": totalFail}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fatalErr(err)
		}
	default:
		for _, fr := range report {
			fmt.Printf("FILE %s (profile %s)\n", fr.File, fr.Profile)
			for _, r := range fr.Cases {
				status := "PASS"
				if !r.Pass {
					status = "FAIL"
				}
				fmt.Printf("  %-4s %-42s %q -> %q", status, r.ID, r.Input, r.Output)
				if len(r.Edits) > 0 {
					fmt.Printf(" edits=%v", r.Edits)
				}
				if !r.Pass {
					fmt.Printf("  (%s)", r.Error)
				}
				fmt.Println()
			}
		}
		fmt.Printf("summary: %d pass, %d fail\n", totalPass, totalFail)
		if totalFail > 0 {
			os.Exit(1)
		}
	}
}

func diffCmd(args []string) {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	base := fs.String("base", "", "base profile")
	next := fs.String("next", "", "candidate profile")
	format := fs.String("format", "text", "output format: text|json")
	fs.Parse(args)
	files := sortFiles(fs.Args())
	if *base == "" || *next == "" || len(files) == 0 {
		fmt.Fprintln(os.Stderr, "textnorm diff: -base and -next profiles and at least one corpus file are required")
		os.Exit(2)
	}

	baseEngine := compileProfile(*base)
	nextEngine := compileProfile(*next)
	baseManifest := baseEngine.Manifest()
	nextManifest := nextEngine.Manifest()
	ctx := context.Background()

	type diffRow struct {
		ID        string   `json:"id"`
		Input     string   `json:"input"`
		Base      string   `json:"base"`
		Next      string   `json:"next"`
		BaseEdits []string `json:"base_edits"`
		NextEdits []string `json:"next_edits"`
	}
	var diffs []diffRow

	for _, f := range files {
		corpus, err := loadCorpusFile(f)
		if err != nil {
			fatalErr(fmt.Errorf("diff: %v", err))
		}
		for _, cs := range corpus.Cases {
			resBase, err := baseEngine.Normalize(ctx, textnorm.Request{Text: cs.Text, Protected: cs.Protected})
			if err != nil {
				fatalErr(err)
			}
			resNext, err := nextEngine.Normalize(ctx, textnorm.Request{Text: cs.Text, Protected: cs.Protected})
			if err != nil {
				fatalErr(err)
			}
			if resBase.Text != resNext.Text {
				diffs = append(diffs, diffRow{
					ID:        cs.ID,
					Input:     cs.Text,
					Base:      resBase.Text,
					Next:      resNext.Text,
					BaseEdits: edits(resBase.Edits),
					NextEdits: edits(resNext.Edits),
				})
			}
		}
	}

	switch *format {
	case "json":
		out := map[string]interface{}{
			"base":          *base,
			"next":          *next,
			"base_manifest": baseManifest,
			"next_manifest": nextManifest,
			"differences":   diffs,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fatalErr(err)
		}
	default:
		fmt.Printf("DIFF %s -> %s\n", *base, *next)
		fmt.Printf("  base manifest: version=%s profile=%s/%d hash=%s\n", baseManifest.Version, baseManifest.Profile, baseManifest.ProfileVersion, baseManifest.ConfigHash)
		fmt.Printf("  next manifest: version=%s profile=%s/%d hash=%s\n", nextManifest.Version, nextManifest.Profile, nextManifest.ProfileVersion, nextManifest.ConfigHash)
		for _, d := range diffs {
			fmt.Printf("  %-42s %q\n", d.ID, d.Input)
			fmt.Printf("    %-8s %q  edits=%v\n", *base, d.Base, d.BaseEdits)
			fmt.Printf("    %-8s %q  edits=%v\n", *next, d.Next, d.NextEdits)
		}
		fmt.Printf("summary: %d behavioral differences\n", len(diffs))
	}
}

func compileProfile(name string) *textnorm.Engine {
	engine, err := textnorm.CompileProfile(name)
	if err != nil {
		fatalErr(err)
	}
	return engine
}

func loadCorpusFile(path string) (textnorm.Corpus, error) {
	f, err := os.Open(path)
	if err != nil {
		return textnorm.Corpus{}, err
	}
	defer f.Close()
	return textnorm.LoadCorpus(f)
}

func edits(es []textnorm.Edit) []string {
	ids := make([]string, len(es))
	for i, e := range es {
		ids[i] = e.RuleID
	}
	return ids
}

func sortFiles(files []string) []string {
	out := append([]string(nil), files...)
	sort.Strings(out)
	return out
}

func fatalErr(err error) {
	fmt.Fprintf(os.Stderr, "textnorm: %v\n", err)
	os.Exit(1)
}
