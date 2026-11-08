// 讲稿生成：LLM 调用、重试与缓存。
//
// 从 scriptdraft.go 按职责拆出（纯搬移，实现逐字未改）：负责单页成稿的两轮调用、
// 429 退避重试、错误分类、令牌预扣/结算，以及成稿与洞察的对象存储缓存。

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/F31/ppts/internal/integrations/llm"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/usage"
	"github.com/F31/ppts/internal/validation"
)

// draftTextResult 是单页成稿的产出与**降级原因**。
//
// Reason 只在 Degraded 时有值，写入 job_steps.result_ref：用户看到「已保留原文」
// 时必须知道是哪条规则没过（缺了哪个数字、多了哪个数字、还是模型吐了思考过程），
// 否则只能反复重试，无法判断该改页面、该换模型还是该改配置。
type draftTextResult struct {
	Display  string
	Spoken   string
	Degraded bool
	Reason   string
	// Note 是"已放行但请核对"的提示（当前仅按策略放行的概括计数）。它不影响落库，
	// 只随步骤记录带给用户：这类数字是模型自己数出来的，界面不该假装它是原文事实。
	Note string
}

// draftText 保留原签名，供既有调用方与测试使用；需要降级原因时用 draftTextDetail。
func (h *ScriptDraftHandler) draftText(ctx context.Context, tenantID, projectID, slideID, language string, mode narration.ScriptMode, in draftInput, facts []draftFact, snap ScriptDraftSnapshot, polisher llm.TextRewriter) (string, string, bool, error) {
	res, err := h.draftTextDetail(ctx, tenantID, projectID, slideID, language, mode, in, facts, snap, polisher)
	return res.Display, res.Spoken, res.Degraded, err
}
func (h *ScriptDraftHandler) draftTextDetail(ctx context.Context, tenantID, projectID, slideID, language string, mode narration.ScriptMode, in draftInput, facts []draftFact, snap ScriptDraftSnapshot, polisher llm.TextRewriter) (draftTextResult, error) {
	source := in.Text
	if mode == narration.ModeOriginal {
		return draftTextResult{Display: source, Spoken: source}, nil
	}
	if polisher == nil {
		return draftTextResult{}, errors.New("script_draft: LLM draft mode requires configured text rewriter")
	}
	instructions := draftInstructions(mode, in.Kind, snap)
	promptSource, validationSource := augmentDraftSource(source, facts)
	instructions += factCitationInstruction(facts)
	// 内容寻址缓存：相同（模式+语言+指令+素材）直接复用上次产出，避免重复调用模型。
	// 同一份稿子/同一批参数反复生成（补页、重试、改投影前后）时命中率很高。
	var cacheKey objectstore.ObjectKey
	if !h.disableCache {
		cacheKey = h.scriptCacheKey(tenantID, mode, language, instructions, promptSource)
		// 缓存也可能是脏的（历史版本把 number:3 写进过正文，截断成 69 字残稿，
		// 或干脆把页面文字原样存了下来）：命中后仍要过质量判定，
		// 否则一次坏产出会被缓存永久复用。
		if cached, ok := h.loadScriptCache(ctx, cacheKey); ok &&
			!draftOutputSuspect(language, cached) && !draftTooShort(cached, snap.TargetSeconds) &&
			!draftEchoesSource(source, cached) {
			// 三道判定只保证「文本没坏」，不保证「按**当前**规则仍然合规」。缓存 key 不含
			// 规则表版本，而规则会变（中文序数改按序号判定、计数策略收紧）：旧产出可能
			// 已经该被拦，却在缓存路径直接判成功——这是假成功，用户看到的是一份按现行
			// 标准不合格的稿子。此处按现行规则复验，不合格就走下面的重新生成。
			report := validation.CheckPreserved(validationSource, cached)
			_, cited := extractFactCitations(cached)
			if report.OK() && len(unknownFactCitations(cited, facts)) == 0 {
				return draftTextResult{Display: cached, Spoken: cached, Note: toleratedNote(report, cached)}, nil
			}
		}
	}
	opID := tenantID + ":" + projectID + ":" + slideID + ":" + string(mode)
	if err := h.reserveLLMTokens(ctx, tenantID, opID, promptSource); err != nil {
		return draftTextResult{}, err
	}
	result, err := polisher.Rewrite(ctx, llm.RewriteRequest{
		LogicalOpID:  opID,
		Mode:         string(mode),
		Language:     language,
		SourceText:   promptSource,
		Instructions: instructions,
	})
	if err != nil {
		return draftTextResult{}, classifyLLMError(fmt.Errorf("script_draft: generate text: %w", err))
	}
	h.settleLLMTokens(ctx, tenantID, opID, result)
	clean, cited := extractFactCitations(result.Text)
	unknown := unknownFactCitations(cited, facts)
	report := validation.CheckPreserved(validationSource, clean)
	suspectFirst := draftOutputSuspect(language, clean)
	shortFirst := draftTooShort(clean, snap.TargetSeconds)
	echoFirst := draftEchoesSource(source, clean)
	if !suspectFirst && !shortFirst && !echoFirst && report.OK() && len(unknown) == 0 {
		if !h.disableCache {
			h.saveScriptCache(ctx, cacheKey, clean)
		}
		return draftTextResult{Display: clean, Spoken: clean, Note: toleratedNote(report, clean)}, nil
	}
	// 降级原因：两轮各自为什么没过，逐条留痕。
	why := make([]string, 0, 4)
	appendReport := func(tag string, r validation.Report, text string) {
		if ms := validation.FormatEntitiesHuman(r.Missing); len(ms) > 0 {
			why = append(why, tag+"遗漏「"+strings.Join(ms, "、")+"」")
		}
		if ins := validation.FormatEntitiesHuman(r.Inserted); len(ins) > 0 {
			why = append(why, tag+"新增「"+strings.Join(ins, "、")+"」"+numberContextHint(r.Inserted, text))
		}
	}
	if suspectFirst {
		why = append(why, "第一版含内部标记或思考过程")
	}
	if shortFirst {
		why = append(why, fmt.Sprintf("第一版过短（%d 字，目标 %d 秒）",
			utf8.RuneCountInString(clean), snap.TargetSeconds))
	}
	if echoFirst {
		why = append(why, "第一版原样照抄页面文字，没有改写成讲稿")
	}
	appendReport("第一版", report, clean)
	if len(unknown) > 0 {
		why = append(why, "第一版引用了不存在的事实出处")
	}
	// 第二轮是**定点修补**：把上一版文案本身当作待修的稿子，只列出违规项。
	// 原先这里重发原文（SourceText: source），等于让模型从头再写一遍——第一版已经写好的
	// 部分被丢弃，很可能换一种方式再错，两轮下来只剩下回退原文一条路。
	guardOpID := tenantID + ":" + projectID + ":" + slideID + ":" + string(mode) + ":guard"
	guardText := clean
	if suspectFirst || echoFirst {
		// 上一版是思维链/字面量垃圾/照抄页面的产物：在它上面"修补"只会得到更长的垃圾。
		// 回到原始素材重写一次，让第二轮有一次真正的机会。
		guardText = source
	}
	guardInstructionsText := guardInstructions(report)
	if suspectFirst || echoFirst {
		// 此时 report 可能恰好 OK（照抄不会漏实体），用实体修补指令等于让模型继续照抄。
		guardInstructionsText = rewriteGuardInstructions(echoFirst) + entityFixSuffix(report)
	}
	if c := citationGuardInstructions(unknown); c != "" {
		guardInstructionsText += c
	}
	if err := h.reserveLLMTokens(ctx, tenantID, guardOpID, guardText); err != nil {
		return draftTextResult{}, err
	}
	fix, err := polisher.Rewrite(ctx, llm.RewriteRequest{
		LogicalOpID:  guardOpID,
		Mode:         string(mode),
		Language:     language,
		SourceText:   guardText, // ← 待修补的是**上一版产出**（已剥离引用标记），不是原始素材
		Instructions: guardInstructionsText,
	})
	if err == nil {
		h.settleLLMTokens(ctx, tenantID, guardOpID, fix)
		fixedClean, fixedCited := extractFactCitations(fix.Text)
		fixedUnknown := unknownFactCitations(fixedCited, facts)
		fixedReport := validation.CheckPreserved(validationSource, fixedClean)
		suspectSecond := draftOutputSuspect(language, fixedClean)
		shortSecond := draftTooShort(fixedClean, snap.TargetSeconds)
		echoSecond := draftEchoesSource(source, fixedClean)
		if !suspectSecond && !shortSecond && !echoSecond && fixedReport.OK() && len(fixedUnknown) == 0 {
			if !h.disableCache {
				h.saveScriptCache(ctx, cacheKey, fixedClean)
			}
			return draftTextResult{Display: fixedClean, Spoken: fixedClean, Note: toleratedNote(fixedReport, fixedClean)}, nil
		}
		if suspectSecond {
			why = append(why, "修补版含内部标记或思考过程")
		}
		if shortSecond {
			why = append(why, fmt.Sprintf("修补版过短（%d 字，目标 %d 秒）",
				utf8.RuneCountInString(fixedClean), snap.TargetSeconds))
		}
		if echoSecond {
			why = append(why, "修补版仍然照抄页面文字")
		}
		appendReport("修补版", fixedReport, fixedClean)
		if len(fixedUnknown) > 0 {
			why = append(why, "修补版引用了不存在的事实出处")
		}
	} else {
		why = append(why, "修补调用失败")
	}
	// 两轮校验都不过：保守回退为原始素材原文，并以 degraded=true 告知调用方——落库的是
	// 页面要点片段，界面必须显示"已保留原文、待人工确认"，不得报告成稿完成。
	reason := strings.Join(why, "；")
	if reason == "" {
		reason = "数字/单位/型号校验未通过"
	}
	return draftTextResult{Display: source, Spoken: source, Degraded: true, Reason: reason}, nil
}

// scriptDraftCacheAssetType 是讲稿内容缓存的资产类型（租户级共享缓存段）。
const scriptDraftCacheAssetType = "scriptdraft"

// scriptCacheKey 由「租户 + 模式 + 语言 + 指令 + 素材」派生内容地址。
// 指令已编码 audience/style/length/targetSeconds 等参数，故参数变化会自然落到不同缓存。
func (h *ScriptDraftHandler) scriptCacheKey(tenantID string, mode narration.ScriptMode, language, instructions, source string) objectstore.ObjectKey {
	sum := hashBytes([]byte(string(mode) + "\x00" + language + "\x00" + instructions + "\x00" + source))
	return objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: sharedCacheProject, Revision: "cache",
		AssetType: scriptDraftCacheAssetType, AssetID: sum, Ext: "json",
	}
}

type scriptDraftCacheEntry struct {
	Text string `json:"text"`
}

// loadScriptCache 命中返回缓存正文；未命中/读取失败返回 ("", false)（缓存不可用不影响生成）。
func (h *ScriptDraftHandler) loadScriptCache(ctx context.Context, key objectstore.ObjectKey) (string, bool) {
	if h.objects == nil {
		return "", false
	}
	rc, _, err := h.objects.Get(ctx, key)
	if err != nil {
		return "", false
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return "", false
	}
	var entry scriptDraftCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return "", false
	}
	if strings.TrimSpace(entry.Text) == "" {
		return "", false
	}
	return entry.Text, true
}

// saveScriptCache 写入内容缓存；失败静默（缓存只是优化，不影响正确性）。
func (h *ScriptDraftHandler) saveScriptCache(ctx context.Context, key objectstore.ObjectKey, text string) {
	if h.objects == nil || strings.TrimSpace(text) == "" {
		return
	}
	data, err := json.Marshal(scriptDraftCacheEntry{Text: text})
	if err != nil {
		return
	}
	_ = h.objects.Put(ctx, key, bytes.NewReader(data), objectstore.ObjectMeta{
		ContentType: "application/json", ContentHash: hashBytes(data), Size: int64(len(data)),
	})
}

// reserveLLMTokens 预占 LLM token 额度（G2-5）；超出上限时任务失败。
func (h *ScriptDraftHandler) reserveLLMTokens(ctx context.Context, tenantID, opID, source string) error {
	if h.tokens == nil {
		return nil
	}
	if _, err := h.tokens.Reserve(ctx, tenantID, opID, usage.KindLLMTokens, usage.EstimateLLMTokens(utf8.RuneCountInString(source))); err != nil {
		if errors.Is(err, usage.ErrInsufficientQuota) {
			return fmt.Errorf("script_draft: LLM token quota exceeded: %w", err)
		}
		return fmt.Errorf("script_draft: reserve llm tokens: %w", err)
	}
	return nil
}

// settleLLMTokens 以供应商返回的真实 token 数结算（prompt+completion）。
func (h *ScriptDraftHandler) settleLLMTokens(ctx context.Context, tenantID, opID string, result llm.RewriteResult) {
	if h.tokens == nil {
		return
	}
	actual := float64(result.PromptTokens + result.CompletionTokens)
	if err := h.tokens.Settle(ctx, tenantID, opID, usage.KindLLMTokens, actual, ""); err != nil {
		// 记账失败不阻塞已成功的文案生成（额度账本最终一致性由重试/TTL 兜底）。
		_ = err
	}
}

// draftInstructions 生成发给模型的指令。**指令必须描述真实的输入来源**：
// 输入是页面文字/备注（版面要点片段）时，若仍说"把当前讲稿润色一遍"，模型会按
// "改写一份已有成稿"去理解，而它实际拿到的是碎片要点 —— 产出会偏离素材。
// 来源种类由 selectDraftInput 单点决定（本函数不重复做优先级判断）。
func draftInstructions(mode narration.ScriptMode, kind draftInputKind, snap ScriptDraftSnapshot) string {
	parts := []string{}
	if strings.TrimSpace(snap.Audience) != "" {
		parts = append(parts, "目标受众："+strings.TrimSpace(snap.Audience)+"。")
	}
	if strings.TrimSpace(snap.Style) != "" {
		parts = append(parts, "讲解风格："+strings.TrimSpace(snap.Style)+"。")
	}
	if snap.TargetSeconds > 0 {
		parts = append(parts, fmt.Sprintf("控制成适合约 %d 秒口播的长度。", snap.TargetSeconds))
	}
	// 数字/单位/型号/量词的约束**与校验器同源**：等价写法与量词族说明直接取自 validation 包，
	// 避免出现「指令说可以这样写、校验器却拒绝」的假失败——模型照着指令写、仍被判违规，
	// 用户只看到整页回退原文，却查不出是谁的问题（M1/M3 的教训：同一件事两处各写一份必然漂移）。
	// 量词自 M16 起参与判违规（"3 页" ≠ "3 个项目"），因此必须由校验器一并说明。
	suffix := strings.Join(parts, "") +
		"硬约束：① 原文中的数字、单位、日期、型号必须逐一保留，不得改写、替换或省略；" +
		// ② 由校验器按 counts 策略生成：策略放开了概括计数，指令却仍写"不要计数"，
		// 模型只能把「四个用例」拆成别扭的无数字句子（M1/M3：同一件事两处各写一份必然漂移）。
		validation.FormatCountHint() +
		"③ 型号与缩写保持原样，不要擅自改写、展开或省略。" +
		validation.FormatMeasureHint() +
		validation.FormatEquivalenceHint() +
		"只输出正文。"

	// 输入是已有讲稿（未显式指定来源时的沿用/润色路径）。
	if kind == inputExisting {
		if mode == narration.ModeAIGenerated {
			return "基于当前讲稿生成一段更完整、自然、适合客户演示的讲解稿；可以补足衔接和解释，但不得引入当前讲稿没有支持的事实。" + suffix
		}
		return "把当前讲稿润色为更适合 PPT 演示讲解的自然口播稿；" + suffix
	}

	// 输入是原始素材（页面文字 / 备注 / 自定义文本），不是成稿，必须说清是"整理要点"。
	material := "以下是本页 PPT 的页面文字（版面上的要点片段，不是成稿句子）"
	switch kind {
	case inputNotes:
		material = "以下是本页 PPT 的演讲者备注（讲稿要点，不是成稿句子）"
	case inputCustom:
		material = "以下是用户为本页提供的内容（讲稿要点，不是成稿句子）"
	}
	flow := "讲解要先形成清晰主线：开场点明本页要解决的问题或核心结论，中段说明关键关系、因果、对比或取舍，收束到听众应带走的判断；不要按版面顺序逐条复述，也不要输出标题、项目符号或显式结构标签。"
	if mode == narration.ModeAIGenerated {
		return material + "。请据此撰写一段完整、自然、适合客户演示的口播讲解稿：" + flow + "把要点组织成连贯的语句，可以补足衔接与必要解释，但不得引入这些内容没有支持的事实。" + suffix
	}
	return material + "。请把其中的要点整理成一段适合 PPT 演示讲解的自然口播稿：" + flow + "保持原意、要点齐全，不添加这些内容以外的信息。" + suffix
}
func revisionText(rev *narration.Revision) string {
	if rev == nil {
		return ""
	}
	parts := make([]string, 0, len(rev.Segments))
	for _, seg := range rev.Segments {
		if seg == nil {
			continue
		}
		text := strings.TrimSpace(seg.DisplayText)
		if text == "" {
			text = strings.TrimSpace(seg.SpokenText)
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}
func classifyLLMError(err error) error {
	var retryable *llm.RetryableError
	if !errors.As(err, &retryable) {
		return err
	}
	retry := &pipeline.RetryError{Err: err}
	if retryable.RetryAfter > 0 {
		retry.At = time.Now().Add(retryable.RetryAfter)
		return retry
	}
	// 429 速率/配额限流：多数供应商按分钟窗口恢复。带 Retry-After 的按它退避；没有的
	// 用固定退避重试（成稿任务并行度高，单次突发打爆窗口很常见；一击失败会把整单挂掉）。
	var httpErr interface{ HTTPStatus() int }
	if errors.As(retryable.Err, &httpErr) && httpErr.HTTPStatus() == 429 {
		retry.At = time.Now().Add(llm429Backoff)
	}
	return retry
}

// isRateLimitError 判断错误是否为供应商 429（速率/配额限流）。
// 判断走错误链，而不是匹配文案：供应商文案千差万别，状态码是唯一稳定契约。
func isRateLimitError(err error) bool {
	var httpErr interface{ HTTPStatus() int }
	return errors.As(err, &httpErr) && httpErr.HTTPStatus() == 429
}
