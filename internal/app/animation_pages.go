package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/design"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/render"
)

// 本文件是导出路径与「动画数据从哪来」之间的组装层（tasks 2.0）。
//
// 它做三件事，且只做这三件事：
//
//	1. 把每页的页图投递到渲染服务能读的位置（AssetPublisher，单机=本地盘）；
//	2. 按快照里的规格身份取回规格本体，并核对版本（AnimationSpecSource）；
//	3. 把时间轴的逐页区间换算成渲染输入（时长、分段偏移）与元素几何（PageGeometrySource）。
//
// 它不读数据库之外的东西、不提交渲染、不决定降级。取不到规格的那一页按「没有动画」处理
// （Revision=nil），而不是让整次导出退回静态——用户可能只给一部分页配了动效，
// 这是常态而不是失败。

// AnimationSpecSource 取一页的动画规格。
//
// 用接口而不是直接依赖 animstore.Store：组装层只需要 Get，
// 而 Store 还带着写入与状态机（Put/SetStatus）。接口窄一点，
// 测试就不必造一个能写的假存储。
type AnimationSpecSource interface {
	Get(ctx context.Context, key animation.Key) (*animation.Revision, error)
}

// PageGeometrySource 返回一页里可挂动效的元素（含 EMU 几何）。
//
// 几何目前还没有持久化来源（tasks 2.0b），所以它是可选注入：
// 未注入时元素级轨道会被编译成 anchor_missing 诊断并跳过，
// **整页轨道（转场、整页推拉/淡入）仍然有效**。这条区别很重要——
// 缺几何不等于「动画完全没用」，只是能做的动效少一类。
type PageGeometrySource interface {
	Elements(ctx context.Context, slideID string) ([]render.Element, error)
}

// AnimationPageAssembler 是 AnimationRequestBuilder 的默认实现。
type AnimationPageAssembler struct {
	publisher AssetPublisher
	specs     AnimationSpecSource
	geometry  PageGeometrySource
	logger    *slog.Logger
}

// NewAnimationPageAssembler 组装一个可用的构建器。publisher 与 specs 都必须非 nil：
// 两者缺一都说明接线错了，让它在启动时明确失败，而不是每次导出静默降级。
func NewAnimationPageAssembler(publisher AssetPublisher, specs AnimationSpecSource) *AnimationPageAssembler {
	return &AnimationPageAssembler{publisher: publisher, specs: specs}
}

// WithGeometry 注入元素几何来源。未注入 = 只编译整页轨道（见 PageGeometrySource）。
func (a *AnimationPageAssembler) WithGeometry(g PageGeometrySource) *AnimationPageAssembler {
	a.geometry = g
	return a
}

// WithLogger 注入日志。降级与版本不符靠日志留痕，nil 时静默。
func (a *AnimationPageAssembler) WithLogger(l *slog.Logger) *AnimationPageAssembler {
	a.logger = l
	return a
}

// Build 实现 AnimationRequestBuilder。
func (a *AnimationPageAssembler) Build(ctx context.Context, in AnimationPageInput) (AnimationRequest, error) {
	if a == nil || a.publisher == nil {
		return AnimationRequest{}, errors.New("animation: asset publisher is not configured")
	}
	if a.specs == nil {
		return AnimationRequest{}, errors.New("animation: animation spec source is not configured")
	}
	// 页图与时间轴必须一一对应：少一张会把后面所有页的图错位一格，
	// 而成片里看不出来「错了」，只看到「这几页的画面和配音对不上」。
	if len(in.PagePNGs) != len(in.Slides) {
		return AnimationRequest{}, fmt.Errorf("animation: %d page images for %d slides", len(in.PagePNGs), len(in.Slides))
	}
	if len(in.Slides) == 0 {
		return AnimationRequest{}, errors.New("animation: timeline has no slides")
	}

	canvas := design.Default().Canvas
	req := AnimationRequest{
		Width:      firstPositive(in.Snapshot.Width, canvas.Width),
		Height:     firstPositive(in.Snapshot.Height, canvas.Height),
		FPS:        firstPositive(in.Snapshot.FPS, defaultAnimationFPS),
		Background: "",
		Pages:      make([]AnimationPage, 0, len(in.Slides)),
	}
	refs := specRefsBySlide(in.Snapshot)

	for i, slide := range in.Slides {
		durationMS := (slide.EndUS - slide.StartUS) / 1000
		if durationMS <= 0 {
			// 零长页在渲染侧会变成一帧，拼接时判不出漂移，成片里那一页一闪而过。
			// 这属于时间轴数据有问题，不是渲染能补救的事，交回上层走静态路径。
			return AnimationRequest{}, fmt.Errorf("animation: slide %q has non-positive duration", slide.SlideID)
		}
		rel, err := a.publisher.Publish(ctx, "image/png", in.PagePNGs[i])
		if err != nil {
			return AnimationRequest{}, fmt.Errorf("animation: publish page %d image: %w", i, err)
		}
		req.Pages = append(req.Pages, AnimationPage{
			SlideID:        slide.SlideID,
			DurationMs:     durationMS,
			BaseImage:      rel,
			Elements:       a.elements(ctx, slide.SlideID),
			Revision:       a.revision(ctx, in, slide.SlideID, refs),
			SegmentOffsets: segmentOffsets(slide),
		})
	}
	return req, nil
}

// revision 取回一页的规格并核对版本。
//
// 版本不符（导出排队期间规格被改）时**这一页按无规格处理**，而不是拿最新版渲下去：
// snapshotHash 已经承诺了「这次产物依据的是哪一版」，用新版会让产物与它的缓存键、
// 幂等载荷对不上——同一份键产出两份不同的视频，比少一页动画难查得多。
// 差异必须留痕，否则用户只会看到「我明明配了动效」。
func (a *AnimationPageAssembler) revision(ctx context.Context, in AnimationPageInput, slideID string,
	refs map[string]AnimationSpecRef) *animation.Revision {
	ref, ok := refs[slideID]
	if !ok {
		return nil // 这页本来就没配动效，常态。
	}
	key := animation.Key{
		TenantID:         in.TenantID,
		ProjectID:        in.ProjectID,
		SlideID:          slideID,
		Language:         ref.Language,
		SourceRevisionNo: ref.SourceRevisionNo,
	}.Normalize()
	if err := key.Validate(); err != nil {
		a.warn("animation: invalid spec key, page stays static",
			"slide", slideID, "error", err.Error())
		return nil
	}
	rev, err := a.specs.Get(ctx, key)
	if err != nil {
		// 存储故障也按「这页没有动画」处理：动画是增强层（ADR-21），
		// 一次读库抖动不该让整次导出退回静态。
		if !errors.Is(err, animation.ErrNotFound) {
			a.warn("animation: load spec failed, page stays static",
				"slide", slideID, "language", key.Language, "error", err.Error())
		}
		return nil
	}
	if rev == nil {
		return nil
	}
	if rev.Revision != ref.Revision {
		a.warn("animation: spec revision changed since snapshot, page stays static",
			"slide", slideID, "snapshot_revision", ref.Revision, "stored_revision", rev.Revision)
		return nil
	}
	return rev
}

// elements 取元素几何。几何是可选数据：取不到就只编整页轨道。
func (a *AnimationPageAssembler) elements(ctx context.Context, slideID string) []render.Element {
	if a.geometry == nil {
		return nil
	}
	elems, err := a.geometry.Elements(ctx, slideID)
	if err != nil {
		a.warn("animation: load element geometry failed, page only gets page-level tracks",
			"slide", slideID, "error", err.Error())
		return nil
	}
	return elems
}

func (a *AnimationPageAssembler) warn(msg string, args ...any) {
	if a.logger == nil {
		return
	}
	a.logger.Warn(msg, args...)
}

// specRefsBySlide 把快照里的规格身份按页索引。同页重复只保留第一条：
// 快照是对一份输入的承诺，重复本身说明上游拼错了，取第一条至少是确定的。
func specRefsBySlide(snapshot ExportSnapshot) map[string]AnimationSpecRef {
	if snapshot.Animation == nil {
		return nil
	}
	refs := make(map[string]AnimationSpecRef, len(snapshot.Animation.Specs))
	for _, ref := range snapshot.Animation.Specs {
		slideID := strings.TrimSpace(ref.SlideID)
		if slideID == "" {
			continue
		}
		if _, dup := refs[slideID]; dup {
			continue
		}
		refs[slideID] = ref
	}
	return refs
}

// segmentOffsets 把分段的绝对时间换算成**相对本页起点**的毫秒偏移。
// 负偏移（分段的开始早于本页）归零：它只可能来自错位的时间轴，
// 而归零至少能保证动效从页首开始，而不是从负时间开始。
func segmentOffsets(slide media.SlideCue) map[string]int64 {
	if len(slide.Segments) == 0 {
		return nil
	}
	offsets := make(map[string]int64, len(slide.Segments))
	for _, seg := range slide.Segments {
		if seg.SegmentID == "" {
			continue
		}
		off := (seg.StartUS - slide.StartUS) / 1000
		if off < 0 {
			off = 0
		}
		offsets[seg.SegmentID] = off
	}
	if len(offsets) == 0 {
		return nil
	}
	return offsets
}

func firstPositive(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

// defaultAnimationFPS 与静态编码的缺省值一致，避免两条路径出片帧率不同。
const defaultAnimationFPS = 30
