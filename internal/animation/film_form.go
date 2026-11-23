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

// 片种来源：batch 是一键/批量套用，page 是逐页编辑器改过片种。
// 空串表示尚未记录（旧行），一键套用可以覆盖。
const (
	FormSourceBatch = "batch"
	FormSourcePage  = "page"
)

// ValidFormSource 允许空串（旧行）以及 batch / page。
func ValidFormSource(src string) bool {
	switch src {
	case "", FormSourceBatch, FormSourcePage:
		return true
	default:
		return false
	}
}

// HasPageFilmForm 表示这一页的片种是逐页改过的，一键套用（overwrite=false）必须跳过。
func HasPageFilmForm(rev *Revision) bool {
	return rev != nil && rev.FormSource == FormSourcePage
}

// ReconcileFormSource 在 PUT 时决定页级片种是否覆盖项目默认。
//
// 片种相对上一版变了 → page，一键套用（overwrite=false）会跳过这一页。
// 片种没变 → 沿用上一版（batch 仍可被一键覆盖）。
// 没有上一版且本版不是默认注视 → 也标 page：用户在逐页编辑器里第一次保存的显现
// 不该被随后的一键注视冲掉。
func ReconcileFormSource(prev, next *Revision) {
	if next == nil {
		return
	}
	nextForm := FilmFormForMotionMode(InferMotionMode(next))
	if prev == nil {
		if nextForm != FilmFormGaze {
			next.FormSource = FormSourcePage
		}
		return
	}
	if FilmFormForMotionMode(InferMotionMode(prev)) != nextForm {
		next.FormSource = FormSourcePage
		return
	}
	next.FormSource = prev.FormSource
}
