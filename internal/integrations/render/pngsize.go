package render

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// pngSignature 是 PNG 文件头。
var pngSignature = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// pngSize 读取 PNG 的像素宽高（IHDR 首部，大端）。用于验证栅格化产物尺寸，
// 供 MP4 合成前的统一分辨率检查（V4.0 §9.2）。
func pngSize(data []byte) (width, height int, err error) {
	if len(data) < 24 || !bytes.Equal(data[:8], pngSignature) {
		return 0, 0, fmt.Errorf("render: not a png")
	}
	// IHDR: 8B 签名 + 4B 长度 + 4B "IHDR" + 4B width + 4B height
	width = int(binary.BigEndian.Uint32(data[16:20]))
	height = int(binary.BigEndian.Uint32(data[20:24]))
	if width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("render: invalid png dimensions %dx%d", width, height)
	}
	return width, height, nil
}
