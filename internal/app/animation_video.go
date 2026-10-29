package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/pipeline"
)

// 本文件把「逐页动画片段」合成为一张 MP4，并处理两件必须由这一层负责的事：
//
//	1. 单页降级（ADR-23）：没拿到片段或时长漂移的页，替换成**同参数的静态段**，
//	   而不是跳过——跳过会让成片比时间轴短，音频整体前移，比多一张静态图糟得多。
//	2. 整体降级（ADR-21）：凡是无法保证音画同步的失败，一律返回 nil 交回静态路径，
//	   绝不返回 error。动画是增强层，它不该有能力让一次导出失败。

// AnimationVideoOptions 是动画成片合成的输入。
type AnimationVideoOptions struct {
	// PagePNGs 与动画请求的页同序，仅用于把降级页替换成静态段。
	PagePNGs [][]byte
	// PageDurationsMS 是每页的计划时长，来自时间轴。它同时是拼接的时长基准，
	// 所以降级段必须用同一个值——否则「用静态段修复漂移」这个动作本身又引入一次漂移。
	PageDurationsMS []int64
	// AudioPath 是时间轴音频（WAV）。空表示用静音轨填充（与静态版一致）。
	AudioPath     string
	BurnSubtitles bool
	SubtitleASS   []byte
	SubtitleSRT   []byte
	FPS           int
	Width, Height int
}

// AnimatedVideo 是动画版成片及其留痕。
type AnimatedVideo struct {
	File *os.File
	// Degraded 是没拿到动画段、已替换为静态段的页（0 基）。
	Degraded []int
	// Drifted 是 Degraded 的子集：片段拿到了，但时长与计划偏差超限。
	// 与 Degraded 分开记，是为了让「渲染服务少给了一段」和「渲染时长不准」
	// 在日志里能分辨——两者的修法完全不同。
	Drifted []int
	// Reencoded 为 true 表示整片走了重编码（有字幕、参数不一致，或 copy 失败回退）。
	Reencoded bool
	PlanHash  string
}

// animatedVideo 判定是否走动画通道，并在可以时产出动画版成片。
//
// 三个前置缺一不可：渲染客户端（开关）、快照里的逐页规格（用户确实要动画）、
// 组装器（能把底图与几何交出去）。缺任何一个都直接回静态路径，
// 不做「先探一下服务是否活着」这种事——探测本身也是对外部服务的依赖。
func (h *ExportHandler) animatedVideo(ctx context.Context, job *pipeline.Job, snapshot ExportSnapshot,
	bundle *TimelineAsset, pagePNGs [][]byte, pageDurations []int64,
	audioPath string, subtitleASS []byte) *AnimatedVideo {
	if h.renderer == nil || h.animationPages == nil {
		return nil
	}
	if snapshot.Animation == nil || len(snapshot.Animation.Specs) == 0 {
		return nil
	}
	if bundle == nil || bundle.Timeline == nil {
		return nil
	}
	req, err := h.animationPages(ctx, AnimationPageInput{
		Snapshot:  snapshot,
		TenantID:  job.TenantID,
		ProjectID: job.ProjectID,
		Slides:    bundle.Timeline.Slides,
		PagePNGs:  pagePNGs,
	})
	if err != nil {
		h.logAnimationSkip("animation: assemble pages: " + err.Error())
		return nil
	}
	// 页数必须与时间轴一致：少一页成片比时间轴短（音频前移），多一页把后面所有页顶偏。
	if len(req.Pages) != len(pagePNGs) || len(req.Pages) != len(pageDurations) {
		h.logAnimationSkip(fmt.Sprintf("animation: assembled %d pages for %d slides", len(req.Pages), len(pagePNGs)))
		return nil
	}
	// 画布参数以快照为准：快照是这次导出的输入契约，组装器给的只是建议值，
	// 两者不一致时按快照出片，才不会出现「产物尺寸与快照描述不符」。
	if req.Width == 0 || req.Height == 0 {
		req.Width, req.Height = snapshot.Width, snapshot.Height
	}
	if req.FPS == 0 {
		req.FPS = snapshot.FPS
	}
	return h.tryAnimation(ctx, req, AnimationVideoOptions{
		PagePNGs:        pagePNGs,
		PageDurationsMS: pageDurations,
		AudioPath:       audioPath,
		BurnSubtitles:   snapshot.BurnSubtitles,
		SubtitleASS:     subtitleASS,
		FPS:             snapshot.FPS,
		Width:           snapshot.Width,
		Height:          snapshot.Height,
	})
}

// logAnimationPages 留痕「哪几页没有动画」。降级是静默的用户体验损失，
// 日志是唯一能让「用户说这页没动」变得可查的东西。
func (h *ExportHandler) logAnimationPages(av *AnimatedVideo, total int) {
	if h.logger == nil || len(av.Degraded) == 0 {
		return
	}
	h.logger.Warn("animation export contains still pages",
		"pages", av.Degraded, "drifted", av.Drifted, "total_pages", total, "plan_hash", av.PlanHash)
}

// tryAnimation 尝试产出动画版成片。返回 nil 表示「不用动画版」，调用方继续走静态编码。
//
// 刻意不返回 error（ADR-21）：一旦有 error，调用链上迟早有人把它当业务失败，
// 于是「渲染服务抖一下」就变成「用户的导出失败」。失败原因通过日志留痕。
//
// 开关关闭时本函数立刻返回，**不探测、不提交、不建临时目录**——
// 「关闭」必须在行为上与「没有这个功能」等价，而不是「跑了再丢掉」。
func (h *ExportHandler) tryAnimation(ctx context.Context, req AnimationRequest, opts AnimationVideoOptions) *AnimatedVideo {
	if h.renderer == nil || h.encoder == nil {
		return nil
	}
	if len(req.Pages) == 0 || len(req.Pages) != len(opts.PagePNGs) || len(req.Pages) != len(opts.PageDurationsMS) {
		// 页数对不上是调用方的错，但按 ADR-21 也只能降级：
		// 拿一份错位的输入去拼接，产物是「音画整体错位」，比静态版糟得多。
		h.logAnimationSkip("animation: page count mismatch between request, images and durations")
		return nil
	}
	// 每次导出独占目录：片段按 segment_NNN.mp4 落盘，共用目录会互相覆盖，
	// 而覆写发生在下载阶段，表现得像「这一页放错了」。
	workDir, err := os.MkdirTemp("", "ppts-anim-*")
	if err != nil {
		h.logAnimationSkip("animation: work dir: " + err.Error())
		return nil
	}
	defer os.RemoveAll(workDir)

	res, err := h.RenderAnimation(ctx, req, workDir)
	if err != nil {
		// 服务不可达、超时、整片失败都到这里：退回静态版，导出仍然成功。
		h.logAnimationSkip(err.Error())
		return nil
	}

	segments := make([]media.VideoSegment, len(req.Pages))
	degraded := make([]int, 0, len(res.Missing))
	for i := range segments {
		if res.Segments[i] != "" {
			segments[i] = media.VideoSegment{Path: res.Segments[i], DurationMS: opts.PageDurationsMS[i]}
			continue
		}
		still, serr := h.stillSegment(ctx, opts, workDir, i)
		if serr != nil {
			// 连静态段都做不出来，就没有任何办法保住这一页的时长——
			// 整体退回静态路径（那里每一页都是静态图，至少音画同步）。
			h.logAnimationSkip(fmt.Sprintf("animation: still segment for page %d: %v", i, serr))
			return nil
		}
		segments[i] = *still
		degraded = append(degraded, i)
	}

	// 成片写在 workDir 之外：workDir 会整体删除，而返回的 *os.File 还要被上层读取。
	out, err := os.CreateTemp("", "ppts-anim-out-*.mp4")
	if err != nil {
		h.logAnimationSkip("animation: out file: " + err.Error())
		return nil
	}
	outPath := out.Name()
	out.Close()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(outPath)
		}
	}()

	concatOpts := media.ConcatOptions{
		Segments:         segments,
		AudioPath:        opts.AudioPath,
		BurnSubtitles:    opts.BurnSubtitles,
		SubtitleASS:      opts.SubtitleASS,
		SubtitleSRT:      opts.SubtitleSRT,
		SubtitleFontName: h.subtitleFontName,
		FPS:              opts.FPS,
		Width:            opts.Width,
		Height:           opts.Height,
		OutPath:          outPath,
	}
	cr, err := h.encoder.ConcatSegments(ctx, concatOpts)
	if err != nil {
		h.logAnimationSkip("animation: concat: " + err.Error())
		return nil
	}
	var drifted []int
	if len(cr.Drifted) > 0 {
		// 时长漂移的页换成静态段重拼一次。只重拼一次：仍然漂移说明问题不在某一页，
		// 继续重试只是把一次慢失败变成长失败，不如直接退回静态版。
		for _, i := range cr.Drifted {
			if containsInt(degraded, i) {
				continue
			}
			still, serr := h.stillSegment(ctx, opts, workDir, i)
			if serr != nil {
				h.logAnimationSkip(fmt.Sprintf("animation: still segment for drifted page %d: %v", i, serr))
				return nil
			}
			segments[i] = *still
			degraded = append(degraded, i)
			drifted = append(drifted, i)
		}
		sort.Ints(degraded)
		sort.Ints(drifted)
		cr, err = h.encoder.ConcatSegments(ctx, concatOpts)
		if err != nil {
			h.logAnimationSkip("animation: concat after drift repair: " + err.Error())
			return nil
		}
		if len(cr.Drifted) > 0 {
			h.logAnimationSkip(fmt.Sprintf("animation: still drifted after repair on pages %v", cr.Drifted))
			return nil
		}
	}
	f, err := os.Open(outPath)
	if err != nil {
		h.logAnimationSkip("animation: open output: " + err.Error())
		return nil
	}
	cleanup = false
	return &AnimatedVideo{
		File:      f,
		Degraded:  degraded,
		Drifted:   drifted,
		Reencoded: cr.Reencoded,
		PlanHash:  res.PlanHash,
	}
}

// stillSegment 把一页的静态图编码成与动画段**同参数**的片段。
// 参数一致性是 -c:v copy 能成立的前提；不一致时整片只能退化成重编码，
// 而重编码一部 5 分钟的片子是分钟级的事（ADR-22）。
func (h *ExportHandler) stillSegment(ctx context.Context, opts AnimationVideoOptions, workDir string, page int) (*media.VideoSegment, error) {
	png := opts.PagePNGs[page]
	if len(png) == 0 {
		return nil, fmt.Errorf("page %d has no image", page)
	}
	dst := filepath.Join(workDir, fmt.Sprintf("still_%03d.mp4", page))
	return h.encoder.EncodeStillSegment(ctx, png, opts.PageDurationsMS[page], dst, opts.FPS, opts.Width, opts.Height)
}

func (h *ExportHandler) logAnimationSkip(reason string) {
	if h.logger == nil {
		return
	}
	h.logger.Warn("animation channel degraded to static export", "reason", reason)
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
