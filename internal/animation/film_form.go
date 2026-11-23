package animation

// 片种：用户按需选的成片方式。口味（预设）和页策略与它分开。
const (
	FilmFormGaze   = "gaze"   // 注视：整页画面，讲到哪条框哪条
	FilmFormReveal = "reveal" // 显现：要点按讲解顺序依次出现
)

// ValidFilmForm 报告片种是否是产品入口允许的值。
func ValidFilmForm(form string) bool {
	switch form {
	case FilmFormGaze, FilmFormReveal:
		return true
	default:
		return false
	}
}

// MotionModeForFilmForm 把片种写成编译用的 MotionMode。
func MotionModeForFilmForm(form string) string {
	if form == FilmFormReveal {
		return MotionModeGazeEntrance
	}
	return MotionModeGaze
}

// FilmFormForMotionMode 从已保存规格反推片种。纯 entrance 也算显现。
func FilmFormForMotionMode(mode string) string {
	switch InferMotionMode(&Revision{MotionMode: mode}) {
	case MotionModeGazeEntrance, MotionModeEntrance:
		return FilmFormReveal
	default:
		return FilmFormGaze
	}
}

// FilmFormWantsEntrance 是显现片种要写入 catalog EntranceClips。
func FilmFormWantsEntrance(form string) bool {
	return form == FilmFormReveal
}

// ResolveFilmForm 以片种为准；空串时回退 withEntrance（旧勾选）。
func ResolveFilmForm(form string, withEntrance bool) (string, bool) {
	if ValidFilmForm(form) {
		return form, FilmFormWantsEntrance(form)
	}
	if withEntrance {
		return FilmFormReveal, true
	}
	return FilmFormGaze, false
}
