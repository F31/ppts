package animation

// 运动模式：决定编译走注视、入场，还是两者都跑。
const (
	MotionModeGaze         = "gaze"
	MotionModeEntrance     = "entrance"
	MotionModeGazeEntrance = "gaze+entrance"
)

// ValidMotionMode 报告显式写入的模式是否合法。空串表示「未写，由 InferMotionMode 推断」。
func ValidMotionMode(mode string) bool {
	switch mode {
	case MotionModeGaze, MotionModeEntrance, MotionModeGazeEntrance:
		return true
	default:
		return false
	}
}

// IsGazeMode 是注视编译要跑的两种模式。
func IsGazeMode(mode string) bool {
	return mode == MotionModeGaze || mode == MotionModeGazeEntrance
}

// InferMotionMode 给出这一版规格的运动模式。显式值永远赢；空串才推断。
//
// 旧 teaching-clear 只有 fadeIn → entrance（已保存的入场规格再导出仍走入场）。
// 只有 spotlight、或 catalog 标了 gazeFollowsSegment、或什么都没有 → gaze。
func InferMotionMode(rev *Revision) string {
	if rev == nil {
		return MotionModeGaze
	}
	if ValidMotionMode(rev.MotionMode) {
		return rev.MotionMode
	}
	ent := hasEntranceClips(rev)
	spot := hasSpotlightClips(rev)
	if ent && !spot {
		return MotionModeEntrance
	}
	if spot && ent {
		return MotionModeGazeEntrance
	}
	if p, ok := Catalog().Preset(rev.PresetID); ok && p.GazeFollowsSegment {
		return MotionModeGaze
	}
	return MotionModeGaze
}

// IsEntranceEffect 是会编译成图层运动的入场类动效。
func IsEntranceEffect(typ string) bool {
	switch typ {
	case "fadeIn", "slideIn", "pop", "grow":
		return true
	default:
		return false
	}
}

// IsEntrancePunchKind 是「要点依次出现」会追加入场 clip 的元素类别。
//
// 与 web/src/animationState.ts 的 ENTRANCE_PUNCH_KINDS 同口径：title/bullet/shape/icon
// 挖洞显现；chart/table/image 只保留注视，避免整图/整表被挖掉。
func IsEntrancePunchKind(kind string) bool {
	switch kind {
	case KindTitle, KindBullet, KindShape, KindIcon:
		return true
	default:
		return false
	}
}

// IsGazeMarker 是注视候选：不成 opacity 层，只用来铺 Focus.rect。
func IsGazeMarker(typ string) bool {
	return typ == "spotlight" || typ == "highlight"
}

func hasEntranceClips(rev *Revision) bool {
	if rev == nil {
		return false
	}
	for _, t := range rev.Tracks {
		if t == nil {
			continue
		}
		for _, c := range t.Clips {
			if c != nil && IsEntranceEffect(c.Type) {
				return true
			}
		}
	}
	return false
}

func hasSpotlightClips(rev *Revision) bool {
	if rev == nil {
		return false
	}
	for _, t := range rev.Tracks {
		if t == nil {
			continue
		}
		for _, c := range t.Clips {
			if c != nil && IsGazeMarker(c.Type) {
				return true
			}
		}
	}
	return false
}
