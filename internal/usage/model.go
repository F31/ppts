// Package usage 负责额度预占、结算、账本与成本记录（V4.0 §4.2/§12.2）。
// 供应商成本与用户计费分开记录；不以供应商回调直接修改余额。
package usage

import (
	"context"
	"errors"
	"time"
)

// Kind 是计量种类（用户计费口径）。首批以生成时长（秒）计量。
type Kind string

const KindGenSeconds Kind = "gen_seconds"

// CharsPerSecond 是中文播报时长估算系数（用于生成前预估；实际以合成时长结算）。
const CharsPerSecond = 5.0

// EstimateSeconds 依据讲稿字数估算播报秒数（仅用于预占，结算以真实时长为准）。
func EstimateSeconds(runeCount int) float64 {
	if runeCount <= 0 {
		return 0
	}
	return float64(runeCount) / CharsPerSecond
}

var (
	// ErrInsufficientQuota 表示预占会导致超出额度上限。
	ErrInsufficientQuota = errors.New("usage: insufficient quota")
	// ErrReservationNotFound 表示逻辑操作没有预占记录。
	ErrReservationNotFound = errors.New("usage: reservation not found")
	// ErrReservationReleased 表示预占已释放，不能再结算。
	ErrReservationReleased = errors.New("usage: reservation already released")
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
