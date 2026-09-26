package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/pronunciation"
	"github.com/F31/ppts/internal/textnorm"
)

// TextNormDictStore 是文本规范化词典所需的存储窄能力：租户规则 + 平台种子。
// 由 pronunciation.Store（PGStore/SQLiteStore）实现。
type TextNormDictStore interface {
	LoadTenantDefault(ctx context.Context, tenantID string) (pronunciation.Rules, error)
	LoadPlatformDefault(ctx context.Context) (pronunciation.Rules, error)
}

// TextNormMetrics 是文本规范化词典加载时的可选可观测性钩子
// （R1：异常空/加载错误必须可观测，不能退回静默）。
type TextNormMetrics interface {
	// OnDictEvent 上报词典加载事件；name 取 "load_error" / "empty_unexpected"。
	OnDictEvent(context context.Context, name string)
}

// DictAdapter 实现 textnorm.Dictionary：按 pronunciation.Rules 语义在普通 span 内替换。
// 规则快照在构造时冻结；替换语义与既有 pronunciation.Apply 一致
// （逐条 ReplaceAll、enabled=true 且 pattern 非空），保证黄金对比测试②逐字节可过。
type DictAdapter struct {
	rules pronunciation.Rules
	lang  string
}

// NewDictAdapter 由合并后的规则集构造适配器。
func NewDictAdapter(rules pronunciation.Rules, lang string) *DictAdapter {
	return &DictAdapter{rules: rules, lang: lang}
}

// Substitute 实现 textnorm.Dictionary。
func (a *DictAdapter) Substitute(lang, text string) (bool, string) {
	if a == nil || len(a.rules) == 0 {
		return false, text
	}
	changed := false
	for _, rule := range a.rules {
		if !rule.Enabled || rule.Pattern == "" {
			continue
		}
		if strings.Contains(text, rule.Pattern) {
			text = strings.ReplaceAll(text, rule.Pattern, rule.Replacement)
			changed = true
		}
	}
	return changed, text
}

// NewTextNormEngine 按租户+语言构建冻结的文本规范化引擎。
//
// 语义（V2.8 §5.2 + §6，合并归属已拍板：只在 DictAdapter，不改 Store）：
//   - 合并租户自定义规则（LoadTenantDefault，优先）与平台种子（LoadPlatformDefault，兜底）；
//   - 租户加载错误 → 返回错误（任务失败，不静默降空）；
//   - 平台种子加载错误 → Warn + 跳过（共享基础设施故障不阻塞单租户配音）；
//   - expectedSeedNonEmpty=true 表示平台种子本应非空：若合并结果却为空 → R1 异常空信号；
//   - 正常空（租户与平台均无规则）→ 返回不含词典规则的引擎（纯标记扫描），不报错。
//
// 返回的 Engine 已 Build() 冻结，可安全并发 Run。
func NewTextNormEngine(ctx context.Context, store TextNormDictStore, tenantID, lang string, expectedSeedNonEmpty bool, metrics TextNormMetrics, logger *slog.Logger) (*textnorm.Engine, error) {
	if store == nil {
		return nil, fmt.Errorf("textnorm: nil dictionary store")
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("textnorm: empty tenantID (must come from a trusted job principal)")
	}

	tenantRules, err := store.LoadTenantDefault(ctx, tenantID)
	if err != nil {
		if metrics != nil {
			metrics.OnDictEvent(ctx, "load_error")
		}
		return nil, fmt.Errorf("textnorm: load tenant dictionary: %w", err)
	}

	platformRules, err := store.LoadPlatformDefault(ctx)
	if err != nil {
		if logger != nil {
			logger.Warn("textnorm: load platform default dictionary failed; continuing with tenant rules only",
				"tenantId", tenantID, "error", err)
		}
		platformRules = nil
	}

	merged := mergeDictRules(tenantRules, platformRules)
	if expectedSeedNonEmpty && len(merged) == 0 {
		if metrics != nil {
			metrics.OnDictEvent(ctx, "empty_unexpected")
		}
		if logger != nil {
			logger.Warn("textnorm: platform seed expected non-empty but merged dictionary is empty",
				"tenantId", tenantID)
		}
	}

	engine := textnorm.New()
	if len(merged) > 0 {
		engine.RegisterRule("definition", 10, textnorm.DefinitionRule(NewDictAdapter(merged, lang)))
	}
	engine.Build()
	return engine, nil
}

// mergeDictRules 合并租户与平台规则：租户规则存在则整体采用租户（缺租户自定义时回退平台种子）。
func mergeDictRules(tenant, platform pronunciation.Rules) pronunciation.Rules {
	if len(tenant) > 0 {
		return tenant
	}
	return platform
}

// ToSpeechControlPauses 把 textnorm 停顿事件映射为供应商结构化停顿。
// AfterRunes 以 effectiveText（派生后朗读文本）为基准，与 tts.Pause.AfterChars 语义一致。
func ToSpeechControlPauses(pauses []textnorm.Pause) []tts.Pause {
	out := make([]tts.Pause, 0, len(pauses))
	for _, p := range pauses {
		out = append(out, tts.Pause{AfterChars: p.AfterRunes, DurationMS: p.DurationMS})
	}
	return out
}
