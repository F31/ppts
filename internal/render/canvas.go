package render

import (
	"image"
	"image/draw"
	"math"
)

// canvasSlackPx 是页图相对画布允许多出的像素。
// pdftoppm 在 RasterDPI 下常给出 1920×1081，成片画布是 1920×1080；裁掉底/右 1 行，
// 不要双线性缩 1px（文字发糊，LookCrop 的 crop-to-fill 也会漂）。
const canvasSlackPx = 2

// CropSlack 在源图每边最多比画布大多 canvasSlackPx 时，裁掉多余的底/右边缘。
// 不缩放、不补边。对不上这条（已吻合、更小、或溢出更大）时返回 src。
func CropSlack(src image.Image, canvasW, canvasH int) image.Image {
	if src == nil || canvasW <= 0 || canvasH <= 0 {
		return src
	}
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	if sw == canvasW && sh == canvasH {
		return src
	}
	dw, dh := sw-canvasW, sh-canvasH
	if dw < 0 || dh < 0 || dw > canvasSlackPx || dh > canvasSlackPx {
		return src
	}
	return cropTopLeft(src, canvasW, canvasH)
}

// SnapRasterSlack 把 16:9 / 9:16 上多出来的 1–2px 裁掉。
// 预览画布是从页图比例推的：1920×1081 会被算成 1918×1080，先 snap 再推才是 1920×1080。
func SnapRasterSlack(src image.Image) image.Image {
	if src == nil {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	nw, nh := snapAspectSlack(w, h)
	if nw == w && nh == h {
		return src
	}
	return cropTopLeft(src, nw, nh)
}

func snapAspectSlack(w, h int) (int, int) {
	bestW, bestH, bestCut := w, h, canvasSlackPx*2+1
	consider := func(nw, nh int) {
		if nw <= 0 || nh <= 0 || nw > w || nh > h {
			return
		}
		dw, dh := w-nw, h-nh
		if dw > canvasSlackPx || dh > canvasSlackPx {
			return
		}
		cut := dw + dh
		if cut == 0 || cut >= bestCut {
			return
		}
		bestCut = cut
		bestW, bestH = nw, nh
	}
	consider(w, int(math.Round(float64(w)*9/16)))
	consider(int(math.Round(float64(h)*16/9)), h)
	consider(w, int(math.Round(float64(w)*16/9)))
	consider(int(math.Round(float64(h)*9/16)), h)
	return bestW, bestH
}

func cropTopLeft(src image.Image, w, h int) image.Image {
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	return dst
}

// cropStagedPNG 把工作目录里的页图裁到画布。不是 PNG 或无需裁时静默跳过——
// 资产复制用例会放占位字节，这里不能把「不是图」升级成准备失败。
func cropStagedPNG(path string, canvasW, canvasH int) error {
	if canvasW <= 0 || canvasH <= 0 {
		return nil
	}
	img, err := readImage(path)
	if err != nil {
		return nil
	}
	cropped := CropSlack(img, canvasW, canvasH)
	if cropped == img {
		return nil
	}
	return writePNGFile(path, cropped)
}
