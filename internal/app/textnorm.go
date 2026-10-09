package app

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	newtn "github.com/F31/textnorm"

	"github.com/F31/ppts/internal/contextrule"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/pronunciation"
)

const (
	textNormProductionProfileName    = "ppts.v3.1"
	textNormProductionProfileVersion = 2
)

// NarrationTextNorm is the component-specific builder used by the adapter/cache.
type NarrationTextNorm interface {
	Build(context.Context, string, string) (*newtn.Engine, error)
}

type textNormPreparationFactory struct{ builder NarrationTextNorm }
type textNormPreparer struct{ engine *newtn.Engine }

// AdaptTextNorm confines textnorm's API to the integration boundary.
func AdaptTextNorm(builder NarrationTextNorm) TextPreparationFactory {
	if builder == nil {
		return nil
	}
	return textNormPreparationFactory{builder: builder}
}

func (f textNormPreparationFactory) Resolve(ctx context.Context, tenant, lang string) (TextPreparer, error) {
	e, err := f.builder.Build(ctx, tenant, lang)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, fmt.Errorf("textnorm: builder returned nil engine")
	}
	return textNormPreparer{engine: e}, nil
}

func (p textNormPreparer) Prepare(ctx context.Context, text, lang string) (PreparedText, error) {
	r, err := p.engine.Normalize(ctx, newtn.Request{Text: text, Lang: lang, LegacyMarkup: true})
	if err != nil {
		return PreparedText{}, err
	}
	var mapping []int
	if len(r.SourceMap.Spans) > 0 {
		mapping = newtn.DisplayToEffective(text, r)
	}
	return PreparedText{Text: r.Text, Pauses: ToControlsPauses(r.Controls), DisplayToSpoken: mapping}, nil
}

func (p textNormPreparer) Display(text string) string {
	display, _ := newtn.StripLegacyMarkers(text)
	return display
}

// WithTextNorm preserves existing wiring while adapting it to the application contract.
func (h *NarrationHandler) WithTextNorm(builder NarrationTextNorm) *NarrationHandler {
	return h.WithTextPreparation(AdaptTextNorm(builder))
}

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

// TextNormContextRuleStore 是上下文替换规则所需的存储窄能力（M5，V3.0 §3.3 数据驱动）。
// 由 contextrule.Store（PGStore/SQLiteStore）实现。
type TextNormContextRuleStore interface {
	LoadAllEffective(ctx context.Context, tenantID string) ([]*contextrule.Record, error)
}

// TextNormOptions 是文本规范化引擎的可选行为。
type TextNormOptions struct {
	// NumberMode 非 nil 时注册数值读法规则（V2.8 §3.4 优先级 20，N2 可选，默认不注册）。
	// 数值展开会改 effectiveText（→ 音频缓存键）与显示字数（→ 触发 IndexMap 精确高亮）。
	NumberMode *newtn.NumberMode
	// Classes 开启 PPT 高频声明式类别规则（V3.0 §2.2，默认空=仅标记+词典，零回退）。
	// 取值：percent,decimal,date,money,unit,fraction,time,ordinal,english_money。
	// fraction/time 需要显式语义提示；日期、金额等整体候选先于数字子候选。
	Classes []string
	// ContextRules 非 nil 时加载上下文替换规则（M5 数据驱动）：租户+平台生效行按
	// priority 注册为 ReplacementPattern。默认 nil=不加载（零回退）。
	// 规则优先级与代码规则共享表（词典 10、类别 11-15、number 20），DB 行 priority 冲突时
	// 与代码规则按注册顺序稳定排序（同 priority 下先注册先执行）。
	ContextRules TextNormContextRuleStore
}

// classPatterns 映射类别名 → 声明式 Pattern 构造器（V3.0 §2.2）。
// 独立开关：装配方按 Classes 列表选择性注册，默认不注册（零回退）。
var classRules = map[string]func() newtn.CandidateRule{
	"percent":       newtn.ZHPercentRule,
	"decimal":       newtn.ZHDecimalRule,
	"date":          func() newtn.CandidateRule { return newtn.ZHDateRule(newtn.ModeConvert) },
	"money":         newtn.ZHMoneyRule,
	"unit":          newtn.ZHUnitRule,
	"fraction":      newtn.ZHFractionRule,
	"time":          newtn.ZHTimeRule,
	"ordinal":       newtn.ZHOrdinalRule,
	"english_money": newtn.ZHEnglishMoneyRule,
}

// loadMergedDictRules 加载并合并租户/平台词典，应用 R1 语义（错误可观测，不静默降空）。
// 供模块引擎构建使用。
func loadMergedDictRules(ctx context.Context, store TextNormDictStore, tenantID string, expectedSeedNonEmpty bool, metrics TextNormMetrics, logger *slog.Logger) (pronunciation.Rules, error) {
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
	return merged, nil
}

// NewTextNormEngine 按租户+语言构建冻结的模块文本规范化引擎。
//
// 语义（V2.8 §5.2 + §6，合并归属已拍板：只在 DictAdapter，不改 Store）：
//   - 合并租户自定义规则（LoadTenantDefault，优先）与平台种子（LoadPlatformDefault，兜底）；
//   - 租户加载错误 → 返回错误（任务失败，不静默降空）；
//   - 平台种子加载错误 → Warn + 跳过（共享基础设施故障不阻塞单租户配音）；
//   - expectedSeedNonEmpty=true 表示平台种子本应非空：若合并结果却为空 → R1 异常空信号；
//   - 正常空（租户与平台均无规则）→ 返回不含词典规则的引擎（纯标记扫描），不报错。
//
// 返回的 Engine 已由 Compile 冻结，可安全并发 Normalize。
func NewTextNormEngine(ctx context.Context, store TextNormDictStore, tenantID, lang string, expectedSeedNonEmpty bool, metrics TextNormMetrics, logger *slog.Logger, opts ...TextNormOptions) (*newtn.Engine, error) {
	merged, err := loadMergedDictRules(ctx, store, tenantID, expectedSeedNonEmpty, metrics, logger)
	if err != nil {
		return nil, err
	}
	var rules []newtn.CandidateRule
	if len(merged) > 0 {
		entries := make([]newtn.LiteralEntry, 0, len(merged))
		for _, rule := range merged {
			if rule.Enabled && rule.Pattern != "" {
				entries = append(entries, newtn.LiteralEntry{Match: rule.Pattern, Replace: rule.Replacement, Whole: false})
			}
		}
		if len(entries) > 0 {
			// Explicit tenant/platform pronunciations are definition-authoritative and
			// must beat automatic technical-model preservation (priority 5).
			rules = append(rules, newtn.LiteralDictRule("definition", 3, entries))
		}
	}
	var opt TextNormOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	// 语言按需装配：en* → 英文规则集（en.conservative 基础 + 词典 + 上下文规则）；
	// 其余 → 中文规则集（类别 + 数值 + S4 + 词典 + 上下文规则）。中英文规则互不混装，
	// 由调用方接口（Resolve/Builder 的 lang 参数）在运行时按需选择。
	if isEnglishLang(lang) {
		enBase, err := newtn.ProfileRules(newtn.ProfileEnglishConservative)
		if err != nil {
			return nil, fmt.Errorf("textnorm: load english base rules: %w", err)
		}
		rules = append(rules, enBase...)
	} else {
		// 类别规则（V3.0 §2.2）：按开关注册，全部先于 NumberRule 执行（优先级 11-15 < 20），
		// 避免数字规则先吞日期/百分比/金额中的数字而破坏类别识别。
		for _, c := range opt.Classes {
			if fn, ok := classRules[c]; ok {
				rules = append(rules, fn())
			}
		}
		if opt.NumberMode != nil {
			rules = append(rules, newtn.ZHNumberRule(*opt.NumberMode))
		}
		// S4 规则默认装配：保护技术型号/版本串，电话逐位读，比分仅在 SemanticScore 提示下触发。
		rules = append(rules,
			newtn.TechnicalModelRule(),
			newtn.ZHVersionPreserveRule(),
			newtn.ZHTelephoneRule(),
			newtn.ZHScoreRule(),
		)
	}
	// 上下文替换规则（M5 数据驱动）：租户+平台生效行按 priority 注册为 ReplacementPattern。
	// 加载失败 → 返回错误（R1：依赖不可用不退回成功）；非法 regexp 已由 API 层拦截，
	// 但迁移/种子数据不可控，此处同样报错（不静默丢弃）。
	if opt.ContextRules != nil {
		recs, err := opt.ContextRules.LoadAllEffective(ctx, tenantID)
		if err != nil {
			return nil, fmt.Errorf("textnorm: load contextual rules: %w", err)
		}
		for i, rec := range recs {
			re, perr := regexp.Compile(rec.Pattern)
			if perr != nil {
				return nil, fmt.Errorf("textnorm: invalid contextual rule %q: %w", rec.ID, perr)
			}
			rules = append(rules, newtn.RegexRule{ID: fmt.Sprintf("context:%d", i), Category: "context", Priority: rec.Priority, Match: re, Replacement: rec.Replacement})
		}
	}
	return newtn.Compile(newtn.Config{Rules: rules, Profile: textNormProductionProfileName, ProfileVersion: textNormProductionProfileVersion})
}

// mergeDictRules 合并租户与平台规则：租户规则存在则整体采用租户（缺租户自定义时回退平台种子）。
func mergeDictRules(tenant, platform pronunciation.Rules) pronunciation.Rules {
	if len(tenant) > 0 {
		return tenant
	}
	return platform
}

// ToControlsPauses 把候选引擎（模块 textnorm）的 Control 事件映射为供应商结构化停顿。
// 与 ToSpeechControlPauses 语义一致；主引擎切换后 narration 消费 Controls 而非旧 Pauses。
func ToControlsPauses(controls []newtn.Control) []tts.Pause {
	out := make([]tts.Pause, 0, len(controls))
	for _, c := range controls {
		out = append(out, tts.Pause{AfterChars: c.AfterRunes, DurationMS: c.DurationMS})
	}
	return out
}

// TextNormCacheEvent 上报引擎缓存事件的可选钩子（与 TextNormMetrics 同风格）。
// name 取 "cache_hit" / "cache_miss"。
type TextNormCacheEvent func(name string)

// cachedTextNorm 包装 NarrationTextNorm：按 (租户, 语言, 配置指纹) 缓存冻结引擎。
//
// 背景（架构优化）：NarrationTextNorm.Build 每次任务都重新查库加载词典/上下文规则、
// 全量 regexp.Compile 并 Compile 引擎。引擎构造后不可变、可并发 Normalize（模块契约），
// 同一租户连续多任务时纯属重复劳动。
//
// 语义：
//   - 键 = tenantID + lang + 配置指纹（env 派生选项变化 → 指纹变化 → 自动失效）；
//   - TTL 覆盖词典/上下文规则的数据变更（库存版无推送机制，采用时间兜底）；
//   - 每键独立构建等待：并发同键任务不重复构建，且等待支持 context 取消；
//   - 超 maxEntries 时逐出最旧条目，防内存无界增长。
type cachedTextNorm struct {
	inner NarrationTextNorm
	// fingerprint 返回当前配置指纹；nil 时视为空串（配置假设静态）。
	fingerprint func() string
	// onEvent 上报 cache_hit/cache_miss（可为 nil）。
	onEvent TextNormCacheEvent
	ttl     time.Duration
	max     int

	mu      sync.Mutex
	entries map[string]engineCacheEntry
	// order 维护插入序（FIFO 逐出最旧）。
	order []string
	// flights 只保存正在构建的键，完成后删除；等待可被 context 取消。
	flights map[string]chan struct{}
}

type engineCacheEntry struct {
	engine  *newtn.Engine
	builtAt time.Time
}

// NewCachedTextNorm 返回包装后的 NarrationTextNorm。
// ttl<=0 或 max<=0 时禁用缓存，保持逐任务加载语义。
func NewCachedTextNorm(inner NarrationTextNorm, ttl time.Duration, max int, fingerprint func() string, onEvent TextNormCacheEvent) NarrationTextNorm {
	if inner == nil {
		panic("textnorm: cachedTextNorm requires non-nil inner builder")
	}
	if ttl <= 0 || max <= 0 {
		return inner
	}
	return &cachedTextNorm{
		inner:       inner,
		fingerprint: fingerprint,
		onEvent:     onEvent,
		ttl:         ttl,
		max:         max,
		entries:     make(map[string]engineCacheEntry),
		flights:     make(map[string]chan struct{}),
	}
}

func (c *cachedTextNorm) cacheKey(tenantID, lang string) string {
	var fp string
	if c.fingerprint != nil {
		fp = c.fingerprint()
	}
	return tenantID + "\x00" + lang + "\x00" + fp
}

// Build 实现 NarrationTextNorm：缓存命中返回冻结引擎；未命中或过期才调底层构建。
func (c *cachedTextNorm) Build(ctx context.Context, tenantID, lang string) (*newtn.Engine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := c.cacheKey(tenantID, lang)

	if e, ok := c.lookup(key); ok {
		if c.onEvent != nil {
			c.onEvent("cache_hit")
		}
		return e, nil
	}
	if c.onEvent != nil {
		c.onEvent("cache_miss")
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		if e, ok := c.entries[key]; ok && time.Since(e.builtAt) < c.ttl {
			c.mu.Unlock()
			return e.engine, nil
		}
		if done, ok := c.flights[key]; ok {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-done:
				continue
			}
		}
		c.flights[key] = make(chan struct{})
		c.mu.Unlock()
		break
	}
	engine, err := c.inner.Build(ctx, tenantID, lang)
	if err == nil && engine == nil {
		err = fmt.Errorf("textnorm: builder returned nil engine")
	}
	if err == nil {
		c.store(key, engine)
	}
	c.mu.Lock()
	close(c.flights[key])
	delete(c.flights, key)
	c.mu.Unlock()
	return engine, err
}

func (c *cachedTextNorm) lookup(key string) (*newtn.Engine, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if c.ttl > 0 && time.Since(e.builtAt) > c.ttl {
		c.deleteLocked(key)
		return nil, false
	}
	return e.engine, true
}

func (c *cachedTextNorm) store(key string, engine *newtn.Engine) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; exists {
		// 已存在（极端重入）则刷新时间与引擎即可。
		c.entries[key] = engineCacheEntry{engine: engine, builtAt: time.Now()}
		return
	}
	c.entries[key] = engineCacheEntry{engine: engine, builtAt: time.Now()}
	c.order = append(c.order, key)
	for c.max > 0 && len(c.entries) > c.max {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

func (c *cachedTextNorm) deleteLocked(key string) {
	delete(c.entries, key)
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

// isEnglishLang 判断语言标识是否为英文（en / en-US / en-GB 等）。
// 用于 NewTextNormEngine 的语言按需装配分支：en* → 英文规则集，其余 → 中文规则集。
func isEnglishLang(lang string) bool {
	l := strings.ToLower(strings.TrimSpace(lang))
	return l == "en" || strings.HasPrefix(l, "en-")
}
