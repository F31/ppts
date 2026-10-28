// Package design 定义成片的视觉与运动基线：画布栅格、字阶、色板、版式原型、
// 运动质感参数与观感度量（设计方案 §19 批次一 G2/G4/G5）。
//
// 本包不含任何绘制逻辑，也不 import 仓库内其他包：它是渲染器（M2）的输入契约，
// 而不是渲染器的一部分。度量接口刻意接受纯数值事实（PageFacts），避免标准被
// 实现的数据结构绑定。
package design

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// RGB 是 8 位 sRGB 颜色。
type RGB struct {
	R, G, B uint8
}

// Hex 返回 #RRGGBB 形式。
func (c RGB) Hex() string {
	return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
}

// ParseHex 解析 #RGB / #RRGGBB（大小写不限）。MUST NOT 接受缩写以外的格式，
// 因为令牌是人工维护的常量，宽松解析只会掩盖笔误。
func ParseHex(s string) (RGB, error) {
	v := strings.TrimSpace(s)
	if !strings.HasPrefix(v, "#") {
		return RGB{}, fmt.Errorf("design: 颜色必须以 # 开头: %q", s)
	}
	v = v[1:]
	if len(v) == 3 {
		v = string([]byte{v[0], v[0], v[1], v[1], v[2], v[2]})
	}
	if len(v) != 6 {
		return RGB{}, fmt.Errorf("design: 颜色必须是 #RGB 或 #RRGGBB: %q", s)
	}
	n, err := strconv.ParseUint(v, 16, 32)
	if err != nil {
		return RGB{}, fmt.Errorf("design: 颜色含非十六进制字符: %q", s)
	}
	return RGB{R: uint8(n >> 16), G: uint8(n >> 8), B: uint8(n)}, nil
}

// MustParseHex 供包内常量使用；解析失败直接 panic（常量写错属编译期错误）。
func MustParseHex(s string) RGB {
	c, err := ParseHex(s)
	if err != nil {
		panic(err)
	}
	return c
}

// channelLuminance 是 sRGB 相对亮度的单通道线性化（WCAG 2.1 定义）。
func channelLuminance(c uint8) float64 {
	v := float64(c) / 255
	if v <= 0.03928 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// Luminance 返回 sRGB 相对亮度，范围 [0,1]。
func (c RGB) Luminance() float64 {
	return 0.2126*channelLuminance(c.R) + 0.7152*channelLuminance(c.G) + 0.0722*channelLuminance(c.B)
}

// ContrastRatio 返回两色的 WCAG 对比度（1.0–21.0，保留两位小数）。
// 顺序无关：内部按较亮/较暗排序。
func ContrastRatio(a, b RGB) float64 {
	la, lb := a.Luminance(), b.Luminance()
	if la < lb {
		la, lb = lb, la
	}
	ratio := (la + 0.05) / (lb + 0.05)
	return math.Round(ratio*100) / 100
}
