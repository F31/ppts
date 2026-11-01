//go:build corpus

// 语料回放测试：把 testdata/corpus/manifest.json 登记的样本逐一喂给读适配器，
// 断言可解析、页数与备注符合预期。源文件缺席（如外部金样未检出）则以 Skip 跳过。
package project

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

type corpusManifest struct {
	SchemaVersion      string        `json:"schemaVersion"`
	ExternalCorpusRoot string        `json:"externalCorpusRoot"`
	Entries            []corpusEntry `json:"entries"`
}

// corpusMinEntries 是 V4.0 §15.3「至少 30 份组合语料」的份数门槛。
// 只数本仓库自产的条目：外置金样能否检出取决于开发者机器，
// 拿它凑数会让门槛在一台机器上成立、在 CI 上不成立。
const corpusMinEntries = 30

// sortedFlags 保证失败信息里的键序稳定，便于比对两次运行的差异。
func sortedFlags(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type corpusEntry struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	ExternalPath string `json:"externalPath"`
	Pages        int    `json:"pages"`
	WithNotes    bool   `json:"withNotes"`
	// HiddenPages 是期望的隐藏页数：0 表示"这份样本不含隐藏页"。
	// 显式写出来才能区分"没隐藏页"与"漏读了隐藏页"。
	HiddenPages int `json:"hiddenPages"`
	// Features 是期望在读路径上出现的 featureFlag（table / chart / picture /
	// audio / timing / transition / auto_advance / hidden）。
	//
	// 这一列是语料真正的护栏：只断言"文件能解析"的话，生成器写出十份表格
	// 而读路径全认成 textbox，测试照样绿。登记期望后，"文件里有"与"读出来有"
	// 才被绑在一起。
	Features []string `json:"features"`
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
			if e.HiddenPages > 0 || e.ID == "s123" {
				got := 0
				for _, pg := range doc.Pages {
					if pg.Hidden != nil && *pg.Hidden {
						got++
					}
				}
				if got != e.HiddenPages {
					t.Fatalf("hidden pages: got %d want %d", got, e.HiddenPages)
				}
			}
			if len(e.Features) > 0 {
				seen := map[string]bool{}
				for _, pg := range doc.Pages {
					for _, f := range pg.FeatureFlags {
						seen[f] = true
					}
				}
				for _, want := range e.Features {
					if !seen[want] {
						t.Errorf("featureFlag %q missing; got %v", want, sortedFlags(seen))
					}
				}
			}
			// 自产语料都是合法 PPTX：解析器不该在上面留下任何诊断。
			// 诊断会经导入报告变成"部分页面内容可能不完整"的 warn，
			// 让用户为一份正常文件去排查——所以这里当硬失败，不当日志。
			if e.Path != "" && len(doc.Diagnostics) > 0 {
				t.Fatalf("解析器在合法样本上留下诊断：%v", doc.Diagnostics)
			}
			if doc.Features.PageCount != len(doc.Pages) {
				t.Fatalf("features.PageCount %d != pages %d", doc.Features.PageCount, len(doc.Pages))
			}
		})
	}
	if ran == 0 {
		t.Fatalf("no corpus sources available (manifest entries absent)")
	}
	// 份数门槛：V4.0 §15.3 要求至少 30 份。外置金样可能未检出，
	// 因此只对"本仓库自产"的条目强制；外置缺席单独提示，不掩盖本地不足。
	synthetic := 0
	for _, e := range manifest.Entries {
		if e.Path != "" {
			synthetic++
		}
	}
	if synthetic < corpusMinEntries {
		t.Fatalf("synthetic corpus has %d entries, requires >= %d", synthetic, corpusMinEntries)
	}
	if ran < corpusMinEntries {
		t.Logf("corpus replay: ran=%d missing=%d (external samples absent; synthetic=%d)", ran, missing, synthetic)
	}
}
