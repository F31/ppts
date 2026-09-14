// Package pricing 提供定价表与"供应商成本 vs 用户计费"分账（V4.0 §12.2，G3-2）。
// 定价按计量种类（usage.Kind 字符串）配置每单位用户价与供应商成本；金额以浮点记录，
// 账本（usage_ledger）保存 user_amount/supplier_cost/currency，二者分开不混淆。
// 正式供应商价格接入前使用可配置默认价目（PPTS_PRICE_BOOK 覆盖）。
package pricing

import (
	"encoding/json"
	"fmt"
	"os"
)

// Book 是定价表。Version 对应账本 price_version，便于审计/追溯。
type Book struct {
	Version  string             `json:"version"`
	Currency string             `json:"currency"`
	User     map[string]float64 `json:"user"`     // kind -> 每单位用户计费金额
	Supplier map[string]float64 `json:"supplier"` // kind -> 每单位供应商成本
}

// Default 返回占位默认价目（正式定价表接入前使用，价格随正式供应商确定后调整）。
func Default() *Book {
	return &Book{
		Version:  "default-2026-09",
		Currency: "CNY",
		User: map[string]float64{
			"gen_seconds": 0.01,
		},
		Supplier: map[string]float64{
			"gen_seconds": 0.004,
		},
	}
}

// UserAmount 返回用户计费金额（units<=0 或未配置时 0）。
func (b *Book) UserAmount(kind string, units float64) float64 {
	rate, ok := b.User[kind]
	if !ok || units <= 0 {
		return 0
	}
	return rate * units
}

// SupplierAmount 返回供应商成本（units<=0 或未配置时 0）。
func (b *Book) SupplierAmount(kind string, units float64) float64 {
	rate, ok := b.Supplier[kind]
	if !ok || units <= 0 {
		return 0
	}
	return rate * units
}

// FromEnv 从环境变量构建定价表；未配置时返回 Default。
// PPTS_PRICE_BOOK 为 JSON，例如：
// {"version":"v1","currency":"CNY","user":{"gen_seconds":0.01},"supplier":{"gen_seconds":0.004}}
func FromEnv() (*Book, error) {
	raw := os.Getenv("PPTS_PRICE_BOOK")
	if raw == "" {
		return Default(), nil
	}
	var b Book
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return nil, fmt.Errorf("pricing: invalid PPTS_PRICE_BOOK: %w", err)
	}
	if b.Currency == "" {
		b.Currency = "CNY"
	}
	return &b, nil
}
