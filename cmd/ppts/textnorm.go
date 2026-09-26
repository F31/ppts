package main

import (
	"context"
	"log/slog"
	"os"

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
type appTextNorm struct {
	store  app.TextNormDictStore
	logger *slog.Logger
}

var _ app.NarrationTextNorm = appTextNorm{}

// Build 实现 NarrationTextNorm：加载词典并构建冻结引擎。
// 平台种子来自迁移固定写入（is_platform_default=true），故 expectedSeedNonEmpty 恒真——
// 合并结果为空触发的 R1 "empty_unexpected" 信号由此生效。
func (a appTextNorm) Build(ctx context.Context, tenantID, lang string) (*textnorm.Engine, error) {
	return app.NewTextNormEngine(ctx, a.store, tenantID, lang, true, textNormMetricsAdapter{}, a.logger)
}

// textNormMetricsAdapter 把 app.TextNormMetrics 事件落到 Prometheus 指标。
type textNormMetricsAdapter struct{}

var _ app.TextNormMetrics = textNormMetricsAdapter{}

// OnDictEvent 上报词典加载事件（load_error / empty_unexpected）。
func (textNormMetricsAdapter) OnDictEvent(_ context.Context, name string) {
	observability.TextNormDictEvent(name)
}

// compile-time assertion：store 满足窄接口（由 compose.go 装配的 pronunciation.Store 提供）。
var _ app.TextNormDictStore = (pronunciation.Store)(nil)
