package app

import (
	"strings"
	"testing"

	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/validation"
)

// 本文件锁定两件事，都不依赖 PG/外部工具，任何机器可跑：
//  ① 成稿来源优先级（M1）：显式来源永远优先于沿用旧讲稿；
//  ② 提示词与输入来源一致（M3）：输入是页面文字/备注时，不得说成"润色当前讲稿"。
//
// 回归背景（2026-09-22）：一键成稿选「来源=页面内容 + 覆盖全部」时，产出的讲稿是
// 旧讲稿的润色版，页面文字从未进入提示词。原因是 pgTextForMode 的结果被
// `overwrite && rev != nil` 无条件覆盖。前三条用例即该缺陷的守门人。

type testShape struct{ kind, text string }

func testPage(notes string, shapes ...testShape) parsedPage {
	pg := parsedPage{SlideID: "slide-1", NotesText: notes}
	for _, sh := range shapes {
		var item struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		item.ID = "shape-" + sh.kind
		item.Kind = sh.kind
		item.Text = sh.text
		pg.Shapes = append(pg.Shapes, item)
	}
	return pg
}

func TestSelectDraftInputSourcePriority(t *testing.T) {
	page := testPage("备注文字",
		testShape{"title", "页面标题"},
		testShape{"autoshape", "页面正文一"},
		testShape{"autoshape", "页面正文二"},
	)
	const pageAll = "页面标题\n页面正文一\n页面正文二"
	const pageBody = "页面正文一\n页面正文二"
	pageNotesOnly := testPage("只有备注")
	pageNoText := testPage("")

	cases := []struct {
		name       string
		pg         parsedPage
		source     string
		custom     string
		sourceMode string
		mode       narration.ScriptMode
		overwrite  bool
		existing   string
		want       string
		wantKind   draftInputKind
	}{
		{
			// ★ 回归：显式选页面内容 + 覆盖全部 + 已有旧讲稿 → 必须用页面文字
			name: "页面内容优先于沿用旧讲稿", pg: page, sourceMode: "page_content", mode: narration.ModePolish,
			overwrite: true, existing: "旧的讲稿文字", want: pageAll, wantKind: inputPage,
		},
		{
			// ★ 页面级来源（无备注页的「讲稿来源」）同样优先于旧讲稿
			name: "页面级来源优先于沿用旧讲稿", pg: page, source: "body", mode: narration.ModePolish,
			overwrite: true, existing: "旧的讲稿文字", want: pageBody, wantKind: inputPage,
		},
		{
			// ★ 自定义文本来源同样优先于旧讲稿
			name: "自定义文本来源优先于旧讲稿", pg: page, source: "custom", custom: "用户自定义输入",
			mode: narration.ModePolish, overwrite: true, existing: "旧的讲稿文字",
			want: "用户自定义输入", wantKind: inputCustom,
		},
		{
			// 单页「重新生成讲稿」（未指定来源 + overwrite）保持沿用旧稿润色
			name: "未指定来源时沿用旧讲稿", pg: page, mode: narration.ModePolish,
			overwrite: true, existing: "旧的讲稿文字", want: "旧的讲稿文字", wantKind: inputExisting,
		},
		{
			name: "未指定来源且无旧稿用页面文字", pg: page, mode: narration.ModePolish,
			overwrite: true, existing: "", want: pageAll, wantKind: inputPage,
		},
		{
			name: "原文模式未指定来源用备注", pg: page, mode: narration.ModeOriginal,
			overwrite: true, existing: "旧的讲稿文字", want: "备注文字", wantKind: inputNotes,
		},
		{
			name: "原文模式显式仅备注", pg: page, sourceMode: "notes_only", mode: narration.ModeOriginal,
			overwrite: true, existing: "旧的讲稿文字", want: "备注文字", wantKind: inputNotes,
		},
		{
			name: "备注优先且无备注时回退页面文字", pg: pageNotesOnly, sourceMode: "notes_first",
			mode: narration.ModePolish, overwrite: true, existing: "旧的讲稿文字",
			want: "只有备注", wantKind: inputNotes,
		},
		{
			// 来源种类必须跟着回退走：形状文字为空时用的是备注，不能仍标成"页面文字"
			name: "页面内容来源在页面无文字但有备注时用备注", pg: pageNotesOnly,
			sourceMode: "page_content", mode: narration.ModePolish, overwrite: true, existing: "旧稿",
			want: "只有备注", wantKind: inputNotes,
		},
		{
			name: "页面无文字无备注时无素材", pg: pageNoText, sourceMode: "page_content",
			mode: narration.ModePolish, overwrite: true, existing: "旧稿", want: "", wantKind: inputNotes,
		},
		{
			name: "原文模式无备注时无素材", pg: pageNoText, mode: narration.ModeOriginal,
			overwrite: false, existing: "", want: "", wantKind: inputNotes,
		},
		{
			name: "无文字页未指定来源时仍可沿用旧稿", pg: pageNoText, mode: narration.ModePolish,
			overwrite: true, existing: "旧稿", want: "旧稿", wantKind: inputExisting,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := selectDraftInput(tc.pg, tc.source, tc.custom, tc.sourceMode, tc.mode, tc.overwrite, tc.existing)
			if got.Text != tc.want {
				t.Fatalf("selectDraftInput.Text = %q, want %q", got.Text, tc.want)
			}
			if got.Kind != tc.wantKind {
				t.Fatalf("selectDraftInput.Kind = %q, want %q", got.Kind, tc.wantKind)
			}
		})
	}
}

// TestDraftInstructionsDescribeActualInput 锁定「指令必须与输入来源一致」。
// deny 断言针对的正是缺陷的原始形态：给模型喂页面文字，却告诉它"把当前讲稿润色一遍"。
func TestDraftInstructionsDescribeActualInput(t *testing.T) {
	cases := []struct {
		name string
		kind draftInputKind
		mode narration.ScriptMode
		want string // 指令中必须出现
		deny string // 指令中不得出现（空串跳过）
	}{
		{"页面文字·润色不得说成润色讲稿", inputPage, narration.ModePolish, "页面文字", "当前讲稿"},
		{"页面文字·AI 生成不得说成基于当前讲稿", inputPage, narration.ModeAIGenerated, "页面文字", "当前讲稿"},
		{"备注·润色不得说成润色讲稿", inputNotes, narration.ModePolish, "演讲者备注", "当前讲稿"},
		{"备注·AI 生成不得说成基于当前讲稿", inputNotes, narration.ModeAIGenerated, "演讲者备注", "当前讲稿"},
		{"自定义文本·润色不得说成润色讲稿", inputCustom, narration.ModePolish, "用户为本页提供的内容", "当前讲稿"},
		{"自定义文本·AI 生成不得说成基于当前讲稿", inputCustom, narration.ModeAIGenerated, "用户为本页提供的内容", "当前讲稿"},
		// 沿用旧稿必须明说是基于当前讲稿（否则模型会以为是从版面重新抽取）
		{"沿用旧稿·润色必须说明来源是当前讲稿", inputExisting, narration.ModePolish, "当前讲稿", ""},
		{"沿用旧稿·AI 生成必须说明来源是当前讲稿", inputExisting, narration.ModeAIGenerated, "当前讲稿", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := draftInstructions(tc.mode, tc.kind, ScriptDraftSnapshot{})
			if !strings.Contains(got, tc.want) {
				t.Fatalf("instructions = %q，应包含 %q", got, tc.want)
			}
			if tc.deny != "" && strings.Contains(got, tc.deny) {
				t.Fatalf("instructions = %q，不应包含 %q", got, tc.deny)
			}
			// 统一的收尾约束（数字/单位/型号保持 + 只输出正文）任何来源都必须带上。
			if !strings.Contains(got, "只输出正文") {
				t.Fatalf("instructions = %q，缺少收尾约束", got)
			}
		})
	}
}

// TestDraftInstructionsCarryHardConstraints 守住 M10：任何来源、任何模式的指令都必须带上
// 三条硬约束，且**等价写法说明取自校验器的同一张表**。
//
// 为什么必须断言「同源」而不是只断言"指令里有这句话"：如果提示词自己抄一份等价写法说明，
// 两边迟早不一致——模型照着指令写、校验器却判违规，用户只看到整页回退原文，
// 查不出是模型的问题还是提示词的问题（M1/M3 的教训）。
func TestDraftInstructionsCarryHardConstraints(t *testing.T) {
	hint := validation.FormatEquivalenceHint()
	if strings.TrimSpace(hint) == "" {
		t.Fatal("等价写法说明为空：断言会退化为恒真")
	}
	// 量词自 M16 起参与判违规，说明也必须同源传入（否则模型把"3 页"写成"3 个项目"
	// 会整页回退原文，用户查不出原因）。
	measureHint := validation.FormatMeasureHint()
	if strings.TrimSpace(measureHint) == "" {
		t.Fatal("量词说明为空：断言会退化为恒真")
	}
	modes := []narration.ScriptMode{narration.ModePolish, narration.ModeAIGenerated}
	kinds := []draftInputKind{inputPage, inputNotes, inputCustom, inputExisting}
	for _, mode := range modes {
		for _, kind := range kinds {
			got := draftInstructions(mode, kind, ScriptDraftSnapshot{})
			if !strings.Contains(got, hint) {
				t.Errorf("mode=%s kind=%s：指令未带上校验器的等价写法说明%q", mode, kind, hint)
			}
			if !strings.Contains(got, measureHint) {
				t.Errorf("mode=%s kind=%s：指令未带上校验器的量词说明%q", mode, kind, measureHint)
			}
			// 与校验器守门用例（序号不豁免）一一对应：原文没有的数字不得自行新增。
			if !strings.Contains(got, "不得新增") {
				t.Errorf("mode=%s kind=%s：指令未禁止新增原文没有的数字", mode, kind)
			}
			if !strings.Contains(got, "型号与缩写保持原样") {
				t.Errorf("mode=%s kind=%s：指令未要求型号/缩写保持原样", mode, kind)
			}
			if !strings.Contains(got, "逐一保留") {
				t.Errorf("mode=%s kind=%s：指令未要求逐字保留原文实体", mode, kind)
			}
		}
	}
}

// TestDraftInstructionsKeepAudienceStyleLength 保证 M10 的改写没有丢掉既有的受众/风格/时长。
func TestDraftInstructionsKeepAudienceStyleLength(t *testing.T) {
	snap := ScriptDraftSnapshot{Audience: "技术决策者", Style: "简洁", TargetSeconds: 90}
	got := draftInstructions(narration.ModePolish, inputPage, snap)
	for _, want := range []string{"技术决策者", "简洁", "90 秒"} {
		if !strings.Contains(got, want) {
			t.Errorf("指令丢失 %q：%q", want, got)
		}
	}
}

// TestPageStepStateIncludesDegraded 守住"降级不得记为成功"（M9）：
// 数字/单位/型号校验两轮不过时 draftText 会把**原始素材**当稿落库（draftDegraded）。
// 若它被映射成 StepSuccess，界面就会把"页面要点片段"报告成"成稿完成"（A26：不得假成功）。
func TestPageStepStateIncludesDegraded(t *testing.T) {
	cases := []struct {
		outcome draftOutcome
		want    pipeline.JobStepState
		why     string
	}{
		{draftGenerated, pipeline.StepSuccess, "正常生成"},
		{draftDegraded, pipeline.StepDegraded, "回退原文必须单列 degraded"},
		{draftSkippedExisting, pipeline.StepSkipped, "已有讲稿且未选覆盖"},
		{draftSkippedNoText, pipeline.StepSkipped, "无可用素材"},
	}
	for _, tc := range cases {
		got := pageStepState(tc.outcome)
		if got != tc.want {
			t.Errorf("pageStepState(%q) = %q, want %q（%s）", tc.outcome, got, tc.want, tc.why)
		}
	}
	if got := pageStepState(draftDegraded); got == pipeline.StepSuccess {
		t.Fatalf("draftDegraded 被映射为 %q：界面会把原始素材当成讲解稿报告", got)
	}
	// 步骤状态字面量必须与迁移的 CHECK 约束一致（PG 0039 / sqlite 0007），
	// 否则 MarkStep 写库会直接被约束拒绝。
	if string(pipeline.StepDegraded) != "degraded" {
		t.Fatalf("StepDegraded = %q，但迁移的 CHECK 约束只允许 'degraded'", pipeline.StepDegraded)
	}
}
