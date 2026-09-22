// replay_eval 离线回放校验规则语料：**只跑校验器、不调用任何模型**，
// 因此可在无 LLM 密钥、无数据库的机器上运行（CI 友好）。
//
// 用法（仓库根执行）：
//
//	go run ./scripts/replay_eval                     # 回放并打印报告；有漏放/翻转则 exit 1
//	go run ./scripts/replay_eval -update-baseline     # 逐条审阅后锁定判定基线
//	go run ./scripts/replay_eval -dir <dir>           # 指定语料目录
//
// 语料三来源（详见 internal/validation/replay 包注释）：
//
//	generated 从真实语料页文字机械生成（等价改写 / 数字篡改）
//	authored  人工编写的域用例（日期、单位、千分位、型号、量词等语料覆盖不到的）
//	recorded  真实模型输出（不带期望，只做判定基线比对）
//
// 报告写入 testdata/validation/replay-report.json。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/F31/ppts/internal/validation/replay"
)

const (
	defaultDir  = "internal/validation/replay/testdata"
	reportPath  = "testdata/validation/replay-report.json"
	baselineWhy = "逐条审阅后的判定基线；判定改变必须显式更新并说明原因"
)

type jsonReport struct {
	GeneratedAt string         `json:"generatedAt"`
	Total       int            `json:"total"`
	Passed      int            `json:"passed"`
	Blocked     int            `json:"blocked"`
	FalseBlock  int            `json:"falseBlock"`
	FalsePass   int            `json:"falsePass"`
	ByProv      map[string]int `json:"byProvenance"`
	ByTag       map[string]int `json:"byTag"`
	Gaps        []jsonGap      `json:"gaps,omitempty"`
	Flips       []string       `json:"flips,omitempty"`
}

type jsonGap struct {
	ID     string `json:"id"`
	Gap    string `json:"gap"`
	Note   string `json:"note,omitempty"`
	Source string `json:"source"`
	Target string `json:"target"`
}

func main() {
	dir := flag.String("dir", defaultDir, "语料目录")
	update := flag.Bool("update-baseline", false, "用当前判定覆盖基线（需先逐条审阅）")
	flag.Parse()

	cases, err := replay.Load(*dir)
	if err != nil {
		fatal(err)
	}
	if len(cases) == 0 {
		fatal(fmt.Errorf("语料为空：%s", *dir))
	}
	sum := replay.Run(cases)
	fmt.Print(sum.Report())

	basePath := filepath.Join(*dir, "baseline.json")
	base, err := replay.LoadBaseline(basePath)
	if err != nil {
		fatal(err)
	}
	flips := replay.CompareBaseline(cases, base)
	missing := replay.MissingFromBaseline(cases, base)

	if *update {
		if err := writeBaseline(basePath, replay.Snapshot(cases, baselineWhy)); err != nil {
			fatal(err)
		}
		fmt.Printf("基线已更新：%s（%d 条判定）\n", basePath, len(cases))
		return
	}

	rep := jsonReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Total:       sum.Total,
		Passed:      sum.Passed,
		Blocked:     sum.Blocked,
		FalseBlock:  sum.FalseBlock,
		FalsePass:   sum.FalsePass,
		ByProv:      sum.ByProv,
		ByTag:       tagCounts(cases),
	}
	for _, g := range sum.Gaps {
		rep.Gaps = append(rep.Gaps, jsonGap{
			ID: g.Case.ID, Gap: g.Gap, Note: g.Case.Note,
			Source: g.Case.Source, Target: g.Case.Target,
		})
	}
	for _, f := range flips {
		rep.Flips = append(rep.Flips, string(f.Was)+"→"+string(f.Now)+"  "+f.ID)
	}
	writeJSON(reportPath, rep)

	failed := false
	if sum.FalsePass > 0 {
		fmt.Printf("\n[失败] 漏放 %d 例（期望拦截却放行）——安全门禁失效，修规则而非改语料。\n", sum.FalsePass)
		failed = true
	}
	if len(missing) > 0 {
		fmt.Printf("\n[失败] %d 条用例未登记进基线：%s\n", len(missing), strings.Join(missing, ", "))
		failed = true
	}
	if len(flips) > 0 {
		fmt.Printf("\n[失败] %d 例判定相对基线翻转：\n", len(flips))
		for _, f := range flips {
			fmt.Printf("   %s→%s  %s\n", f.Was, f.Now, f.ID)
		}
		fmt.Println("逐条确认（有意改进？还是回归？）后跑 -update-baseline 并写明原因。")
		failed = true
	}
	if failed {
		os.Exit(1)
	}
	fmt.Printf("\n回放通过（报告：%s）\n", reportPath)
}

func tagCounts(cases []replay.Case) map[string]int {
	out := map[string]int{}
	for _, c := range cases {
		out[c.Tag]++
	}
	return out
}

func writeBaseline(path string, b replay.Baseline) error {
	// 按 ID 排序输出，让 diff 可读（map 的 JSON 键有序，Go 会按 key 排序）。
	keys := make([]string, 0, len(b.Verdicts))
	for k := range b.Verdicts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]string, len(keys))
	for _, k := range keys {
		ordered[k] = b.Verdicts[k]
	}
	b.Verdicts = ordered
	writeJSON(path, b)
	return nil
}

func writeJSON(path string, v any) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fatal(err)
		}
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "replay_eval:", err)
	os.Exit(1)
}
