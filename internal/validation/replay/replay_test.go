package replay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// requiredTags 是语料必须覆盖的规则族。语料本身就是门禁的依据：若它悄悄缩水
// （某族用例被删光、或新用例不带 tag），门禁会退化成"永远通过"——所以尺子也要被校准。
var requiredTags = []string{
	"date",
	"unit",
	"thousands",
	"fullwidth",
	"trailing-zero",
	"multiplier-fold",
	"model",
	"number-changed",
	"number-dropped",
	"number-inserted",
}

// corpusDir 定位语料目录（从包目录上溯到含 go.mod 的仓库根）。
func corpusDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "internal", "validation", "replay", "testdata")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("未找到仓库根（go.mod）")
		}
		dir = parent
	}
}

func loadCorpus(t *testing.T) ([]Case, string) {
	t.Helper()
	dir := corpusDir(t)
	cases, err := Load(dir)
	if err != nil {
		t.Fatalf("加载语料: %v", err)
	}
	if len(cases) == 0 {
		t.Fatalf("语料为空：%s 下没有用例", dir)
	}
	return cases, dir
}

// TestReplayCorpus 是校验规则的核心门禁，**不依赖 LLM、不依赖外部工具**，
// 因此可以直接进 CI。
//
// 两条硬门禁：
//  1. 漏放必须为 0 —— 应拦却放行 = 门禁真的漏了（安全问题，不是统计问题）。
//  2. 相对基线不得翻转 —— 任何判定改变都必须显式审阅并更新基线；
//     新用例也必须先登记基线，否则"新加一条永远失败的用例"或"新加一条把漏洞固化的用例"
//     都能悄悄进仓库。
func TestReplayCorpus(t *testing.T) {
	cases, dir := loadCorpus(t)
	sum := Run(cases)
	t.Log("\n" + sum.Report())

	if sum.FalsePass > 0 {
		var ids []string
		for _, g := range sum.Gaps {
			if g.Gap == GapFalsePass {
				ids = append(ids, g.Case.ID+"（"+g.Case.Note+"）")
			}
		}
		t.Fatalf("漏放 %d 例：期望拦截却放行了 %s\n这是安全门禁失效，必须修规则而不是改语料。",
			sum.FalsePass, strings.Join(ids, "; "))
	}

	base, err := LoadBaseline(filepath.Join(dir, "baseline.json"))
	if err != nil {
		t.Fatalf("读基线: %v", err)
	}
	if missing := MissingFromBaseline(cases, base); len(missing) > 0 {
		t.Fatalf("有 %d 条用例未登记进基线（新增用例必须与基线在同一次改动里被审阅）：%s\n"+
			"处理：跑 `go run ./scripts/replay_eval -update-baseline`，逐条确认判定合理后一并提交。",
			len(missing), strings.Join(missing, ", "))
	}
	if flips := CompareBaseline(cases, base); len(flips) > 0 {
		lines := make([]string, 0, len(flips))
		for _, f := range flips {
			lines = append(lines, string(f.Was)+"→"+string(f.Now)+"  "+f.ID)
		}
		t.Fatalf("有 %d 例相对基线翻转：\n  %s\n"+
			"翻转必须逐条解释（是有意改进？还是回归？），确认后跑 "+
			"`go run ./scripts/replay_eval -update-baseline` 更新基线并写明原因。",
			len(flips), strings.Join(lines, "\n  "))
	}
}

// TestCorpusCoverage 保证语料对关键规则族有覆盖，且每条用例都带 tag。
func TestCorpusCoverage(t *testing.T) {
	cases, _ := loadCorpus(t)

	for _, c := range cases {
		if strings.TrimSpace(c.Tag) == "" {
			t.Errorf("用例 %s 缺少 tag：语料覆盖度依赖 tag，缺失会让校准失效", c.ID)
		}
		if c.Want != WantPass && c.Want != WantBlock {
			t.Errorf("用例 %s 的 want=%q 非法", c.ID, c.Want)
		}
	}

	have := map[string]bool{}
	for _, tag := range Tags(cases) {
		have[tag] = true
	}
	var missing []string
	for _, want := range requiredTags {
		if !have[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("语料缺少规则族覆盖：%s\n若确实要下线某族，需在 requiredTags 里显式删除并说明。",
			strings.Join(missing, ", "))
	}

	var pass, block int
	for _, c := range cases {
		switch c.Want {
		case WantPass:
			pass++
		case WantBlock:
			block++
		}
	}
	if pass == 0 || block == 0 {
		t.Fatalf("语料必须同时含放行与拦截用例（pass=%d block=%d）", pass, block)
	}
}
