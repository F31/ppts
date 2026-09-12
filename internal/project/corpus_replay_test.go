//go:build corpus

// 语料回放测试：把 testdata/corpus/manifest.json 登记的样本逐一喂给读适配器，
// 断言可解析、页数与备注符合预期。源文件缺席（如外部金样未检出）则以 Skip 跳过。
package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type corpusManifest struct {
	SchemaVersion      string        `json:"schemaVersion"`
	ExternalCorpusRoot string        `json:"externalCorpusRoot"`
	Entries            []corpusEntry `json:"entries"`
}

type corpusEntry struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	ExternalPath string `json:"externalPath"`
	Pages        int    `json:"pages"`
	WithNotes    bool   `json:"withNotes"`
}

func loadCorpusManifest(t *testing.T) corpusManifest {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "testdata/corpus/manifest.json"))
	if err != nil {
		t.Fatalf("read corpus manifest: %v", err)
	}
	var m corpusManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse corpus manifest: %v", err)
	}
	return m
}

// repoRoot 从测试工作目录（包目录）上溯到含 go.mod 的仓库根，并返回其绝对路径。
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Abs(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

func resolveCorpusPath(t *testing.T, m corpusManifest, e corpusEntry) (string, bool) {
	t.Helper()
	if e.Path != "" {
		root, err := repoRoot()
		if err != nil {
			t.Fatalf("repo root: %v", err)
		}
		return filepath.Join(root, e.Path), true
	}
	root := os.Getenv("GOPPTX_CORPUS")
	if root == "" {
		root = m.ExternalCorpusRoot
	}
	return filepath.Join(root, e.ExternalPath), true
}

func TestCorpusReplayInspect(t *testing.T) {
	manifest := loadCorpusManifest(t)
	r := NewGoPPTXReader(Limits{})
	ctx := context.Background()
	var ran, missing int
	for _, e := range manifest.Entries {
		path, _ := resolveCorpusPath(t, manifest, e)
		if _, err := os.Stat(path); err != nil {
			t.Logf("skip %s: source absent (%v)", e.ID, err)
			missing++
			continue
		}
		ran++
		t.Run(e.ID, func(t *testing.T) {
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer f.Close()
			st, _ := f.Stat()
			doc, err := r.Inspect(ctx, f, st.Size())
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if e.Pages > 0 && len(doc.Pages) != e.Pages {
				t.Fatalf("pages: got %d want %d", len(doc.Pages), e.Pages)
			}
			if len(doc.Pages) == 0 {
				t.Fatalf("no pages extracted")
			}
			notesCount := 0
			for _, pg := range doc.Pages {
				if pg.NotesText != "" {
					notesCount++
				}
			}
			if e.WithNotes && notesCount == 0 {
				t.Fatalf("entry expects notes but none extracted")
			}
			if doc.Features.PageCount != len(doc.Pages) {
				t.Fatalf("features.PageCount %d != pages %d", doc.Features.PageCount, len(doc.Pages))
			}
		})
	}
	if ran == 0 {
		t.Fatalf("no corpus sources available (manifest entries absent)")
	}
	if ran < 5 {
		t.Logf("corpus replay: ran=%d missing=%d (below target)", ran, missing)
	}
}
