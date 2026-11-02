package app

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"log/slog"

	"github.com/F31/ppts/internal/design"
	sliderenderer "github.com/F31/ppts/internal/integrations/render"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/render"
	"github.com/F31/ppts/internal/share"
)

// compileAnimationPlan 把一次动画渲染请求编译成 IR。
//
// 抽出来是因为**有两个消费者**：MP4 通道（RenderAnimation → 提交渲染服务）与
// HTML 成品包（packHTMLBundle → 取 layers/box/clips）。两处若各自建 Compiler，
// 参数（画布、EMU、fps、底色、字体）迟早会有一处漏改，表现是「同一次导出，
// HTML 的动画和 MP4 的不一样」——这种缺陷只能靠人眼发现，而它恰好是最难解释的那类。
func (h *ExportHandler) compileAnimationPlan(req AnimationRequest) (*render.Timeline, error) {
	compiler, err := render.NewCompiler(render.Options{
		Canvas:         design.Canvas{Width: req.Width, Height: req.Height},
		SlideWidthEMU:  req.SlideWidthEMU,
		SlideHeightEMU: req.SlideHeightEMU,
		FPS:            req.FPS,
		Background:     req.Background,
		Fonts:          req.Fonts,
	})
	if err != nil {
		return nil, fmt.Errorf("animation: %w", err)
	}
	inputs := make([]render.PageInput, 0, len(req.Pages))
	for i, p := range req.Pages {
		inputs = append(inputs, render.PageInput{
			Index: i, SlideID: p.SlideID, DurationMs: p.DurationMs,
			BaseImage: p.BaseImage, Elements: p.Elements, Revision: p.Revision,
			SegmentOffsets: p.SegmentOffsets, Shots: p.Shots,
		})
	}
	return compiler.Compile(inputs)
}

// BundleAnimations 把 IR 与原始页图变成成品包的分层 + 镜头素材。
//
// 顺序是关键：**挖空与出层图必须在原始 PNG 上做**。BuildPlate 的填充色取自元素
// 外环的中位数，若先转成 JPEG（或先降采样到长边 1600），取到的颜色会偏，
// 底板与层图之间就会出现一道看不见的接缝——它不是错误，只是「画面有点脏」，
// 而且要逐像素比对才能发现。
//
// 镜头切片（Shot）与分层动画互相独立：没有分层动画的页一样可以切镜头
// （"先全图、后特写" 是最常见用法）。两段分别构建，任一降级都不拖累另一段——
// 镜头裁切失败只丢特写，分层挖空失败只丢层，绝不让整包失败。
//
// 返回与 plan.Pages 同序；某项为空值表示该页没有元素级动画也没有镜头。
//
// 导出而不是留在包内：V13 的成片夹具（cmd/film-baseline）要从**同一份 IR** 派生
// 成品包，若夹具另写一份「IR → share.Clip」的转换，这条判据就变成「两份实现互相
// 对齐」，而它本来的用途正是发现实现之间的分叉。导出后只有一份转换，夹具只负责
// 造输入与落盘。
func BundleAnimations(plan *render.Timeline, pagePNGs [][]byte, logger *slog.Logger) ([]share.PageAnimation, error) {
	if plan == nil {
		return nil, nil
	}
	out := make([]share.PageAnimation, len(plan.Pages))
	for i, page := range plan.Pages {
		// 镜头切片先单独产出（不依赖分层挖空）。降级时镜头缺失，但本页其余内容仍可正常产出。
		var shots []share.ShotAsset
		if len(page.Shots) > 0 {
			if s, ok := buildShotAssets(page, i, pagePNGs, plan.Width, plan.Height, logger); ok {
				shots = s
			}
		}

		if !hasAnimatedLayer(page.Layers) {
			// 无分层动画的页：只有当本页有镜头切片时才落一条（仅 Shots）。
			if len(shots) == 0 {
				continue
			}
			out[i] = share.PageAnimation{Shots: shots}
			continue
		}
		if i >= len(pagePNGs) || len(pagePNGs[i]) == 0 {
			// 页图缺失：分层无从出，但镜头切片仍可尝试（不依赖分层挖空）。
			if len(shots) > 0 {
				out[i] = share.PageAnimation{Shots: shots}
			}
			continue
		}
		base, err := png.Decode(bytes.NewReader(pagePNGs[i]))
		if err != nil {
			if len(shots) > 0 {
				out[i] = share.PageAnimation{Shots: shots}
			}
			continue
		}

		// 转场与层列表无论挖空是否成功都要写：即使这一页退化为整页图，
		// 转场仍然是它的一部分（转场不需要分层素材）。
		anim := share.PageAnimation{
			Transition: share.SlideTransition{Type: page.Transition.Type, DurationMs: page.Transition.DurationMs},
			Focus:      slideFocus(page.Focus),
		}

		plateImg, _ := render.BuildPlate(base, page.Layers, plan.Width, plan.Height)
		assets := render.BuildLayerAssets(base, page.Layers, plan.Width, plan.Height)
		if plateImg == nil || len(assets) == 0 {
			// 挖空不可用（元素几乎占满整页、取不到外环色）：退回整页图 + 无层。
			// 这一页会「不动的入场」——但至少画面是对的，不会出现纯色疤。
			out[i] = share.PageAnimation{Transition: anim.Transition, Focus: anim.Focus}
		} else {
			platePNG, err := encodePNG(plateImg)
			if err != nil {
				return nil, fmt.Errorf("animation: encode plate for page %d: %w", i, err)
			}
			anim.PlatePNG = platePNG

			layers := make([]share.LayerAsset, 0, len(assets))
			for _, a := range assets {
				if a.Index < 0 || a.Index >= len(page.Layers) {
					continue
				}
				layer := page.Layers[a.Index]
				layerPNG, err := encodePNG(a.Image)
				if err != nil {
					return nil, fmt.Errorf("animation: encode layer %s on page %d: %w", a.ElementID, i, err)
				}
				layers = append(layers, share.LayerAsset{
					ElementID: layer.ElementID,
					Kind:      layer.Kind,
					Box:       share.SlideBox{X: layer.Box.X, Y: layer.Box.Y, W: layer.Box.W, H: layer.Box.H},
					PNG:       layerPNG,
					Origin:    layer.Origin,
					Clips:     bundleClips(layer.Clips),
				})
			}
			anim.Layers = layers
			out[i] = anim
		}

		// 分层页上的镜头切片：并入同一份条目。
		if len(shots) > 0 {
			merged := out[i]
			merged.Shots = shots
			out[i] = merged
		}
	}
	return out, nil
}

// buildShotAssets 为一页产出镜头素材。
//
// 首镜（index 0）就是整页，PNG 留空——打包期会复用已写进包的页级图（有分层时是底板）。
// 其余镜头按 Crop 从原始页图裁出特写；任何一步失败返回 (nil, false) 降级：
// 这一页的镜头切片整体缺失，但不拖累整包（镜头是增强层）。
//
// crop 在 page.Shots 里是**画布像素**坐标（编译器用 c.scale 统一换算），而落盘字节是
// **源图**分辨率；两者差一个 sx/sy 倍率，裁切前必须映射回源像素坐标，
// 否则裁出来的区域会与画面错位（错位不会报错，只会特写"对不上"）。
func buildShotAssets(page render.Page, i int, pagePNGs [][]byte, canvasW, canvasH int, logger *slog.Logger) ([]share.ShotAsset, bool) {
	if canvasW <= 0 || canvasH <= 0 {
		return nil, false
	}
	out := make([]share.ShotAsset, 0, len(page.Shots))
	for k, shot := range page.Shots {
		if k == 0 {
			// 首镜复用页级图：PNG 留空，Crop 也留空（整页）。相机镜头首镜 MUST 为空。
			out = append(out, share.ShotAsset{
				StartMs:     shot.StartMs,
				DurationMs:  shot.DurationMs,
				CrossFadeMs: shot.CrossFadeMs,
				Camera:      toShareCamera(shot.Camera),
			})
			continue
		}
		if shot.Crop == nil {
			// 非首镜却无裁切框：规格语义错误，整页镜头降级。
			if logger != nil {
				logger.Warn("animation: non-first shot without crop; dropping page shots",
					"page", i, "shot", k)
			}
			return nil, false
		}
		if i >= len(pagePNGs) || len(pagePNGs[i]) == 0 {
			if logger != nil {
				logger.Warn("animation: page image missing; cannot crop shot",
					"page", i, "shot", k)
			}
			return nil, false
		}
		src, err := png.Decode(bytes.NewReader(pagePNGs[i]))
		if err != nil {
			if logger != nil {
				logger.Warn("animation: decode page png for shot failed",
					"page", i, "shot", k, "error", err)
			}
			return nil, false
		}
		pngBytes, err := cropShot(src, *shot.Crop, canvasW, canvasH)
		if err != nil {
			if logger != nil {
				logger.Warn("animation: crop shot failed; dropping page shots",
					"page", i, "shot", k, "error", err)
			}
			return nil, false
		}
		// Crop 按**画布像素**落清单，与 IR、播放器、清单校验器保持同一个坐标系。
		//
		// 曾经这里按源像素写过（乘了 srcW/canvasW）：页图 1× 时两者相等，所以一直没被
		// 发现；生产页图是 300 DPI（约 2.3×），于是清单里出现一个画布放不下的裁切框——
		// 校验器按画布判越界，打包直接失败；就算校验放行，播放器拿到的也是一个它在
		// 自己坐标系里解释不了的矩形。裁 PNG 用源像素（见 CropShot），落清单用画布像素，
		// 两个坐标系各归各位。
		crop := share.SlideBox{
			X: shot.Crop.X, Y: shot.Crop.Y, W: shot.Crop.W, H: shot.Crop.H,
		}
		out = append(out, share.ShotAsset{
			PNG:         pngBytes,
			Crop:        &crop,
			Camera:      toShareCamera(shot.Camera),
			StartMs:     shot.StartMs,
			DurationMs:  shot.DurationMs,
			CrossFadeMs: shot.CrossFadeMs,
		})
	}
	return out, true
}

// toShareCamera 把渲染层的相机关键帧转成打包层的同构类型（见 animation-camera design.md：
// 相机坐标已是归一化、后端无关，无需换算，仅做跨包类型对齐）。
func toShareCamera(kfs []render.CameraKeyframe) []share.CameraKeyframe {
	if len(kfs) == 0 {
		return nil
	}
	out := make([]share.CameraKeyframe, len(kfs))
	for i, k := range kfs {
		out[i] = share.CameraKeyframe{AtMs: k.AtMs, X: k.X, Y: k.Y, Scale: k.Scale}
	}
	return out
}

// cropShot 从源页图按画布坐标裁切框裁出一张特写 PNG（原生分辨率，不放大）。
//
// 裁切本身在 render.CropShot：渲染服务的 staging 也要从同一张页图裁同一块矩形，
// 两处各写一份的结果不是「两种实现」而是「两张取景不同的图」——那个差异在单端
// 截图里看不出来，只在逐帧比对时表现为「元素飘了」。
func cropShot(src image.Image, crop render.Box, canvasW, canvasH int) ([]byte, error) {
	dst, err := render.CropShot(src, crop, canvasW, canvasH)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, fmt.Errorf("encode cropped shot: %w", err)
	}
	return buf.Bytes(), nil
}

// readPagePNGs 按 key 顺序读页图，返回与 keys 等长的切片。
//
// 单页失败记 nil 而不是整包失败：缺图在 M6-1 里本来就有降级路径（沿用/占位），
// 打包器会处理。返回第一个错误只是为了日志能说清「为什么这页没动画」。
func (h *ExportHandler) readPagePNGs(ctx context.Context, tenantID string, keys []string) ([][]byte, error) {
	out := make([][]byte, len(keys))
	var firstErr error
	for i, key := range keys {
		if key == "" {
			continue
		}
		data, err := h.readTenantObject(ctx, tenantID, key)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out[i] = data
	}
	return out, firstErr
}

// htmlBundleAnimations 产出成品包的分层素材；nil 表示这一包没有动画。
//
// 三个前置与 MP4 通道逐条对齐（`animatedVideo`）：组装器、快照里的规格、时间轴。
// 用同一个组装器是关键——它决定了元素几何、规格版本核对、分段偏移的取法。
// 若这里另写一套，HTML 与 MP4 会用两份不同的「同一份 IR」。
func (h *ExportHandler) htmlBundleAnimations(ctx context.Context, job *pipeline.Job,
	snapshot ExportSnapshot, bundle *TimelineAsset, pagePNGs [][]byte) ([]share.PageAnimation, error) {
	if h.animationPages == nil {
		return nil, nil
	}
	if snapshot.Animation == nil || len(snapshot.Animation.Specs) == 0 {
		return nil, nil
	}
	if bundle == nil || bundle.Timeline == nil || len(bundle.Timeline.Slides) == 0 {
		return nil, nil
	}
	slides := bundle.Timeline.Slides
	if len(pagePNGs) != len(slides) {
		return nil, fmt.Errorf("animation: %d page images for %d slides", len(pagePNGs), len(slides))
	}
	// baseImage 只被渲染器读；包内的图另有路径，这里给一个与包内命名一致的占位值，
	// 让 IR 与包内容在排查时能对上号。
	paths := make([]string, len(slides))
	for i := range paths {
		paths[i] = fmt.Sprintf("slides/%03d.png", i+1)
	}
	req, err := h.animationPages(ctx, AnimationPageInput{
		Snapshot:  snapshot,
		TenantID:  job.TenantID,
		ProjectID: job.ProjectID,
		Slides:    slides,
		PagePNGs:  pagePNGs,
		// HTML 包自带页图：不走资产发布器，也就不用为一次包导出往渲染资产根写一份。
		BaseImagePaths: paths,
	})
	if err != nil {
		return nil, fmt.Errorf("animation: assemble pages: %w", err)
	}
	if len(req.Pages) != len(slides) {
		return nil, fmt.Errorf("animation: assembled %d pages for %d slides", len(req.Pages), len(slides))
	}
	if req.Width == 0 || req.Height == 0 {
		req.Width, req.Height = snapshot.Width, snapshot.Height
	}
	if req.FPS == 0 {
		req.FPS = snapshot.FPS
	}
	plan, err := h.compileAnimationPlan(req)
	if err != nil {
		return nil, err
	}
	// 对带 Crop 的页做 300 DPI 重渲并替换源图，使特写清晰（graceful 降级，见函数）。
	h.rerenderHiResPages(ctx, job, plan, bundle, pagePNGs)
	return BundleAnimations(plan, pagePNGs, h.logger)
}

// rerenderHiResPages 对带 Crop 的页做 300 DPI 重渲，并替换 pagePNGs 中对应页的字节。
//
// 设计 §5：特写清晰与否取决于源图分辨率——150 DPI 源裁切后浏览器放大（模糊），
// 300 DPI 源裁切区恰好等于画布尺寸（零重采样清晰）。整篇只渲一次 300 DPI，
// 仅裁切页替换源图，其余页保持 150 DPI（省体积与渲染时间）。
//
// 渲染器 / 源版本任一不可用，或重渲失败：降级为 150 DPI 模糊路径（镜头仍是增强层，
// 缺失清晰特写不拖累整包）。
func (h *ExportHandler) rerenderHiResPages(ctx context.Context, job *pipeline.Job, plan *render.Timeline, bundle *TimelineAsset, pagePNGs [][]byte) {
	if h.slideRenderer == nil {
		return
	}
	// 找出哪些页需要高清源图（有 k>0 且带 Crop 的镜头）。
	cropPages := make([]int, 0)
	for i, page := range plan.Pages {
		if pageHasCrop(page) {
			cropPages = append(cropPages, i)
		}
	}
	if len(cropPages) == 0 {
		return
	}
	if h.sourceRevisions == nil {
		if h.logger != nil {
			h.logger.Warn("export: 300dpi re-render skipped: no source revision lookup")
		}
		return
	}
	rev, err := h.sourceRevisions.GetSourceRevision(ctx, job.TenantID, job.ProjectID, bundle.Timeline.SourceRevisionNo)
	if err != nil || rev == nil || rev.ObjectKey == "" {
		if h.logger != nil {
			h.logger.Warn("export: 300dpi re-render skipped: cannot locate source pptx", "error", err)
		}
		return
	}
	src, err := h.readTenantObject(ctx, job.TenantID, rev.ObjectKey)
	if err != nil {
		if h.logger != nil {
			h.logger.Warn("export: 300dpi re-render skipped: read source pptx failed", "error", err)
		}
		return
	}
	result, err := h.slideRenderer.Render(ctx, bytes.NewReader(src), int64(len(src)), sliderenderer.RenderOptions{DPI: 300})
	if err != nil {
		if h.logger != nil {
			h.logger.Warn("export: 300dpi re-render failed; falling back to 150dpi shots", "error", err)
		}
		return
	}
	for _, i := range cropPages {
		if i < len(result.Pages) {
			pagePNGs[i] = result.Pages[i].PNG
		}
	}
	if h.logger != nil {
		h.logger.Info("export: 300dpi re-render applied to cropped pages",
			"pages", cropPages, "source_dpi", result.Report.DPI)
	}
}

// pageHasCrop 报告一页是否含「非首镜且带裁切框」的镜头（即需要高清源图做特写）。
func pageHasCrop(page render.Page) bool {
	for _, s := range page.Shots {
		if s.Index > 0 && s.Crop != nil {
			return true
		}
	}
	return false
}

// hasAnimatedLayer 判断一页是否有元素级动画。
//
// 只看 clips 是否非空，不看 layers 数量：编译器会为每条轨道出一层，
// 但一条轨道可能一个 clip 都没有（例如触发器解析失败后被跳过），
// 那种页挖空出图纯属浪费体积，还多了一批不该动的层。
func hasAnimatedLayer(layers []render.Layer) bool {
	for _, l := range layers {
		if len(l.Clips) > 0 {
			return true
		}
	}
	return false
}

func bundleClips(clips []render.Motion) []share.Clip {
	if len(clips) == 0 {
		return nil
	}
	out := make([]share.Clip, 0, len(clips))
	for _, c := range clips {
		out = append(out, share.Clip{
			ClipID: c.ClipID, Effect: c.Effect,
			StartMs: c.StartMs, DurationMs: c.DurationMs,
			Easing: c.Easing, Props: c.Props,
			IsContinuation:       c.IsContinuation,
			ContinuationOffsetMs: c.ContinuationOffsetMs,
			ElementID:            c.ElementID,
			Carry:                c.Carry,
		})
	}
	return out
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// slideFocus 把 IR 的页级焦点搬到分享清单：两处字段一一对应，但分享包不依赖
// internal/render 的类型（清单是给浏览器看的，IR 是给渲染后端看的）。
func slideFocus(in []render.FocusSpan) []share.SlideFocus {
	if len(in) == 0 {
		return nil
	}
	out := make([]share.SlideFocus, 0, len(in))
	for _, f := range in {
		out = append(out, share.SlideFocus{StartMs: f.StartMs, ElementID: f.ElementID,
			EndMs: f.EndMs, FadeMs: f.FadeMs})
	}
	return out
}
