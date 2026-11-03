package api

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg" // 页图打包时可能已转 JPEG，解码配置要认
	_ "image/png"
	"math"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

// 成片画布的决定权（ADR-029 ③：竖屏进首版）。
//
// 此前两个导出入口都把画布写死 1920×1080（Connect 入口直接写在快照里，REST 入口
// 在缺省时补 1920×1080），而前端从不传尺寸。后果不是"竖屏不好看"，而是
// **竖屏源页被塞进 16:9 画布**：HTML 成品的舞台按 16:9 摆一张 9:16 的图，
// MP4 通路则会在 render 编译时因比例不符直接失败（ErrAspectMismatch）——
// 同一个功能在两种格式上表现为"变形"和"报错"两种症状。
//
// 决定权归服务端的理由：客户端手里没有页尺寸（PlaybackManifest 只有时间轴与资源
// 地址），而服务端在受理导出时就能读到页图。页图是渲染器对源页的唯一事实
// （150 DPI 位图），它的宽高比就是源页的宽高比。
//
// 为什么只取比例、不直接拿页图像素当画布：16:9 的页图在 150 DPI 下约 2000×1125。
// 直接采用会让存量成片从 1920×1080 变成 2000×1125——编码成本、体积基线、
// 所有既有成片一起变，而用户看不出差别。所以比例取自页图，尺寸按短边 1080 归一：
// 16:9 → 1920×1080（与今天逐像素一致），9:16 → 1080×1920。

const (
	// canvasShortEdge 是归一后的短边。用短边而不是长边：竖屏的"高度"与横屏的"宽度"
	// 才是同一种东西（1080p 的清晰度承诺），按长边归一会让竖屏变成 607×1080。
	canvasShortEdge = 1080
	// canvasAspectTolerance 是显式尺寸与源页比例的容差（相对值）。
	// 0.5% 放过「1920×1080 对 1920.0×1080.06」这类同比例不同取整的写法，
	// 又能拦住 4:3 塞进 16:9（比值差 33%）——后者才是要拦的那类错误。
	canvasAspectTolerance = 0.005
	// canvasMaxEdge 是画布长边上限：拦住"4:1 超宽页"推出 4320 宽的编码请求。
	// 超限时按比例缩到长边 3840（4K），而不是拒绝——用户没做错什么。
	canvasMaxEdge = 3840
)

// resolveCanvas 决定本次导出的画布尺寸。
//
// explicitW/H 为请求值，0 表示未给。sourceAspect 为源页宽高比，0 表示读不到。
func resolveCanvas(explicitW, explicitH int, sourceAspect float64) (int, int, error) {
	if explicitW < 0 || explicitH < 0 {
		return 0, 0, fmt.Errorf("输出尺寸必须为正：%dx%d", explicitW, explicitH)
	}
	if explicitW > 0 && explicitH > 0 {
		if sourceAspect > 0 {
			got := float64(explicitW) / float64(explicitH)
			if math.Abs(got-sourceAspect)/sourceAspect > canvasAspectTolerance {
				return 0, 0, fmt.Errorf(
					"输出尺寸 %dx%d（比例 %.3f）与源页比例 %.3f 不符；"+
						"按源页比例导出，或去掉尺寸让服务端按页图推导",
					explicitW, explicitH, got, sourceAspect)
			}
		}
		return explicitW, explicitH, nil
	}
	// 只给了一边：按源页比例补另一边。拒绝这种请求没有意义——
	// 用户说"我要 1080 宽"，意图明确，缺的那边可以算出来。
	if sourceAspect > 0 {
		switch {
		case explicitW > 0:
			return explicitW, evenInt(math.Round(float64(explicitW) / sourceAspect)), nil
		case explicitH > 0:
			return evenInt(math.Round(float64(explicitH) * sourceAspect)), explicitH, nil
		}
	} else if explicitW > 0 || explicitH > 0 {
		return 0, 0, fmt.Errorf("只给了一边尺寸（%dx%d）且读不到源页比例，无法推导另一边", explicitW, explicitH)
	}
	if sourceAspect <= 0 {
		// 读不到页图（例如字幕导出根本没有页图）：保持既有默认值，
		// 不做"猜一个比例"——猜错会让成片变形，而 16:9 是绝大多数源页。
		return 1920, 1080, nil
	}
	w, h := fitCanvas(sourceAspect)
	return w, h, nil
}

// fitCanvas 把宽高比归一成短边 1080 的偶数尺寸。
func fitCanvas(aspect float64) (int, int) {
	if aspect <= 0 {
		return 1920, 1080
	}
	var w, h int
	if aspect >= 1 {
		h = canvasShortEdge
		w = evenInt(math.Round(float64(canvasShortEdge) * aspect))
	} else {
		w = canvasShortEdge
		h = evenInt(math.Round(float64(canvasShortEdge) / aspect))
	}
	if w > canvasMaxEdge || h > canvasMaxEdge {
		scale := float64(canvasMaxEdge) / float64(max(w, h))
		w = evenInt(math.Round(float64(w) * scale))
		h = evenInt(math.Round(float64(h) * scale))
	}
	return w, h
}

// evenInt 取最近的偶数。
//
// H.264 要求宽高为偶数（yuv420p 的色度采样按 2×2 块）。奇数会让编码器
// 直接失败，而失败点在渲染服务里，用户在导出十分钟后才看到——在这里抹平最省事。
func evenInt(n float64) int {
	v := int(math.Round(n))
	if v%2 != 0 {
		v++
	}
	if v < 2 {
		v = 2
	}
	return v
}

// sourceAspect 读首张可解码页图的宽高比；读不到返回 0。
//
// 只读图头（image.DecodeConfig），不读整张图：页图单张 100–400KB，
// 而这里只需要两个整数。失败不报错——导出能用 16:9 继续，只是画面可能被拉伸，
// 这比"导出按钮直接不可用"轻得多，但失败必须留痕（调用方记日志）。
func sourceAspect(ctx context.Context, objects objectstore.ObjectStore, keys []string) float64 {
	if objects == nil {
		return 0
	}
	for _, raw := range keys {
		key, err := objectstore.Parse(raw)
		if err != nil {
			continue
		}
		rc, _, err := objects.Get(ctx, key)
		if err != nil {
			continue
		}
		cfg, _, err := image.DecodeConfig(rc)
		rc.Close()
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
			continue
		}
		return float64(cfg.Width) / float64(cfg.Height)
	}
	return 0
}

// canvasFor 是"读比例 + 决定尺寸"的组合，两个导出入口共用。
//
// 错误统一转成 Connect 的 InvalidArgument：尺寸与源页不符是调用方的问题，
// 不是服务端故障——用 500 回会让用户以为是我们的 bug，也不会有人去改请求。
func canvasFor(ctx context.Context, objects objectstore.ObjectStore, tenantID string,
	pageKeys []string, explicitW, explicitH int) (int, int, error) {
	aspect := sourceAspect(ctx, objects, pageKeys)
	w, h, err := resolveCanvas(explicitW, explicitH, aspect)
	if err != nil {
		return 0, 0, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return w, h, nil
}
