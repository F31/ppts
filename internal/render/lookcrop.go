package render

import "math"

// LookCrop 外扩与 shared/lookcrop.ts 同一组常数：强度只改 pad，不改假推近。
const (
	lookCropPadDefault = 8
	lookCropPadMin     = 4
	lookCropPadMax     = 16
)

// LookCropPadPx 是科技 punch-in 的裁切外扩（画布像素）。
func LookCropPadPx(intensity float64) int {
	if intensity <= 0 {
		intensity = 1
	}
	pad := math.Round(lookCropPadDefault * intensity)
	if pad < lookCropPadMin {
		pad = lookCropPadMin
	}
	if pad > lookCropPadMax {
		pad = lookCropPadMax
	}
	return int(pad)
}
