// Package usage 负责额度预占、结算、账本与成本记录（V4.0 §4.2/§12.2）。
// 供应商成本与用户计费分开记录；不以供应商回调直接修改余额。
package usage

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Kind 是计量种类（用户计费口径）。首批以生成时长（秒）计量；G2-5 增加 LLM token。
type Kind string

const KindGenSeconds Kind = "gen_seconds"

// KindLLMTokens 计量 LLM 文案生成 token 消耗（prompt+completion，G2-5）。
const KindLLMTokens Kind = "llm_tokens"

// KindAnimationMinutes 计量动画渲染分钟数（M4 §7.5）。单位是**分钟**，与 gen_seconds
// 的秒分开：两个口径混在一个 Kind 里，"还剩多少"就要先知道对方记的是秒还是分钟。
const KindAnimationMinutes Kind = "animation_minutes"

// PeriodSeparator 分隔计量种类与账期（"animation_minutes:2026-10"）。
const PeriodSeparator = ":"

// Period 返回限定到某个账期（YYYY-MM）的计量种类。
//
// 为什么把账期编进 Kind，而不是给额度表加"周期"字段：
//   - 额度预占的原子性依赖 tenant_quotas 上的条件更新（reserved+consumed+Δ <= limit），
//     一旦周期滚动需要先重置 consumed_units，"检查是否跨期"就成了事务外的读改写；
//   - 用账期做 Kind，跨期天然是**新行**：上月行留在原处供对账，本月行从 0 开始，
//     Reserve 的条件更新依旧在单行上原子完成（并发预占不会超卖）。
//
// 代价要写清楚：月度累计要按前缀聚合（见 Base），不能直接 `WHERE usage_kind='...'`。
// 空账期返回原值，便于"不按月"的老口径继续被同一个函数表达。
func (k Kind) Period(period string) Kind {
	if period == "" {
		return k
	}
	return Kind(string(k) + PeriodSeparator + period)
}

// Base 去掉账期后缀，返回基础计量种类。
func (k Kind) Base() Kind {
	s := string(k)
	if i := strings.Index(s, PeriodSeparator); i >= 0 {
		return Kind(s[:i])
	}
	return k
}

// Period 返回账期部分（"2026-10"）；没有账期时返回空串。
func (k Kind) PeriodOf() string {
	s := string(k)
	if i := strings.Index(s, PeriodSeparator); i >= 0 {
		return s[i+len(PeriodSeparator):]
	}
	return ""
}

// MonthPeriod 返回给定时刻所在账期（UTC，YYYY-MM）。
//
// 用 UTC 而不是本地时区：账期边界必须对所有部署实例是同一个瞬间，
// 否则同一份额度在不同时区的节点上会在不同时间"重置"。
func MonthPeriod(t time.Time) string {
	return t.UTC().Format("2006-01")
}

// MonthReset 返回该账期的下一个瞬间（下月 1 日 00:00 UTC），即额度恢复时间。
func MonthReset(period string) (time.Time, error) {
	start, err := time.Parse("2006-01", period)
	if err != nil {
		return time.Time{}, err
	}
	return start.AddDate(0, 1, 0), nil
}

// CharsPerSecond 是中文播报时长估算系数（用于生成前预估；实际以合成时长结算）。
const CharsPerSecond = 5.0

// CharsPerLLMToken 是 LLM token 粗估系数（中文约 1.5 字符/token，仅用于预占；结算以供应商返回为准）。
const CharsPerLLMToken = 1.5

// EstimateSeconds 依据讲稿字数估算播报秒数（仅用于预占，结算以真实时长为准）。
func EstimateSeconds(runeCount int) float64 {
	if runeCount <= 0 {
		return 0
	}
	return float64(runeCount) / CharsPerSecond
}

// EstimateLLMTokens 依据源文本字数粗估单次 LLM 调用 token 消耗（含指令开销）。
func EstimateLLMTokens(runeCount int) float64 {
	if runeCount <= 0 {
		return 0
	}
	return float64(runeCount)/CharsPerLLMToken + 100
}

var (
	// ErrInsufficientQuota 表示预占会导致超出额度上限。
	ErrInsufficientQuota = errors.New("usage: insufficient quota")
	// ErrReservationNotFound 表示逻辑操作没有预占记录。
	ErrReservationNotFound = errors.New("usage: reservation not found")
	// ErrReservationReleased 表示预占已释放，不能再结算。
	ErrReservationReleased = errors.New("usage: reservation already released")
	// ErrReservationSettled 表示预占已结算，不能再释放。
	ErrReservationSettled = errors.New("usage: reservation already settled")
)

// Quota 是某租户某计量种类的额度与累计用量。
type Quota struct {
	TenantID      string
	Kind          Kind
	LimitUnits    float64 // < 0 表示不限量
	ReservedUnits float64
	ConsumedUnits float64
	PriceVersion  string
	UpdatedAt     time.Time
}

// Unlimited 表示未设置上限。
func (q *Quota) Unlimited() bool { return q.LimitUnits < 0 }

// Available 返回剩余可用额度；不限量时返回 -1。
func (q *Quota) Available() float64 {
	if q.Unlimited() {
		return -1
	}
	return q.LimitUnits - q.ReservedUnits - q.ConsumedUnits
}

// Reservation 是一次逻辑操作的额度预占。
type Reservation struct {
	ID                 string
	TenantID           string
	LogicalOperationID string
	Kind               Kind
	ReservedUnits      float64
	State              string // reserved / settled / released
	Created            bool   // 本次调用是否新建预占（幂等重放为 false）
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ProjectUsage 是某项目累计用量与成本（G3-8）。Quantity 即生成秒数，
// UserAmount/SupplierCost 为按定价表结算金额（"供应商成本 vs 用户计费"分账，G3-2）。
type ProjectUsage struct {
	ProjectID    string
	Seconds      float64
	JobCount     int64
	UserAmount   float64
	SupplierCost float64
	Currency     string
}

// Store 是配额与用量端口。所有操作必须原子且幂等。
type Store interface {
	// Reserve 原子预占 units；超出上限返回 ErrInsufficientQuota。
	// 同一 (tenant, logicalOperationID, kind) 重复调用幂等返回既有预占。
	Reserve(ctx context.Context, tenantID, logicalOperationID string, kind Kind, units float64) (*Reservation, error)
	// Settle 以实际用量结算预占，并写入 usage_ledger（重复结算幂等）。
	Settle(ctx context.Context, tenantID, logicalOperationID string, kind Kind, actualUnits float64, priceVersion string) error
	// Release 释放预占（任务取消/失败未产生用量）。
	Release(ctx context.Context, tenantID, logicalOperationID string, kind Kind) error
	// GetQuota 读取额度；未配置时返回不限量默认值。
	GetQuota(ctx context.Context, tenantID string, kind Kind) (*Quota, error)
	// SetLimit 配置/更新额度上限与管理定价版本。
	SetLimit(ctx context.Context, tenantID string, kind Kind, limitUnits float64, priceVersion string) error
}
