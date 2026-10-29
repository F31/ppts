package app

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"

	"github.com/F31/ppts/internal/design"
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
			SegmentOffsets: p.SegmentOffsets,
		})
	}
	return compiler.Compile(inputs)
}

// bundleAnimations 把 IR 与原始页图变成成品包的分层素材。
//
// 顺序是关键：**挖空与出层图必须在原始 PNG 上做**。BuildPlate 的填充色取自元素
// 外环的中位数，若先转成 JPEG（或先降采样到长边 1600），取到的颜色会偏，
// 底板与层图之间就会出现一道看不见的接缝——它不是错误，只是「画面有点脏」，
// 而且要逐像素比对才能发现。
//
// 返回与 plan.Pages 同序；某项为空值表示该页没有元素级动画（走原来的整页 JPEG）。
func bundleAnimations(plan *render.Timeline, pagePNGs [][]byte) ([]share.PageAnimation, error) {
	if plan == nil {
		return nil, nil
	}
	out := make([]share.PageAnimation, len(plan.Pages))
	for i, page := range plan.Pages {
		if !hasAnimatedLayer(page.Layers) {
			continue
		}
		if i >= len(pagePNGs) || len(pagePNGs[i]) == 0 {
			// 页图缺失时不能出分层：底板无从生成，层图也无从裁切。
			// 交给 writeSlides 的缺图降级路径（沿用/占位），不要在这里造半个包。
			continue
		}
		base, err := png.Decode(bytes.NewReader(pagePNGs[i]))
		if err != nil {
			continue
		}

		// 转场与层列表无论挖空是否成功都要写：即使这一页退化为整页图，
		// 转场仍然是它的一部分（转场不需要分层素材）。
		anim := share.PageAnimation{
			Transition: share.SlideTransition{Type: page.Transition.Type, DurationMs: page.Transition.DurationMs},
		}

		plateImg, _ := render.BuildPlate(base, page.Layers, plan.Width, plan.Height)
		assets := render.BuildLayerAssets(base, page.Layers, plan.Width, plan.Height)
		if plateImg == nil || len(assets) == 0 {
			// 挖空不可用（元素几乎占满整页、取不到外环色）：退回整页图 + 无层。
			// 这一页会「不动的入场」——但至少画面是对的，不会出现纯色疤。
			out[i] = share.PageAnimation{Transition: anim.Transition}
			continue
		}

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
				Clips:     bundleClips(layer.Clips),
			})
		}
		anim.Layers = layers
		out[i] = anim
	}
	return out, nil
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
	return bundleAnimations(plan, pagePNGs)
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
