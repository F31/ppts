// gen_replay 生成校验回放语料：读 testdata/corpus/manifest.json 登记的语料 deck，
// 逐页抽取页面文字，机械地产出两类用例：
//
//	等价改写（want=pass）  同一事实的合法异写：日期写法、单位中英、千分位、小数尾随零、
//	                      全角、计数前缀 x8 折叠
//	数字篡改（want=block） 真实数字被改大 / 被删换成模糊表述 / 新增原文没有的数字
//
// 为什么要这样生成：M11 的效果是靠一个**手写 10 例临时探针**估出来的，例子由作者挑选
// （偏向"我认为该通过"）。从语料页文字机械生成可以去主观性，且随语料可重跑。
//
// 语料优先级：**外部金样（真实业务文本）在前，本地生成的填充 deck 在后**——
// testdata/corpus/generated 里 100 页文字几乎完全相同（"… page N：PCIe 5.0 x16 / 64 GB"），
// 只能验证读适配器，用于校验规则几乎没有信息量。真实金样在 sibling 仓库，用 -corpus 指定：
//
//	go run ./scripts/gen_replay -corpus E:/projects/go-pptx/testdata/corpus
//
// 输出 internal/validation/replay/testdata/generated.json（**需 review 后提交**——
// 语料里每一条都是对"什么算违规"的一次表态）。外部金样缺席时仍能生成（只少了那几条）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/validation/replay"
)

const (
	manifestPath     = "testdata/corpus/manifest.json"
	outPath          = "internal/validation/replay/testdata/generated.json"
	maxPerFamily     = 12 // 每个用例族最多取多少条（全局），保持语料可 review
	maxPerDeckFamily = 3  // 且每个 deck 在每个族里最多贡献多少条，避免单个 deck 刷满
)

type manifest struct {
	ExternalCorpusRoot string  `json:"externalCorpusRoot"`
	Entries            []entry `json:"entries"`
}

type entry struct {
	ID           string `json:"id"`
	Path         string `json:"path"`
	ExternalPath string `json:"externalPath"`
}

// transform 是一条"等价改写"：把文本里的某种规范写法换成已登记的等价写法。
type transform struct {
	name  string
	apply func(string) string
}

func main() {
	corpusRoot := flag.String("corpus", os.Getenv("GOPPTX_CORPUS"),
		"外部金样根目录（缺省读 GOPPTX_CORPUS）")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fatal(err)
	}
	m := loadManifest(filepath.Join(root, manifestPath))
	extRoot := strings.TrimSpace(*corpusRoot)
	if extRoot == "" {
		extRoot = strings.TrimSpace(m.ExternalCorpusRoot)
	}

	reader := project.NewGoPPTXReader(project.Limits{})
	ctx := context.Background()

	// 真实金样优先：先收 path=="" 的外部条目，再收本地填充 deck。
	var sources []sourceRef
	for _, pass := range []bool{true, false} {
		for _, e := range m.Entries {
			isExternal := e.Path == ""
			if isExternal != pass {
				continue
			}
			path, ok := resolveEntry(root, extRoot, e)
			if !ok {
				continue
			}
			if _, err := os.Stat(path); err != nil {
				fmt.Fprintf(os.Stderr, "gen_replay: 跳过 %s（源缺席：%s）\n", e.ID, path)
				continue
			}
			sources = append(sources, sourceRef{ID: e.ID, Path: path, External: isExternal})
		}
	}
	if len(sources) == 0 {
		fatal(fmt.Errorf("没有可用的语料源：外部金样请用 -corpus 指定根目录"))
	}

	families := []transform{
		{"date-spaced", rewriteDates},
		{"unit-zh", rewriteUnitsToChinese},
		{"no-thousands", rewriteDropThousands},
		{"trailing-zero", rewriteDropTrailingZero},
		{"fullwidth", rewriteFullwidthToHalf},
		{"multiplier-fold", rewriteFoldMultiplier},
	}

	global := map[string]int{}
	byDeck := map[string]int{}
	var cases []replay.Case
	seenPair := map[string]bool{}
	// seenShape 按"文本骨架"（数字全部折成 #）去重等价改写用例：
	// 填充语料里 100 页文字几乎相同，逐个登记只会让语料 diff 全是噪声，
	// 而规则族覆盖不因重复而增加。数字篡改用例不去重（具体数值正是其价值）。
	seenShape := map[string]bool{}

	emit := func(deck, family string, c replay.Case) {
		key := c.Source + "\x00" + c.Target
		if seenPair[key] {
			return
		}
		if global[family] >= maxPerFamily || byDeck[deck+"\x00"+family] >= maxPerDeckFamily {
			return
		}
		if c.Tag == "" {
			c.Tag = family // 规则族即用例族，供覆盖度断言
		}
		seenPair[key] = true
		global[family]++
		byDeck[deck+"\x00"+family]++
		cases = append(cases, c)
	}

	for _, s := range sources {
		doc, err := inspect(ctx, reader, s.Path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gen_replay: 跳过 %s: %v\n", s.ID, err)
			continue
		}
		for _, pg := range doc.Pages {
			src := pageText(pg)
			if strings.TrimSpace(src) == "" {
				continue
			}
			tag := fmt.Sprintf("%s-p%d", s.ID, pg.Index)

			for _, tf := range families {
				dst := tf.apply(src)
				if dst == src {
					continue
				}
				shape := tf.name + "\x00" + shapeOf(dst)
				if seenShape[shape] {
					continue
				}
				seenShape[shape] = true
				emit(s.ID, tf.name, replay.Case{
					ID:         "gen-" + tag + "-" + tf.name,
					Source:     src,
					Target:     dst,
					Want:       replay.WantPass,
					Provenance: replay.ProvenanceGenerated,
					Note:       "等价改写：" + tf.name,
				})
			}

			nums := allNumbers(src)
			if len(nums) == 0 {
				continue
			}
			if dst, ok := changedNumber(src, nums[0], nums); ok {
				emit(s.ID, "number-changed", replay.Case{
					ID:         "mut-" + tag + "-number-changed",
					Source:     src,
					Target:     dst,
					Want:       replay.WantBlock,
					Provenance: replay.ProvenanceGenerated,
					Note:       "数字被改：真实数值换成源文不存在的值",
				})
			}
			if dst, ok := droppedNumber(src, nums[0]); ok {
				emit(s.ID, "number-dropped", replay.Case{
					ID:         "mut-" + tag + "-number-dropped",
					Source:     src,
					Target:     dst,
					Want:       replay.WantBlock,
					Provenance: replay.ProvenanceGenerated,
					Note:       "数字被删：换成模糊表述（源文没有的写法）",
				})
			}
			if dst, ok := insertedNumber(src, nums); ok {
				emit(s.ID, "number-inserted", replay.Case{
					ID:         "mut-" + tag + "-number-inserted",
					Source:     src,
					Target:     dst,
					Want:       replay.WantBlock,
					Provenance: replay.ProvenanceGenerated,
					Note:       "新增数字：原文没有的数字（含自行编号）",
				})
			}
		}
	}

	if len(cases) == 0 {
		fatal(fmt.Errorf("未生成任何用例"))
	}
	sort.SliceStable(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	if err := replay.WriteFile(filepath.Join(root, outPath), cases); err != nil {
		fatal(err)
	}
	fmt.Printf("gen_replay: 写出 %d 条用例 → %s\n", len(cases), outPath)
	for _, k := range sortedKeys(global) {
		fmt.Printf("  %-18s %d\n", k, global[k])
	}
}

type sourceRef struct {
	ID       string
	Path     string
	External bool
}

func resolveEntry(root, extRoot string, e entry) (string, bool) {
	if e.Path != "" {
		return filepath.Join(root, filepath.FromSlash(e.Path)), true
	}
	if e.ExternalPath != "" && extRoot != "" {
		return filepath.Join(extRoot, filepath.FromSlash(e.ExternalPath)), true
	}
	return "", false
}

// ─── 等价改写 ────────────────────────────────────────────────────────────────

var (
	reThousands = regexp.MustCompile(`(\d),(\d{3})`)
	reDate      = regexp.MustCompile(`(\d{4})\s*([-/.])\s*(\d{1,2})\s*[-/.]\s*(\d{1,2})`)
	reUnitEn    = regexp.MustCompile(`(\d)\s*(ms|min|hz|h|s)\b`)
	// RE2 无后顾断言（同 validation 包的注意事项）：用捕获组吃掉后续字符再原样写回。
	reTrailZero = regexp.MustCompile(`(\d)\.0([^0-9]|$)`)
	reMultiFold = regexp.MustCompile(`\bx(\d+)\b`)
	reNumber    = regexp.MustCompile(`\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:\.\d+)?`)
)

// unitZH 只登记校验器**已认可**的等价写法（见 validation 的单位等价表）。
var unitZH = map[string]string{
	"ms":  "毫秒",
	"min": "分钟",
	"h":   "小时",
	"s":   "秒",
	"hz":  "赫兹",
}

func rewriteDates(s string) string {
	return reDate.ReplaceAllString(s, "$1 年 $3 月 $4 日")
}

func rewriteUnitsToChinese(s string) string {
	return reUnitEn.ReplaceAllStringFunc(s, func(m string) string {
		sub := reUnitEn.FindStringSubmatch(m)
		zh, ok := unitZH[strings.ToLower(sub[2])]
		if !ok {
			return m
		}
		return sub[1] + " " + zh
	})
}

func rewriteDropThousands(s string) string {
	return reThousands.ReplaceAllString(s, "$1$2")
}

func rewriteDropTrailingZero(s string) string {
	return reTrailZero.ReplaceAllString(s, "$1$2")
}

func rewriteFullwidthToHalf(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0xFF10 && r <= 0xFF19:
			return r - 0xFF10 + '0'
		case r >= 0xFF21 && r <= 0xFF3A:
			return r - 0xFF21 + 'A'
		case r >= 0xFF41 && r <= 0xFF5A:
			return r - 0xFF41 + 'a'
		}
		return r
	}, s)
}

// rewriteFoldMultiplier 把计数前缀 x16 写成裸数量 16（M11 的对称归一，
// 让「PCIe x16」与「PCIe 16」同键；x86/x64 不同值仍判违规）。
func rewriteFoldMultiplier(s string) string {
	return reMultiFold.ReplaceAllString(s, "$1")
}

// ─── 数字篡改 ────────────────────────────────────────────────────────────────

func allNumbers(s string) []string {
	return reNumber.FindAllString(s, -1)
}

func numberSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, n := range allNumbers(s) {
		out[strings.ReplaceAll(n, ",", "")] = true
	}
	return out
}

// changedNumber 把第一个数字换成源文里不存在的值（避免"换成了另一个本来就有的数字"
// 导致判定歧义）。
func changedNumber(s, first string, all []string) (string, bool) {
	base := strings.ReplaceAll(first, ",", "")
	v, err := strconv.ParseFloat(base, 64)
	if err != nil {
		return "", false
	}
	have := numberSet(s)
	for _, delta := range []float64{7, 13, 23, 41} {
		nv := v + delta
		if nv <= 0 {
			continue
		}
		cand := formatNumber(nv, strings.Contains(base, "."))
		if have[cand] {
			continue
		}
		return replaceFirst(s, first, cand), true
	}
	return "", false
}

// droppedNumber 把第一个数字换成模糊表述（模拟"关键数字被省略"）。
func droppedNumber(s, first string) (string, bool) {
	return replaceFirst(s, first, "若干"), true
}

// insertedNumber 追加一句源文没有的数字（含"自行编号"场景）。
func insertedNumber(s string, all []string) (string, bool) {
	have := numberSet(s)
	for _, cand := range []string{"37", "19", "53"} {
		if have[cand] {
			continue
		}
		return s + "\n本页共 " + cand + " 个要点。", true
	}
	return "", false
}

func replaceFirst(s, old, new string) string {
	i := strings.Index(s, old)
	if i < 0 {
		return s
	}
	return s[:i] + new + s[i+len(old):]
}

// shapeOf 把文本里的数字全部折成 #，得到"文本骨架"，用于等价改写用例去重。
func shapeOf(s string) string {
	return reNumber.ReplaceAllString(s, "#")
}

func formatNumber(v float64, wasFloat bool) string {
	if !wasFloat && v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// ─── 语料读取 ────────────────────────────────────────────────────────────────

func loadManifest(path string) manifest {
	data, err := os.ReadFile(path)
	if err != nil {
		fatal(fmt.Errorf("读语料清单 %s: %w", path, err))
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		fatal(fmt.Errorf("解析语料清单: %w", err))
	}
	return m
}

func inspect(ctx context.Context, reader *project.GoPPTXReader, path string) (*project.Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return reader.Inspect(ctx, f, st.Size())
}

// pageText 与 ScriptDraftHandler / ai_eval 同口径：拼接正文，为空回退备注。
func pageText(pg *project.Page) string {
	var parts []string
	for _, sh := range pg.Shapes {
		if t := strings.TrimSpace(sh.Text); t != "" {
			parts = append(parts, t)
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		text = strings.TrimSpace(pg.NotesText)
	}
	return text
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("未找到 go.mod（请在仓库内执行）")
		}
		dir = parent
	}
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gen_replay:", err)
	os.Exit(1)
}
