package render

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
)

// rasterSlackPx 是 pdftoppm 相对 RasterSize 允许多吐的像素。
// Office 10" 16:9 @192 的目标是 1920×1080，实测常见 1920×1081；裁掉底/右 1 行，
// 不要把整张图双线性缩 1px（那会让文字发糊，V13 也会把页图和成片对不齐）。
const rasterSlackPx = 2

// cropPNGTo 在源图每边最多比目标大多 rasterSlackPx 时，裁掉多余的底/右边缘。
// 尺寸已吻合、目标为 0、源图更小、或溢出超过 slack 时原样返回。
func cropPNGTo(data []byte, wantW, wantH int) ([]byte, int, int, error) {
	w, h, err := pngSize(data)
	if err != nil {
		return nil, 0, 0, err
	}
	if wantW <= 0 || wantH <= 0 || (w == wantW && h == wantH) {
		return data, w, h, nil
	}
	dw, dh := w-wantW, h-wantH
	if dw < 0 || dh < 0 || dw > rasterSlackPx || dh > rasterSlackPx {
		return data, w, h, nil
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return data, w, h, nil
	}
	dst := image.NewNRGBA(image.Rect(0, 0, wantW, wantH))
	draw.Draw(dst, dst.Bounds(), img, img.Bounds().Min, draw.Src)
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return data, w, h, nil
	}
	return buf.Bytes(), wantW, wantH, nil
}
