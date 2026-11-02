package render

import (
	"fmt"
	"image"
	"image/draw"
	"math"
	"os"
	"path/filepath"
)

// pageHasShots 判断一页是否需要在 staging 里处理镜头。
//
// 判据是「有 shots」而不是「有带 crop 的 shots」：即使全是整页镜（或者数据坏到
// 非首镜没写 crop），首镜的 Image 仍要改指向底板——跳过它，合成器就会先画
// 「元素还在的整页」再让元素从零淡入一遍，观众看到的是重影。
func pageHasShots(page Page) bool {
	return len(page.Shots) > 0
}

// stageShotAssets 把特写镜头按 Crop 从页图裁出来，落到 IR 里声明的相对路径。
//
// 为什么由 staging 派生，而不是让调用方上传：镜头图不是素材，是「页图 + crop」
// 的确定性派生物——与底板、层图同类。让调用方传一份，等于要求它先做一遍本函数
// 该做的事，而两边裁切参数（源图倍率、取整方式）只要有一处不同，特写就会错位；
// 错位不报错，只表现为「元素飘了」。
//
// 失败即硬错误，不降级：staging 面对的是「IR 声明了某个镜头」与「资产目录里有
// 这张页图」两件确定的事，裁不出来说明输入或资产本身有问题（Chrome 崩溃、超时
// 才是瞬时故障，那条路有 report.Degraded）。悄悄退回整页会让这段视频**看起来
// 导出成功**却没有特写，而且片段会被按原计划键写进缓存，把这份降级固化下来。
func stageShotAssets(dir string, page *Page, base image.Image, canvasW, canvasH int) error {
	if len(page.Shots) == 0 {
		return nil
	}
	// 首镜的 Image 在编译期指向 baseImage（那时还没有底板），而 staging 之后页级图
	// 变成了底板。不改的话，合成器会先画「元素还在的整页」再把元素从零淡入一遍——
	// 观众看到的是重影，validateShots 的注释里写的正是这一条。
	if page.PlateImage != "" {
		page.Shots[0].Image = page.PlateImage
	}
	for k := range page.Shots {
		shot := &page.Shots[k]
		if k == 0 {
			continue
		}
		if shot.Crop == nil {
			return fmt.Errorf("shot %d has no crop (a non-first shot must declare one)", k)
		}
		img, err := CropShot(base, *shot.Crop, canvasW, canvasH)
		if err != nil {
			return fmt.Errorf("shot %d: %w", k, err)
		}
		path, err := workPath(dir, shot.Image)
		if err != nil {
			return fmt.Errorf("shot %d image: %w", k, err)
		}
		// 镜头图按 IR 里的相对路径落盘（编译期给的是 slides/NNN-shot-NN.png），
		// 所以 plan.json 与实际文件永远对得上——不需要在 staging 里改 Image 字段。
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return fmt.Errorf("shot %d: prepare dir: %w", k, err)
		}
		if err := writePNGFile(path, img); err != nil {
			return fmt.Errorf("shot %d: %w", k, err)
		}
	}
	return nil
}

// CropShot 从页图按画布坐标裁出一张特写（原生分辨率，不放大）。
//
// 为什么放在 render 而不是各写一份：打包期（app.buildShotAssets）与渲染服务
// staging 都要从**同一张页图**上裁**同一块矩形**。两处各写一份代码，产出就不是
// 「两种实现」而是「两张取景不同的图」——源图缩放比、取整方式、越界处理任一处
// 不同，两端特写就会差几个像素。这个差异在任何单端截图里都看不出来（画面本身
// 是完整的），只有逐帧比对时才表现为「元素飘了」，而那时已经很难判断是哪一端错了。
//
// 裁切是像素对齐的整数矩形，用标准库 image/draw 取子矩形即可，不需要重采样；
// 放大由播放器/合成器按舞台缩放完成（源是 150 DPI 则放大后偏软，300 DPI 则
// 零重采样——这是分辨率问题，不是裁切问题）。
func CropShot(src image.Image, crop Box, canvasW, canvasH int) (*image.RGBA, error) {
	if canvasW <= 0 || canvasH <= 0 {
		return nil, fmt.Errorf("invalid canvas dimensions %dx%d", canvasW, canvasH)
	}
	if crop.W <= 0 || crop.H <= 0 {
		return nil, fmt.Errorf("crop has non-positive size %dx%d", crop.W, crop.H)
	}
	bounds := src.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	if srcW <= 0 || srcH <= 0 {
		return nil, fmt.Errorf("source image has zero dimension")
	}
	// 画布像素坐标 → 源像素坐标。页图可能是 150 DPI（长边 1600）也可能是 300 DPI，
	// 两种都要落在同一块矩形上，所以倍率必须按实际尺寸算，不能写死。
	sx := float64(srcW) / float64(canvasW)
	sy := float64(srcH) / float64(canvasH)
	x := bounds.Min.X + int(math.Round(float64(crop.X)*sx))
	y := bounds.Min.Y + int(math.Round(float64(crop.Y)*sy))
	w := int(math.Round(float64(crop.W) * sx))
	h := int(math.Round(float64(crop.H) * sy))
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("cropped region has non-positive size %dx%d", w, h)
	}
	// 钳制到源图范围内：IR 校验过的 crop 落在页面内，但换算后可能因取整越界一像素，
	// 而 image.Rect 越界不会报错，只会读到图像外的像素（黑色）。
	r := image.Rect(x, y, x+w, y+h)
	if !r.In(bounds) {
		r = r.Intersect(bounds)
		if r.Empty() {
			return nil, fmt.Errorf("cropped region outside source image")
		}
	}
	// 拷进一张恰好大小的 RGBA（顺便统一色彩空间），并且 **Bounds 从 (0,0) 开始**：
	// SubImage 与 NewRGBA(r) 都会保留 r.Min，调用方按 (0,0) 取值就越界拿零值——
	// 那不是报错，是一张全透明的特写。
	dst := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(dst, dst.Bounds(), src, r.Min, draw.Src)
	return dst, nil
}
