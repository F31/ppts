package textnorm

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestCorpusJSON runs every JSON corpus file under testdata/corpus and asserts
// text, edit rule order, and diagnostic counts.
func TestCorpusJSON(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "corpus", "*.json"))
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
		rules, err := ProfileRules(c.Profile)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		engine, err := Compile(Config{Rules: rules})
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

func loadCorpusFile(t *testing.T, path string) (Corpus, error) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return Corpus{}, err
	}
	defer f.Close()
	return LoadCorpus(f)
}
