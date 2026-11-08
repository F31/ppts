package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/render"
	"github.com/F31/ppts/internal/share"
)

// htmlBundleInput 组装成品包的输入。
//
// 目录包（zip）与单文件（.html）**必须**从这一份输入产出：两者的差别只在
// 「条目怎么交给用户」，不在「条目是什么」。分两处组装的话，动画降级、字体子集、
// 页图读取这些语义会慢慢分叉，而分叉的现场在客户手里（「zip 能播、单文件黑屏」）。
func (h *ExportHandler) htmlBundleInput(ctx context.Context, job *pipeline.Job, snapshot ExportSnapshot,
	bundle *TimelineAsset, snapshotHash string) (share.BundleInput, []share.PageAnimation, error) {
	if bundle == nil || bundle.Timeline == nil {
		return share.BundleInput{}, nil, errors.New("export job: timeline bundle is empty")
	}
	// 页图先读一遍：挖空必须基于原始 PNG（转码后再挖取色会偏），而 builder 也要写它们。
	// 读到的字节同时交给两边，避免同一份对象读两次。
	pagePNGs, pngErr := h.readPagePNGs(ctx, job.TenantID, snapshot.PagePNGKeys)
	if pngErr != nil && h.logger != nil {
		// 读失败不致命：打包器会退回按 key 再读一次。留痕是为了让「包很慢」有解释。
		h.logger.Warn("export: read page images for html bundle failed; bundle will fall back to re-reading",
			"job", job.ID, "error", pngErr)
	}
	pages, animErr := h.htmlBundleAnimations(ctx, job, snapshot, bundle, pagePNGs)
	if animErr != nil {
		// 动画是增强层：组装不出来就出静态包，与 M6-1 逐像素一致。
		// 但必须留痕——用户会看到「我勾了动画，包里的页面却不动」。
		if h.logger != nil {
			h.logger.Warn("export: html bundle animation unavailable; packing static bundle",
				"job", job.ID, "error", animErr)
		}
		pages = nil
	}
	return share.BundleInput{
		Timeline:     bundle.Timeline,
		PagePNGKeys:  snapshot.PagePNGKeys,
		PagePNGBytes: pagePNGs,
		Pages:        pages,
		SRTKey:       bundle.SRTKey,
		VTTKey:       bundle.VTTKey,
		Width:        snapshot.Width,
		Height:       snapshot.Height,
		FPS:          snapshot.FPS,
		Title:        bundle.Timeline.SourceDisplayName,
		Font:         h.htmlBundleFont(ctx, job, bundle),
		SnapshotHash: snapshotHash,
		CreatedAt:    time.Now(),
		TextCopy:     snapshot.TextCopy,
		Watermark:    snapshot.Watermark,
	}, pages, nil
}

// audioEncoder 返回可用的 AAC 编码器；nil 表示成品包退回 WAV。
//
// 返回接口而不是 *media.MP4Encoder：把 nil 指针装进接口会得到「非 nil 的接口」，
// 打包器据此判断"有编码器"就会对一个空壳调用，最后在日志里看到一条本不该出现的
// 编码失败——把类型转换收在这一个地方，判断才可靠。
func (h *ExportHandler) audioEncoder() share.M4AEncoder {
	if h.encoder == nil {
		return nil
	}
	return h.encoder
}

// packHTMLBundle 打包「静态讲解成品包」（html_bundle，M6-1）：解压双击 index.html 即可播放。
func (h *ExportHandler) packHTMLBundle(ctx context.Context, job *pipeline.Job, snapshot ExportSnapshot,
	bundle *TimelineAsset, snapshotHash string) (*os.File, error) {
	in, pages, err := h.htmlBundleInput(ctx, job, snapshot, bundle, snapshotHash)
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp("", "ppts-htmlbundle-*.zip")
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}
	builder := share.NewBundleBuilder(tenantObjectReader{handler: h, tenantID: job.TenantID}, h.logger).
		SetAudioEncoder(h.audioEncoder()).
		SetPageImageEncoder(h.pageImageEncoder)
	manifest, err := builder.Build(ctx, in, tmp)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("export job: pack html bundle: %w", err)
	}
	if h.logger != nil {
		h.logger.Info("export: html bundle assembled",
			"job", job.ID, "slides", len(manifest.Slides), "audio_segments", len(manifest.Audio),
			"audio_mix", manifest.AudioMix != "", "animated_slides", animatedPageCount(pages))
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, err
	}
	return tmp, nil
}

// packHTMLSingle 打包单文件成品（html_single，ADR-029 ⑤）：一个 .html，双击即播。
//
// 与目录包同源：同一个 htmlBundleInput、同一个 buildInto。这里只负责内联。
func (h *ExportHandler) packHTMLSingle(ctx context.Context, job *pipeline.Job, snapshot ExportSnapshot,
	bundle *TimelineAsset, snapshotHash string) (*os.File, error) {
	in, pages, err := h.htmlBundleInput(ctx, job, snapshot, bundle, snapshotHash)
	if err != nil {
		return nil, err
	}
	builder := share.NewBundleBuilder(tenantObjectReader{handler: h, tenantID: job.TenantID}, h.logger).
		SetAudioEncoder(h.audioEncoder()).
		SetPageImageEncoder(h.pageImageEncoder)
	data, manifest, err := builder.BuildSingle(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("export job: pack single-file html: %w", err)
	}
	tmp, err := os.CreateTemp("", "ppts-htmlsingle-*.html")
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, err
	}
	if h.logger != nil {
		// 体积如实记：单文件是 base64 内联，约为目录包的 1.45 倍（1.33 的编码膨胀
		// + 页图在 <noscript> 里还有一份完整的 data URI，禁 JS 时没有脚本能还原）。
		// 用户抱怨「附件太大」时，这条日志是唯一能对上账的数字。
		h.logger.Info("export: html single file assembled", "job", job.ID, "bytes", len(data),
			"slides", len(manifest.Slides), "audio_segments", len(manifest.Audio),
			"animated_slides", animatedPageCount(pages))
	}
	return tmp, nil
}

// animatedPageCount 数出真的带分层的页数（日志用）。
func animatedPageCount(pages []share.PageAnimation) int {
	n := 0
	for _, p := range pages {
		if len(p.Layers) > 0 {
			n++
		}
	}
	return n
}

// htmlBundleFont 取成品包要内嵌的字体：按这份讲稿的字符裁一小份。
//
// 字体是**增强**，任何失败都返回 nil 让包退回到「系统字体回退」。但降级必须留痕：
// 「用户拿到一个没内嵌字体的包」与「导出功能坏了」在界面上一模一样，只有日志能区分。
//
// 单独一个 20s 预算，而不是直接用导出的 ctx：子集正常是亚秒级，卡到 20s 说明
// 渲染服务有问题，这时候应该让包不带字体尽快出去，而不是让整个导出跟着慢慢超时。
func (h *ExportHandler) htmlBundleFont(ctx context.Context, job *pipeline.Job, bundle *TimelineAsset) *share.FontAsset {
	if h.renderer == nil || bundle == nil || bundle.Timeline == nil {
		return nil
	}
	charset, err := share.FontRequestCharset(bundle.Timeline.SourceDisplayName, bundle.Timeline)
	if err != nil {
		h.warnExport("export: compute font charset failed; bundle will not embed fonts", job, err)
		return nil
	}
	subCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, err := h.renderer.SubsetFont(subCtx, charset.Runes)
	if err != nil {
		if errors.Is(err, render.ErrFontSubsetUnavailable) {
			// 不是故障：这个部署没启用字体子集（旧版本渲染器 / 未配母版）。
			if h.logger != nil {
				h.logger.Info("export: renderer has no font subset capability; bundle will not embed fonts",
					"job", job.ID)
			}
		} else {
			h.warnExport("export: font subset failed; bundle will not embed fonts", job, err)
		}
		return nil
	}
	if len(res.Missing) > 0 && h.logger != nil {
		h.logger.Warn("export: font subset misses some characters; they will fall back to system fonts",
			"job", job.ID, "missing", len(res.Missing), "sample", string(res.Missing[:min(len(res.Missing), 8)]))
	}
	return &share.FontAsset{
		Family:  res.Family,
		Format:  res.Format,
		Version: res.BaseVersion,
		Data:    res.Data,
	}
}
