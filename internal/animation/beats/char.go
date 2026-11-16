package beats

import (
	"github.com/F31/ppts/internal/integrations/tts"
)

// Char 是页内毫秒的字级时刻。不要复用 media.CharCue（那是全局微秒）。
type Char struct {
	StartMs int64
	EndMs   int64
	Rune    string
}

func charsUsable(seg SpeechSeg) bool {
	switch seg.Method {
	case tts.AlignEstimateVAD, tts.AlignProvider, tts.AlignForced:
	default:
		return false
	}
	return len(seg.Chars) == len([]rune(seg.Text))
}
