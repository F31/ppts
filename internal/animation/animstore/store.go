// Package animstore 是动画规格的持久化实现（SQLite / PostgreSQL）。
//
// 与规格模型分包的唯一理由是依赖面：模型与校验只依赖标准库，
// 而持久化实现会拉进 modernc sqlite 与 pgx。渲染服务（cmd/renderer）只需要模型，
// 分不开就会把数据库驱动链进一个「只做光栅渲染」的容器——体积与 CVE 面都白背。
//
// 依赖方向固定为 animstore → animation，反向不允许（模型不认识存储）。
package animstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/F31/ppts/internal/animation"
)

// Store 是动画规格的存储端口。两个实现（SQLite / PostgreSQL）共享同一套行为契约，
// 由 store_test.go 的 runStoreSuite 对两者跑同一组用例。
//
// 与 narration.Store 的差异只有一处：**不做 legacy 分叉**。讲稿在旧版本上首次编辑会从
// legacy 复制一份，因为文字与源版本弱相关；动画规格强依赖元素 id 与页时长，旧版锚点在新
// 版排版里可能已不存在，静默继承会直接产出校验失败的数据。新源版本首次写入即新建，
// 由调用方用 animation.DefaultSpec 生成。
type Store interface {
	// Get 取指定源版本下的规格；SourceRevisionNo>0 时精确匹配优先，缺失回退 legacy(0)。
	Get(ctx context.Context, key animation.Key) (*animation.Revision, error)

	// Put 写入规格内容（不含状态）。expected 是调用方见到的 revision：
	//   - 行不存在：expected 必须为 0，写入后 revision = 1；
	//   - 行存在：先判 locked（animation.ErrLocked），再判 revision != expected
	//     （animation.ErrConflict{Latest}），通过后 revision += 1。
	// 本方法不承担业务校验（V1–V12 属 animation.Validate），也不改写 Status。
	Put(ctx context.Context, rev *animation.Revision, expected int64) (*animation.Revision, error)

	// SetStatus 迁移审批状态，不允许跨级（draft→approved→locked，locked 可回 approved）。
	// 状态变更不递增 revision：内容没变，前端不必因此重新拉取对比。
	SetStatus(ctx context.Context, key animation.Key, status animation.Status) (*animation.Revision, error)
}

// marshalTracks / unmarshalTracks 是列编码细节：tracks 存成 JSON 文本，
// NULL 与 `[]` 在读端统一成空切片。只有存储实现在意这件事，因此留在本包。
func marshalTracks(tracks []*animation.Track) ([]byte, error) {
	if tracks == nil {
		return []byte("[]"), nil
	}
	b, err := json.Marshal(tracks)
	if err != nil {
		return nil, fmt.Errorf("animation: marshal tracks: %w", err)
	}
	return b, nil
}

func unmarshalTracks(raw []byte) ([]*animation.Track, error) {
	if len(raw) == 0 {
		return []*animation.Track{}, nil
	}
	var tracks []*animation.Track
	if err := json.Unmarshal(raw, &tracks); err != nil {
		return nil, fmt.Errorf("animation: unmarshal tracks: %w", err)
	}
	if tracks == nil {
		tracks = []*animation.Track{}
	}
	return tracks, nil
}
