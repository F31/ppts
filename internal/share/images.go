package share

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png" // 注册 PNG 解码器（image.Decode 依赖 init 注册）
	"math"
)

const (
	// MaxPageLongEdge 是页面底图输出长边上限（design D5）。
	MaxPageLongEdge = 1600
	// PageJPEGQuality 是底图 JPEG 质量上限。
	PageJPEGQuality = 82
)

// PageImageEncoder 把**已经缩放好**的一页像素压成包内页图。
//
// 接口只负责编码，不负责取多大：长边上限是打包策略（MaxPageLongEdge），
// 编码器只回答「这堆像素压成什么格式、多少字节」。返回的 ext 是**实际**扩展名
// （.jpg / .webp），清单按它写文件名——写死扩展名的现场是「清单说 .jpg，包里是 .webp」。
//
// 实现放在 media（外部 ffmpeg），接口定义在这里（使用方）：share 是纯装配，
// 它不该知道图像是用什么编的；隐私模式（用户机器上可能没装 ffmpeg）与云端共用
// 同一条装配路径，差别只在「有没有注入编码器」。
type PageImageEncoder interface {
	EncodePage(ctx context.Context, img *image.NRGBA) ([]byte, string, error)
}

// jpegPageEncoder 是内置编码器，也是所有降级路径的终点。
type jpegPageEncoder struct{}

func (jpegPageEncoder) EncodePage(_ context.Context, img *image.NRGBA) ([]byte, string, error) {
	if img == nil || img.Rect.Dx() <= 0 || img.Rect.Dy() <= 0 {
		return nil, "", errors.New("share: empty page image")
	}
	var buf bytes.Buffer
	// 单页 1600×900 的 JPEG 约 100–400KB（真实 PPT 页面），预留容量避免多次扩容。
	buf.Grow(img.Rect.Dx() * img.Rect.Dy() / 4)
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: PageJPEGQuality}); err != nil {
		return nil, "", fmt.Errorf("share: encode page jpeg: %w", err)
	}
	return buf.Bytes(), ".jpg", nil
}

// EncodePage 把页面底图转成 JPEG（长边 ≤ MaxPageLongEdge）。
//
// 返回编码后的字节与**实际**扩展名：解码失败时调用方回退原始字节，扩展名随之为 .png，
// 清单必须记录真实文件名，否则解压后 404（这是"转码失败不阻断导出"的代价，必须显式处理）。
//
// 这是 JPEG 基线路径；需要 WebP 时用 BundleBuilder.SetPageImageEncoder 注入编码器
// （ADR-034）。保留这个函数是因为它同时被 CLI 与测试当作「不开任何优化的产物」。
func EncodePage(data []byte, longEdge int) ([]byte, string, error) {
	img, err := preparePage(data, longEdge)
	if err != nil {
		return nil, "", err
	}
	return jpegPageEncoder{}.EncodePage(context.Background(), img)
}

// preparePage 解码并按长边缩放，得到两条编码路径共用的同一份像素。
//
// 共用这一步是刻意的：JPEG 与 WebP 必须吃**完全相同**的源，否则「换了编码器
// 省了多少」里会混进缩放算法的差异（实验期就踩过一次：一边吃原图、一边吃缩图，
// 得出"ffmpeg 大 76%"的假结论）。
func preparePage(data []byte, longEdge int) (*image.NRGBA, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("share: empty page image")
	}
	if longEdge <= 0 {
		longEdge = MaxPageLongEdge
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("share: decode page image: %w", err)
	}
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("share: page image has invalid size %dx%d", width, height)
	}
	targetW, targetH := fitLongEdge(width, height, longEdge)
	nrgba := toNRGBA(src)
	if targetW != width || targetH != height {
		nrgba = resizeBilinear(nrgba, targetW, targetH)
	}
	return nrgba, nil
}

// PlaceholderPage 生成与 MP4 路径一致的深色占位页（首屏缺图时使用），输出 JPEG。
//
// 占位页是纯色，WebP 对它没有优势（纯色 JPEG 已经极小），因此固定走 JPEG：
// 拿不到页图时最不该做的事，是多一次可能失败的编码。
func PlaceholderPage(width, height int) []byte {
	if width <= 0 {
		width = 1920
	}
	if height <= 0 {
		height = 1080
	}
	targetW, targetH := fitLongEdge(width, height, MaxPageLongEdge)
	img := image.NewNRGBA(image.Rect(0, 0, targetW, targetH))
	c := color.NRGBA{R: 24, G: 26, B: 32, A: 255}
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i] = c.R
		img.Pix[i+1] = c.G
		img.Pix[i+2] = c.B
		img.Pix[i+3] = c.A
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: PageJPEGQuality}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// fitLongEdge 按长边约束等比缩放，宽高比误差不超过 1px。
func fitLongEdge(width, height, longEdge int) (int, int) {
	longest := width
	if height > longest {
		longest = height
	}
	if longest <= longEdge {
		return width, height
	}
	scale := float64(longEdge) / float64(longest)
	targetW := int(math.Round(float64(width) * scale))
	targetH := int(math.Round(float64(height) * scale))
	if targetW < 1 {
		targetW = 1
	}
	if targetH < 1 {
		targetH = 1
	}
	return targetW, targetH
}

// toNRGBA 用标准库 draw 做一次颜色模型归一（比逐像素 At() 快一个量级），
// 之后缩放只读 Pix，避免每次采样都走接口调用。
func toNRGBA(src image.Image) *image.NRGBA {
	if nrgba, ok := src.(*image.NRGBA); ok && nrgba.Rect.Min == (image.Point{}) {
		return nrgba
	}
	bounds := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), src, bounds.Min, draw.Src)
	return dst
}

// resizeBilinear 双线性重采样。页面底图是文字与色块，缩图后再编码，
// 双线性的轻微软化在 1600 长边下不可辨；不引入 x/image 依赖（design D5）。
func resizeBilinear(src *image.NRGBA, width, height int) *image.NRGBA {
	srcW, srcH := src.Rect.Dx(), src.Rect.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, width, height))
	scaleX := float64(srcW) / float64(width)
	scaleY := float64(srcH) / float64(height)
	for y := 0; y < height; y++ {
		fy := (float64(y)+0.5)*scaleY - 0.5
		y0 := int(math.Floor(fy))
		wy := fy - float64(y0)
		if y0 < 0 {
			y0, wy = 0, 0
		}
		y1 := y0 + 1
		if y1 >= srcH {
			y1 = srcH - 1
		}
		if wy < 0 {
			wy = 0
		}
		if y0 >= srcH {
			y0 = srcH - 1
		}
		row0 := y0 * src.Stride
		row1 := y1 * src.Stride
		dstRow := y * dst.Stride
		for x := 0; x < width; x++ {
			fx := (float64(x)+0.5)*scaleX - 0.5
			x0 := int(math.Floor(fx))
			wx := fx - float64(x0)
			if x0 < 0 {
				x0, wx = 0, 0
			}
			x1 := x0 + 1
			if x1 >= srcW {
				x1 = srcW - 1
			}
			if wx < 0 {
				wx = 0
			}
			if x0 >= srcW {
				x0 = srcW - 1
			}
			p00 := row0 + x0*4
			p01 := row0 + x1*4
			p10 := row1 + x0*4
			p11 := row1 + x1*4
			d := dstRow + x*4
			for c := 0; c < 4; c++ {
				top := float64(src.Pix[p00+c])*(1-wx) + float64(src.Pix[p01+c])*wx
				bottom := float64(src.Pix[p10+c])*(1-wx) + float64(src.Pix[p11+c])*wx
				dst.Pix[d+c] = uint8(math.Round(top*(1-wy) + bottom*wy))
			}
		}
	}
	return dst
}
