// Package replay 提供「校验规则回放」：把 (原文, 成稿) 对固化成语料，**只跑校验器、
// 不调用模型**，让 `internal/validation` 的规则改动在 CI 里可逐次比较。
//
// 动机（2026-09-22）：M11 把归一化规则改成表驱动后，效果是靠一个**手写的 10 例临时探针**
// 估出来的（"符合预期 2/10 → 9/10"）。手写探针有两个硬伤：样本小，且例子由作者挑选
// （偏向"我认为该通过"）。规则一旦被后人改动，也没有任何东西会拦住回归。
// 本包把这件事变成可提交、可 review、可 gate 的语料：
//
//	provenance=generated  从真实语料页文字机械生成（等价改写 / 数字篡改 / 量词替换），
//	                      带 want 期望，用于统计误杀与漏放。
//	provenance=authored   人工编写的政策用例（如"量词族不同必须拦"）。
//	provenance=recorded   真实模型输出的**判定基线快照**（不带 want）——不知道对错，
//	                      只保证规则改动不会悄悄改变对既有样本的判定。
//
// 两条门禁（见 Check）：
//
//	① **漏放必须为 0**：want=block 却放行 = 门禁真的漏了，属安全问题，不是统计问题。
//	② **相对基线不得翻转**：任何用例判定改变都要显式审阅并更新基线（并写明原因），
//	   避免"改一条等价对，顺手放行了一类真改动"这种看不见的回归。
package replay

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/F31/ppts/internal/validation"
)

// Want 是用例的期望判定。
const (
	WantPass  = "pass"  // 应当放行
	WantBlock = "block" // 应当拦截
)

// 用例来源，用于在报告里分开口径（机制用例 ≠ 模型行为用例）。
const (
	ProvenanceGenerated = "generated"
	ProvenanceAuthored  = "authored"
	ProvenanceRecorded  = "recorded"
)

// Case 是一条回放用例。Want 为空表示"只比基线、不判对错"（真实模型输出用）。
// Tag 是用例所属的**规则族**（date/unit/thousands/fullwidth/number-changed/…），
// 供测试断言"语料对这些族的覆盖没有悄悄消失"——尺子本身也要被校准。
type Case struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Target     string `json:"target"`
	Want       string `json:"want,omitempty"`
	Tag        string `json:"tag,omitempty"`
	Provenance string `json:"provenance,omitempty"`
	Note       string `json:"note,omitempty"`
}

// Verdict 是校验器给出的实际判定。
type Verdict string

const (
	VerdictPass  Verdict = "pass"
	VerdictBlock Verdict = "block"
)

// 差异种类。
const (
	GapFalseBlock = "false_block" // 误杀：应放行却拦截
	GapFalsePass  = "false_pass"  // 漏放：应拦截却放行
)

// Outcome 是单例的判定结果。
type Outcome struct {
	Case   Case
	Actual Verdict
	Gap    string // "" | GapFalseBlock | GapFalsePass
}

// Mismatch 表示该例的期望与实际不符。
func (o Outcome) Mismatch() bool { return o.Gap != "" }

// VerdictOf 跑一次校验器，得到实际判定（只做纯函数调用，无 IO）。
func VerdictOf(source, target string) Verdict {
	if validation.CheckPreserved(source, target).OK() {
		return VerdictPass
	}
	return VerdictBlock
}

// Classify 计算单例结果；Want 为空时只给实际判定、不判对错。
func Classify(c Case) Outcome {
	out := Outcome{Case: c, Actual: VerdictOf(c.Source, c.Target)}
	switch c.Want {
	case WantPass:
		if out.Actual == VerdictBlock {
			out.Gap = GapFalseBlock
		}
	case WantBlock:
		if out.Actual == VerdictPass {
			out.Gap = GapFalsePass
		}
	}
	return out
}

// Summary 是整批语料的汇总。
type Summary struct {
	Total      int
	Passed     int
	Blocked    int
	FalseBlock int
	FalsePass  int
	Gaps       []Outcome // 仅不匹配项，便于报告
	ByProv     map[string]int
}

// Run 跑完整批语料。
func Run(cases []Case) Summary {
	sum := Summary{ByProv: map[string]int{}}
	for _, c := range cases {
		out := Classify(c)
		sum.Total++
		if out.Actual == VerdictPass {
			sum.Passed++
		} else {
			sum.Blocked++
		}
		if c.Provenance != "" {
			sum.ByProv[c.Provenance]++
		}
		switch out.Gap {
		case GapFalseBlock:
			sum.FalseBlock++
			sum.Gaps = append(sum.Gaps, out)
		case GapFalsePass:
			sum.FalsePass++
			sum.Gaps = append(sum.Gaps, out)
		}
	}
	sort.SliceStable(sum.Gaps, func(i, j int) bool { return sum.Gaps[i].Case.ID < sum.Gaps[j].Case.ID })
	return sum
}

// Load 读取 dir 下全部 *.json（每个文件是一个 Case 数组），按 ID 排序。
// 重复 ID 直接报错——语料是门禁的依据，静默覆盖会让某个用例悄悄消失。
func Load(dir string) ([]Case, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []Case
	seen := map[string]string{}
	for _, p := range paths {
		base := filepath.Base(p)
		if base == "baseline.json" {
			continue // 基线不是用例
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var batch []Case
		if err := json.Unmarshal(data, &batch); err != nil {
			return nil, fmt.Errorf("%s: %w", base, err)
		}
		for _, c := range batch {
			if c.ID == "" {
				return nil, fmt.Errorf("%s: 用例缺少 id", base)
			}
			if c.Want != "" && c.Want != WantPass && c.Want != WantBlock {
				return nil, fmt.Errorf("%s: %s 的 want=%q 非法（只能 pass/block 或留空）", base, c.ID, c.Want)
			}
			if prev, dup := seen[c.ID]; dup {
				return nil, fmt.Errorf("用例 id 重复：%s（已出现在 %s）", c.ID, prev)
			}
			seen[c.ID] = base
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// WriteFile 写出用例（生成器与录制通道共用，避免 schema 漂移）。
func WriteFile(path string, cases []Case) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cases, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Baseline 是上次审阅过的逐例判定快照。
type Baseline struct {
	Note     string            `json:"note,omitempty"`
	Verdicts map[string]string `json:"verdicts"`
}

// Flip 表示某用例相对基线发生了翻转。
type Flip struct {
	ID       string
	Was, Now Verdict
}

// LoadBaseline 读取基线；文件不存在返回空基线（首次运行时用）。
func LoadBaseline(path string) (Baseline, error) {
	b := Baseline{Verdicts: map[string]string{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return Baseline{}, fmt.Errorf("baseline: %w", err)
	}
	if b.Verdicts == nil {
		b.Verdicts = map[string]string{}
	}
	return b, nil
}

// Snapshot 由用例的实际判定构建基线。
func Snapshot(cases []Case, note string) Baseline {
	b := Baseline{Note: note, Verdicts: make(map[string]string, len(cases))}
	for _, c := range cases {
		b.Verdicts[c.ID] = string(VerdictOf(c.Source, c.Target))
	}
	return b
}

// CompareBaseline 返回与基线不一致的用例。不在基线里的新用例**不算翻转**
// （新用例应在同一次改动里被审阅；漏放门禁会兜住新增的严重错误）。
func CompareBaseline(cases []Case, base Baseline) []Flip {
	var flips []Flip
	for _, c := range cases {
		was, ok := base.Verdicts[c.ID]
		if !ok {
			continue
		}
		now := string(VerdictOf(c.Source, c.Target))
		if was != now {
			flips = append(flips, Flip{ID: c.ID, Was: Verdict(was), Now: Verdict(now)})
		}
	}
	return flips
}

// MissingFromBaseline 返回尚未登记进基线的用例 ID。新增用例必须与基线在同一次改动里
// 被审阅——否则"新加一条永远失败的用例"或"新加一条把漏洞固化的用例"都能悄悄进仓库。
func MissingFromBaseline(cases []Case, base Baseline) []string {
	var out []string
	for _, c := range cases {
		if _, ok := base.Verdicts[c.ID]; !ok {
			out = append(out, c.ID)
		}
	}
	return out
}

// Tags 返回语料里出现过的规则族（去重、排序），供覆盖度断言使用。
func Tags(cases []Case) []string {
	seen := map[string]bool{}
	for _, c := range cases {
		if c.Tag != "" {
			seen[c.Tag] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Report 是给人与 CI 看的文本汇总。
func (s Summary) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "回放：共 %d 例（放行 %d / 拦截 %d）\n", s.Total, s.Passed, s.Blocked)
	if len(s.ByProv) > 0 {
		kinds := make([]string, 0, len(s.ByProv))
		for k := range s.ByProv {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		parts := make([]string, 0, len(kinds))
		for _, k := range kinds {
			parts = append(parts, fmt.Sprintf("%s=%d", k, s.ByProv[k]))
		}
		fmt.Fprintf(&b, "来源：%s\n", strings.Join(parts, " "))
	}
	fmt.Fprintf(&b, "误杀（应放行却拦）：%d    漏放（应拦却放行）：%d\n", s.FalseBlock, s.FalsePass)
	for _, g := range s.Gaps {
		fmt.Fprintf(&b, "  [%s] %s  %s\n", g.Gap, g.Case.ID, g.Case.Note)
	}
	return b.String()
}
