package api

import (
	"strings"
	"testing"

	"github.com/F31/ppts/internal/narration"
)

// 这些用例不依赖 DB / 网络 / 外部工具，任何机器都能跑。
// 目的是把「入参校验」的语义钉死在测试里：**非法取值一律 400，绝不静默回退**。
// 静默回退是这类接口最容易埋的坑——用户以为走了 AI 生成，实际拿到的是原文。

func TestParseScriptDraftParamsModeAcceptsProtoAndDomainValues(t *testing.T) {
	cases := []struct {
		raw  string
		want narration.ScriptMode
	}{
		{"", narration.ModeOriginal}, // 未指定 = 原文，不调 LLM
		{"SCRIPT_MODE_UNSPECIFIED", narration.ModeOriginal},
		{"SCRIPT_MODE_ORIGINAL", narration.ModeOriginal},
		{"original", narration.ModeOriginal},
		{"SCRIPT_MODE_POLISH", narration.ModePolish},
		{"polish", narration.ModePolish},
		{"SCRIPT_MODE_AI_GENERATED", narration.ModeAIGenerated},
		{"ai_generated", narration.ModeAIGenerated},
		{"  SCRIPT_MODE_POLISH  ", narration.ModePolish}, // 前后空白可容忍
	}
	for _, tc := range cases {
		out, err := parseScriptDraftParams(scriptDraftBody{SlideIDs: []string{"s1"}, Mode: tc.raw}, "")
		if err != nil {
			t.Fatalf("mode=%q: 期望通过，实际报错 %v", tc.raw, err)
		}
		if out.Mode != tc.want {
			t.Errorf("mode=%q: 得到 %q，期望 %q", tc.raw, out.Mode, tc.want)
		}
	}
}

func TestParseScriptDraftParamsRejectsInvalidInput(t *testing.T) {
	base := scriptDraftBody{SlideIDs: []string{"s1"}, Mode: "SCRIPT_MODE_POLISH", SourceMode: "page_content"}

	cases := []struct {
		name   string
		mutate func(*scriptDraftBody)
		hint   string // 期望出现在错误信息里的片段
	}{
		{"未知 mode 不得静默降级为原文", func(b *scriptDraftBody) { b.Mode = "AI_GENERATED" }, "unknown mode"},
		{"proto 前缀写错的 mode", func(b *scriptDraftBody) { b.Mode = "MODE_POLISH" }, "unknown mode"},
		{"未知 sourceMode 不得静默退回页面级来源", func(b *scriptDraftBody) { b.SourceMode = "pageContent" }, "unknown sourceMode"},
		{"旧的 notes 别名已不再是合法来源", func(b *scriptDraftBody) { b.SourceMode = "notes" }, "unknown sourceMode"},
		{"slideIds 为空不得建出 0 页任务", func(b *scriptDraftBody) { b.SlideIDs = nil }, "slideIds must not be empty"},
		{"slideIds 只有空白", func(b *scriptDraftBody) { b.SlideIDs = []string{"  ", ""} }, "slideIds must not be empty"},
		{"targetSeconds 为负", func(b *scriptDraftBody) { b.TargetSeconds = -1 }, "targetSeconds"},
	}
	for _, tc := range cases {
		body := base
		tc.mutate(&body)
		if _, err := parseScriptDraftParams(body, ""); err == nil {
			t.Errorf("%s: 期望报错，实际通过", tc.name)
		} else if !strings.Contains(err.Error(), tc.hint) {
			t.Errorf("%s: 错误信息 %q 未包含 %q", tc.name, err.Error(), tc.hint)
		}
	}
}

func TestParseScriptDraftParamsNormalizesSlideIDs(t *testing.T) {
	body := scriptDraftBody{
		SlideIDs: []string{" s2 ", "", "s1", "s2", "  ", "s1"},
		Mode:     "SCRIPT_MODE_POLISH",
	}
	out, err := parseScriptDraftParams(body, "")
	if err != nil {
		t.Fatalf("期望通过，实际报错 %v", err)
	}
	want := []string{"s2", "s1"} // 去空白、去重，且保持首次出现顺序
	if len(out.SlideIDs) != len(want) {
		t.Fatalf("得到 %v，期望 %v", out.SlideIDs, want)
	}
	for i := range want {
		if out.SlideIDs[i] != want[i] {
			t.Fatalf("得到 %v，期望 %v", out.SlideIDs, want)
		}
	}
}

func TestParseScriptDraftParamsLanguageAndOverwrite(t *testing.T) {
	// body 里的 language 优先于请求头。
	out, err := parseScriptDraftParams(scriptDraftBody{SlideIDs: []string{"s1"}, Language: " en-US "}, "zh-CN")
	if err != nil {
		t.Fatalf("期望通过，实际报错 %v", err)
	}
	if out.Language != "en-US" {
		t.Errorf("language: 得到 %q，期望 en-US", out.Language)
	}
	// 未指定时回落到请求头。
	out, err = parseScriptDraftParams(scriptDraftBody{SlideIDs: []string{"s1"}}, "zh-CN")
	if err != nil {
		t.Fatalf("期望通过，实际报错 %v", err)
	}
	if out.Language != "zh-CN" {
		t.Errorf("language 回落: 得到 %q，期望 zh-CN", out.Language)
	}
	// overwrite 缺省为 true（显式覆盖），显式 false 只填空白页。
	if !out.Overwrite {
		t.Error("overwrite 缺省应为 true")
	}
	no := false
	out, err = parseScriptDraftParams(scriptDraftBody{SlideIDs: []string{"s1"}, Overwrite: &no}, "")
	if err != nil {
		t.Fatalf("期望通过，实际报错 %v", err)
	}
	if out.Overwrite {
		t.Error("显式 overwrite=false 应被尊重")
	}
}

// 来源白名单必须与 app.pgInputForMode 支持的取值一致，否则校验放行一个 worker 不认识的来源。
func TestScriptSourceModesMatchParserWhitelist(t *testing.T) {
	for _, mode := range []string{"", "notes_first", "notes_only", "page_content"} {
		if _, ok := scriptSourceModes[mode]; !ok {
			t.Errorf("白名单缺少 %q", mode)
		}
	}
	if len(scriptSourceModes) != 4 {
		t.Errorf("白名单应为 4 项（含空串），实际 %d 项；新增取值须同步 app.pgInputForMode", len(scriptSourceModes))
	}
}
