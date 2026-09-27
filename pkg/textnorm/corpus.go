package textnorm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// Corpus is a set of normalization cases stored as data. Keeping the corpus in
// JSON lets human annotators, reviewers, and other implementations share the
// same behavioral contract without editing Go code, and lets release tooling
// produce text-level behavior difference reports.
type Corpus struct {
	Profile string       `json:"profile"`
	Cases   []CorpusCase `json:"cases"`
}

// CorpusCase is one normalization expectation.
type CorpusCase struct {
	ID        string      `json:"id"`
	Text      string      `json:"text"`
	Protected []Range     `json:"protected,omitempty"`
	Expect    Expectation `json:"expect"`
	Note      string      `json:"note,omitempty"`
}

// Expectation describes the assertions for one corpus case. Omitted fields are
// not asserted; an explicitly empty edits list asserts that no edit occurred.
type Expectation struct {
	Text        string   `json:"text,omitempty"`
	Edits       []string `json:"edits,omitempty"`
	NoEdits     bool     `json:"no_edits,omitempty"`
	Diagnostics *int     `json:"diagnostics,omitempty"`
}

// LoadCorpus decodes one JSON corpus file.
func LoadCorpus(r io.Reader) (Corpus, error) {
	var c Corpus
	if err := json.NewDecoder(r).Decode(&c); err != nil {
		return Corpus{}, fmt.Errorf("textnorm corpus: %w", err)
	}
	for _, cs := range c.Cases {
		if cs.ID == "" {
			return Corpus{}, fmt.Errorf("textnorm corpus: case with empty id")
		}
	}
	return c, nil
}

// CheckCorpusCase runs one case against an engine and reports assertion failures.
func CheckCorpusCase(ctx context.Context, engine *Engine, cs CorpusCase) (Result, []error) {
	res, err := engine.Normalize(ctx, Request{Text: cs.Text, Protected: cs.Protected})
	if err != nil {
		return res, []error{fmt.Errorf("%s: normalize: %w", cs.ID, err)}
	}
	var errs []error
	if cs.Expect.Text != "" && res.Text != cs.Expect.Text {
		errs = append(errs, fmt.Errorf("%s: text = %q, want %q", cs.ID, res.Text, cs.Expect.Text))
	}
	if cs.Expect.Edits != nil {
		if len(res.Edits) != len(cs.Expect.Edits) {
			errs = append(errs, fmt.Errorf("%s: edits = %v, want %v", cs.ID, editIDs(res.Edits), cs.Expect.Edits))
		} else {
			for i := range cs.Expect.Edits {
				if res.Edits[i].RuleID != cs.Expect.Edits[i] {
					errs = append(errs, fmt.Errorf("%s: edit[%d] = %q, want %q", cs.ID, i, res.Edits[i].RuleID, cs.Expect.Edits[i]))
				}
			}
		}
	}
	if cs.Expect.NoEdits && len(res.Edits) != 0 {
		errs = append(errs, fmt.Errorf("%s: expected no edits, got %v", cs.ID, editIDs(res.Edits)))
	}
	if cs.Expect.Diagnostics != nil && len(res.Diagnostics) != *cs.Expect.Diagnostics {
		errs = append(errs, fmt.Errorf("%s: diagnostics = %d, want %d", cs.ID, len(res.Diagnostics), *cs.Expect.Diagnostics))
	}
	return res, errs
}

func editIDs(edits []Edit) []string {
	ids := make([]string, len(edits))
	for i, e := range edits {
		ids[i] = e.RuleID
	}
	return ids
}
