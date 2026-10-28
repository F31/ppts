package animation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrInvalidKey 表示隔离维度不完整（缺租户/项目/页）。
	ErrInvalidKey = errors.New("animation: invalid key")
	// ErrStatusTransition 表示审批状态迁移不被允许（只允许 approved←draft|locked、locked←approved）。
	ErrStatusTransition = errors.New("animation: status transition not allowed")
)

// Store 是动画规格的存储端口。两个实现（SQLite / PostgreSQL）共享同一套行为契约，
// 由 store_test.go 的 runStoreSuite 对两者跑同一组用例。
//
// 与 narration.Store 的差异只有一处：**不做 legacy 分叉**。讲稿在旧版本上首次编辑会从
// legacy 复制一份，因为文字与源版本弱相关；动画规格强依赖元素 id 与页时长，旧版锚点在新
// 版排版里可能已不存在，静默继承会直接产出校验失败的数据。新源版本首次写入即新建，
// 由调用方用 DefaultSpec 生成。
type Store interface {
	// Get 取指定源版本下的规格；SourceRevisionNo>0 时精确匹配优先，缺失回退 legacy(0)。
	Get(ctx context.Context, key Key) (*Revision, error)

	// Put 写入规格内容（不含状态）。expected 是调用方见到的 revision：
	//   - 行不存在：expected 必须为 0，写入后 revision = 1；
	//   - 行存在：先判 locked（ErrLocked），再判 revision != expected（ErrConflict{Latest}），
	//     通过后 revision += 1。
	// 本方法不承担业务校验（V1–V12 属 validate.go），也不改写 Status。
	Put(ctx context.Context, rev *Revision, expected int64) (*Revision, error)

	// SetStatus 迁移审批状态，不允许跨级（draft→approved→locked，locked 可回 approved）。
	// 状态变更不递增 revision：内容没变，前端不必因此重新拉取对比。
	SetStatus(ctx context.Context, key Key, status Status) (*Revision, error)
}

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

// normalize 补齐默认语言（与讲稿一致：zh-CN）。
func (k Key) normalize() Key {
	if strings.TrimSpace(k.Language) == "" {
		k.Language = "zh-CN"
	}
	return k
}

func (k Key) validate() error {
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

// normalize 补齐语言并把 nil 轨道落成空切片：JSONB 列 NOT NULL，
// 且空切片序列化为 `[]` 而非 `null`，读端不必再区分两者。
func (r *Revision) normalize() {
	if strings.TrimSpace(r.Language) == "" {
		r.Language = "zh-CN"
	}
	if r.Tracks == nil {
		r.Tracks = []*Track{}
	}
}

// allowedTransition 固化状态机。跨级（draft→locked）一律拒绝：
// 锁定意味着"已审阅并定稿"，跳过审阅直接锁定会让审批记录失去意义。
func allowedTransition(from, to Status) bool {
	switch to {
	case StatusApproved:
		return from == StatusDraft || from == StatusLocked
	case StatusLocked:
		return from == StatusApproved
	default:
		return false
	}
}

func marshalTracks(tracks []*Track) ([]byte, error) {
	if tracks == nil {
		return []byte("[]"), nil
	}
	b, err := json.Marshal(tracks)
	if err != nil {
		return nil, fmt.Errorf("animation: marshal tracks: %w", err)
	}
	return b, nil
}

func unmarshalTracks(raw []byte) ([]*Track, error) {
	if len(raw) == 0 {
		return []*Track{}, nil
	}
	var tracks []*Track
	if err := json.Unmarshal(raw, &tracks); err != nil {
		return nil, fmt.Errorf("animation: unmarshal tracks: %w", err)
	}
	if tracks == nil {
		tracks = []*Track{}
	}
	return tracks, nil
}
