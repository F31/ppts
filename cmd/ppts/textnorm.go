package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/observability"
	"github.com/F31/ppts/internal/pronunciation"
	"github.com/F31/ppts/internal/textnorm"
)

// textNormEnabled 是 TTS 文本规范化引擎的显式开关（V2.8 §5/§7.4）。
// 默认关闭（零行为回退）；置 true 才启用。启用遵循 A26：非默认能力必须显式声明。
func textNormEnabled() bool {
	switch os.Getenv("PPTS_TEXT_NORM_ENABLED") {
	case "true", "1":
		return true
	default:
		return false
	}
}

// appTextNorm 实现 app.NarrationTextNorm：按 (租户, 语言) 构建冻结引擎。
// 词典合并（租户优先 + 平台种子兜底）与异常空信号在 app.NewTextNormEngine 内完成。
// 上下文替换规则（M5 数据驱动）随引擎总开关启用，加载租户+平台生效行。
type appTextNorm struct {
	store         app.TextNormDictStore
	contextRules  app.TextNormContextRuleStore
	logger        *slog.Logger
}

var _ app.NarrationTextNorm = appTextNorm{}

// Build 实现 NarrationTextNorm：加载词典并构建冻结引擎。
// 平台种子来自迁移固定写入（is_platform_default=true），故 expectedSeedNonEmpty 恒真——
// 合并结果为空触发的 R1 "empty_unexpected" 信号由此生效。
// 数值读法（N2 可选）：PPTS_TEXT_NORM_NUMBERS=quantity|year 时注册，默认不注册。
// PPT 高频类别（V3.0 §2.2）：PPTS_TEXT_NORM_CLASSES=percent,decimal,date,money,unit 时注册，默认不注册。
func (a appTextNorm) Build(ctx context.Context, tenantID, lang string) (*textnorm.Engine, error) {
	opts := []app.TextNormOptions{numberModeOption(), classesOption()}
	if a.contextRules != nil {
		opts = append(opts, app.TextNormOptions{ContextRules: a.contextRules})
	}
	return app.NewTextNormEngine(ctx, a.store, tenantID, lang, true, textNormMetricsAdapter{}, a.logger, opts...)
}

// numberModeOption 依据环境变量返回数值规则装配选项；未设置或非法时返回零值（不注册）。
func numberModeOption() app.TextNormOptions {
	switch os.Getenv("PPTS_TEXT_NORM_NUMBERS") {
	case "quantity":
		return app.TextNormOptions{NumberMode: ptr(textnorm.NumberModeQuantity)}
	case "year":
		return app.TextNormOptions{NumberMode: ptr(textnorm.NumberModeYear)}
	default:
		return app.TextNormOptions{}
	}
}

// classesOption 依据环境变量返回 PPT 高频类别装配选项（V3.0 §2.2 独立开关）。
// 逗号分隔；未设置或空时返回零值（不注册，零回退）。
func classesOption() app.TextNormOptions {
	raw := os.Getenv("PPTS_TEXT_NORM_CLASSES")
	if strings.TrimSpace(raw) == "" {
		return app.TextNormOptions{}
	}
	var classes []string
	for _, c := range strings.Split(raw, ",") {
		c = strings.TrimSpace(c)
		if c != "" {
			classes = append(classes, c)
		}
	}
	return app.TextNormOptions{Classes: classes}
}

// ptr 是泛型取址助手（Go 1.18+）。
func ptr[T any](v T) *T { return &v }

// textNormMetricsAdapter 把 app.TextNormMetrics 事件落到 Prometheus 指标。
type textNormMetricsAdapter struct{}

var _ app.TextNormMetrics = textNormMetricsAdapter{}

// OnDictEvent 上报词典加载事件（load_error / empty_unexpected）。
func (textNormMetricsAdapter) OnDictEvent(_ context.Context, name string) {
	observability.TextNormDictEvent(name)
}

// compile-time assertion：store 满足窄接口（由 compose.go 装配的 pronunciation.Store 提供）。
var _ app.TextNormDictStore = (pronunciation.Store)(nil)
