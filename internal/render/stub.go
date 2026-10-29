package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StubBackend 是确定性的占位实现：按页写一个记录时长的文本文件，不产出真实视频。
//
// 它的用途是契约测试与端到端联通（提交 → 轮询 → 取产物），不是渲染。文件名带 .stub 后缀
// 是刻意的：任何下游都不能把它当 mp4 播放，也就不会有人误以为"动画已经能出片了"。
type StubBackend struct {
	// DelayPerPage 用于模拟渲染耗时（测试取消与进度时使用）。
	DelayPerPage time.Duration
}

// Name 返回 backend 名，出现在 /healthz。
func (b *StubBackend) Name() string { return "stub" }

// Render 写占位产物。它遵守与真实 backend 相同的契约：只写 dir、响应 ctx、回报进度。
func (b *StubBackend) Render(ctx context.Context, plan *Timeline, dir string,
	progress func(done, total int)) (RenderReport, error) {
	total := len(plan.Pages)
	for i, p := range plan.Pages {
		select {
		case <-ctx.Done():
			return RenderReport{}, ctx.Err()
		default:
		}
		name := fmt.Sprintf("segment_%03d.mp4.stub", i)
		body := fmt.Sprintf("stub segment\nslideId=%s\ndurationMs=%d\n", p.SlideID, p.DurationMs)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o640); err != nil {
			return RenderReport{}, fmt.Errorf("render: write stub segment: %w", err)
		}
		if progress != nil {
			progress(i+1, total)
		}
		if b.DelayPerPage > 0 {
			select {
			case <-ctx.Done():
				return RenderReport{}, ctx.Err()
			case <-time.After(b.DelayPerPage):
			}
		}
	}
	return RenderReport{}, nil
}
