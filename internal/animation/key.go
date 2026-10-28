package animation

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidKey 表示隔离维度不完整（缺租户/项目/页）。
var ErrInvalidKey = errors.New("animation: invalid key")

// ErrStatusTransition 表示审批状态迁移不被允许（只允许 approved←draft|locked、locked←approved）。
var ErrStatusTransition = errors.New("animation: status transition not allowed")

// Key 返回该规格的隔离维度。
func (r *Revision) Key() Key {
	return Key{
		TenantID:         r.TenantID,
		ProjectID:        r.ProjectID,
		SlideID:          r.SlideID,
		Language:         r.Language,
		SourceRevisionNo: r.SourceRevisionNo,
	}
}

// Normalize 补齐默认语言（与讲稿一致：zh-CN）。
//
// 导出给持久化实现使用：规整是「这个 key 长什么样」的语义，属于模型，
// 不属于某个 store。放在 animstore 里会让 SQLite 与 PostgreSQL 各写一份。
func (k Key) Normalize() Key {
	if strings.TrimSpace(k.Language) == "" {
		k.Language = "zh-CN"
	}
	return k
}

// Validate 校验隔离维度是否完整。与 Normalize 一样属于 key 自身的语义。
func (k Key) Validate() error {
	if strings.TrimSpace(k.TenantID) == "" {
		return fmt.Errorf("%w: tenant id is empty", ErrInvalidKey)
	}
	if strings.TrimSpace(k.ProjectID) == "" {
		return fmt.Errorf("%w: project id is empty", ErrInvalidKey)
	}
	if strings.TrimSpace(k.SlideID) == "" {
		return fmt.Errorf("%w: slide id is empty", ErrInvalidKey)
	}
	if k.SourceRevisionNo < 0 {
		return fmt.Errorf("%w: negative source revision", ErrInvalidKey)
	}
	return nil
}

// Normalize 补齐语言并把 nil 轨道落成空切片：JSONB 列 NOT NULL，
// 且空切片序列化为 `[]` 而非 `null`，读端不必再区分两者。
//
// 导出给持久化实现：写入前的规整是数据契约的一部分，不该由两个 store 各实现一遍。
func (r *Revision) Normalize() {
	if strings.TrimSpace(r.Language) == "" {
		r.Language = "zh-CN"
	}
	if r.Tracks == nil {
		r.Tracks = []*Track{}
	}
}

// AllowedTransition 固化状态机。跨级（draft→locked）一律拒绝：
// 锁定意味着"已审阅并定稿"，跳过审阅直接锁定会让审批记录失去意义。
//
// 导出给持久化实现：SetStatus 需要在事务内判定迁移合法性，但状态机本身是模型语义。
func AllowedTransition(from, to Status) bool {
	switch to {
	case StatusApproved:
		return from == StatusDraft || from == StatusLocked
	case StatusLocked:
		return from == StatusApproved
	default:
		return false
	}
}
