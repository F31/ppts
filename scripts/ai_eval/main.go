// ai_eval 运行 G2-6 专业语料 AI 评测集：≥100 页语料逐页 polish，
// 用确定性实体校验器检查关键数字/单位/型号/日期是否忠于来源。
// 门禁：任一页出现实体漂移即失败（关键数字全部忠于来源）。
//
// 用法（仓库根执行）：
//
//	PPTS_LLM_PROVIDER=siliconflow PPTS_LLM_API_KEY=... go run ./scripts/ai_eval
//
// 未配置 LLM 供应商时跳过（exit 0），供无密钥环境/CI 占位。
// 报告写入 testdata/ai-eval/report.json。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/F31/ppts/internal/integrations/llm"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/validation"
)

const corpusDir = "testdata/corpus/generated"
const reportDir = "testdata/ai-eval"

type pageResult struct {
	Deck      string   `json:"deck"`
	Index     int      `json:"index"`
	SlideID   string   `json:"slideId"`
	OK        bool     `json:"ok"`
	Missing   []string `json:"missing,omitempty"`
	Inserted  []string `json:"inserted,omitempty"`
	Error     string   `json:"error,omitempty"`
	DurationS float64  `json:"durationSeconds"`
}

type report struct {
	GeneratedAt    string       `json:"generatedAt"`
	Model          string       `json:"model"`
	PagesEvaluated int          `json:"pagesEvaluated"`
	PagesOK        int          `json:"pagesOk"`
	PagesFailed    int          `json:"pagesFailed"`
	PagesErrored   int          `json:"pagesErrored"`
	PassRate       float64      `json:"passRate"`
	P95Seconds     float64      `json:"p95Seconds"`
	Pages          []pageResult `json:"pages"`
}

func main() {
	polisher, err := llm.FromEnv()
	if err != nil {
		fatal(err)
	}
	if polisher == nil {
		fmt.Println("ai_eval: LLM provider not configured; skipped (set PPTS_LLM_PROVIDER=siliconflow + PPTS_LLM_API_KEY to run)")
		return
	}
	decks, err := filepath.Glob(filepath.Join(corpusDir, "*.pptx"))
	if err != nil {
		fatal(err)
	}
	sort.Strings(decks)
	if len(decks) == 0 {
		fatal(errors.New("no corpus decks found; run go run ./scripts/gen_corpus first"))
	}
	reader := project.NewGoPPTXReader(project.Limits{})
	rep := report{GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	ctx := context.Background()

	for _, deckPath := range decks {
		deck := strings.TrimSuffix(filepath.Base(deckPath), ".pptx")
		doc, err := inspect(ctx, reader, deckPath)
		if err != nil {
			fatal(fmt.Errorf("%s: %w", deck, err))
		}
		for _, pg := range doc.Pages {
			source := pageText(pg)
			if strings.TrimSpace(source) == "" {
				continue
			}
			res := evalPage(ctx, polisher, deck, pg, source)
			rep.Pages = append(rep.Pages, res)
			switch {
			case res.Error != "":
				rep.PagesErrored++
			case res.OK:
				rep.PagesOK++
			default:
				rep.PagesFailed++
			}
			rep.PagesEvaluated++
		}
	}
	if rep.PagesEvaluated == 0 {
		fatal(errors.New("no pages evaluated"))
	}
	rep.Model = modelOf()
	rep.PassRate = float64(rep.PagesOK) / float64(rep.PagesEvaluated)
	rep.P95Seconds = p95(rep.Pages)

	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		fatal(err)
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		fatal(err)
	}
	out := filepath.Join(reportDir, "report.json")
	if err := os.WriteFile(out, data, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("pages=%d ok=%d failed=%d errored=%d passRate=%.2f p95=%.2fs\n",
		rep.PagesEvaluated, rep.PagesOK, rep.PagesFailed, rep.PagesErrored, rep.PassRate, rep.P95Seconds)
	fmt.Printf("report written to %s\n", out)
	// G2-6 门禁：关键数字全部忠于来源（任一页实体漂移即失败）。
	if rep.PagesFailed > 0 || rep.PagesErrored > 0 {
		os.Exit(1)
	}
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

func evalPage(ctx context.Context, polisher llm.TextRewriter, deck string, pg *project.Page, source string) pageResult {
	res := pageResult{Deck: deck, Index: pg.Index, SlideID: pg.SlideID}
	started := time.Now()
	defer func() { res.DurationS = time.Since(started).Seconds() }()

	var result llm.RewriteResult
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		result, err = polisher.Rewrite(ctx, llm.RewriteRequest{
			LogicalOpID:  "ai-eval:" + deck + ":" + pg.SlideID,
			Mode:         "polish",
			Language:     "zh-CN",
			SourceText:   source,
			Instructions: "把原文改写为更适合 PPT 演示讲解的自然口播稿；必须逐字保留所有数字、单位、日期、型号，不新增未经原文支持的数字。只输出正文。",
		})
		if err == nil {
			break
		}
		var retryable *llm.RetryableError
		if !errors.As(err, &retryable) {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		res.Error = err.Error()
		return res
	}
	check := validation.CheckPreserved(source, result.Text)
	res.OK = check.OK()
	res.Missing = validation.FormatEntities(check.Missing)
	res.Inserted = validation.FormatEntities(check.Inserted)
	return res
}

// pageText 拼接页面正文；为空时回退备注（与 ScriptDraftHandler 同口径）。
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

func modelOf() string {
	if m := strings.TrimSpace(os.Getenv("PPTS_LLM_MODEL")); m != "" {
		return m
	}
	return "Qwen/Qwen2.5-7B-Instruct"
}

func p95(pages []pageResult) float64 {
	durations := make([]float64, 0, len(pages))
	for _, p := range pages {
		if p.Error == "" {
			durations = append(durations, p.DurationS)
		}
	}
	if len(durations) == 0 {
		return 0
	}
	sort.Float64s(durations)
	idx := int(float64(len(durations)-1) * 0.95)
	return durations[idx]
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ai_eval:", err)
	os.Exit(1)
}
