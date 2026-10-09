package corpus

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/F31/textnorm"
)

// TestCorpusJSON runs every JSON corpus file under ../testdata/corpus and asserts
// text, edit rule order, and diagnostic counts.
func TestCorpusJSON(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "testdata", "corpus", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no corpus files found")
	}
	var total int
	for _, f := range files {
		c, err := loadCorpusFile(t, f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		rules, err := textnorm.ProfileRules(c.Profile)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		engine, err := textnorm.Compile(textnorm.Config{Rules: rules})
		if err != nil {
			t.Errorf("%s: compile: %v", f, err)
			continue
		}
		for _, cs := range c.Cases {
			total++
			_, errs := CheckCorpusCase(context.Background(), engine, cs)
			for _, e := range errs {
				t.Errorf("%s: %v", f, e)
			}
		}
	}
	if t.Failed() {
		t.Fatalf("corpus run failed (checked %d cases)", total)
	}
}

// TestNumberBoundaryCorpus exercises quantity-mode Chinese number boundaries.
func TestNumberBoundaryCorpus(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "testdata", "corpus", "number-boundaries.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c, err := LoadCorpus(f)
	if err != nil {
		t.Fatal(err)
	}
	e, err := textnorm.Compile(textnorm.Config{Rules: []textnorm.CandidateRule{
		textnorm.ZHNumberRule(textnorm.NumberModeQuantity),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, cs := range c.Cases {
		t.Run(cs.ID, func(t *testing.T) {
			_, errs := CheckCorpusCase(context.Background(), e, cs)
			for _, err := range errs {
				t.Error(err)
			}
		})
	}
}

func loadCorpusFile(t *testing.T, path string) (Corpus, error) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return Corpus{}, err
	}
	defer f.Close()
	return LoadCorpus(f)
}
