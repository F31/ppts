package beats

import (
	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/design"
	"github.com/F31/ppts/internal/integrations/tts"
)

const (
	ReasonOK              = "ok"
	ReasonTooFew          = "too_few"
	ReasonStructuredCover = "structured_cover"
	ReasonNoSpeech        = "no_speech"
)

// SpeechSeg 是一页里一段旁白。Text 必须是 DisplayText。
type SpeechSeg struct {
	ID      string
	Text    string // DisplayText = SubtitleCue.Text
	StartMs int64  // 相对本页
	EndMs   int64
	Method  tts.AlignmentMethod
	Chars   []Char // 页内 ms；可空
}

// Input 是确定性生产者的全部输入。同一 Input 字节级相等则 Output 相等。
type Input struct {
	Elements   []bind.Element
	CompileIDs map[string]struct{}
	Segments   []SpeechSeg
	Profile    design.MotionProfile
}

// Output 是一页的拍表。fallback 时 Sheet 置空，DenyIDs 仍交给编译回退。
type Output struct {
	Sheet       animation.BeatSheet
	DenyIDs     map[string]struct{}
	DenyReasons map[string]int // page_number | aux | title
	Reason      string         // ok | too_few | structured_cover | no_speech
	// DirectorFallback 是阶段 2 导演没能换掉阶段 1 回退的原因。空串表示没问模型，
	// 或问了并且这份 Sheet 就是它的产出。
	DirectorFallback string
}

// Produce 按句读 / VAD 停顿切拍，字面匹配形状。不读时钟、不打模型。
func Produce(in Input) Output {
	cands, deny, denyReasons := collectCandidates(in)
	if deny == nil {
		deny = map[string]struct{}{}
	}
	if denyReasons == nil {
		denyReasons = map[string]int{}
	}

	hasSpeech := false
	for _, s := range in.Segments {
		if trimText(s.Text) != "" {
			hasSpeech = true
			break
		}
	}
	if !hasSpeech {
		return Output{
			Sheet:       animation.BeatSheet{Source: animation.BeatSourceFallback},
			DenyIDs:     deny,
			DenyReasons: denyReasons,
			Reason:      ReasonNoSpeech,
		}
	}

	pieces, source := splitAll(in)
	beats := matchPieces(pieces, cands)
	ids := uniqueGazeIDs(beats)

	if structuredCover(in, ids, pieces) {
		return Output{
			Sheet:       animation.BeatSheet{Source: animation.BeatSourceFallback},
			DenyIDs:     deny,
			DenyReasons: denyReasons,
			Reason:      ReasonStructuredCover,
		}
	}
	if len(ids) < 2 {
		return Output{
			Sheet:       animation.BeatSheet{Source: animation.BeatSourceFallback},
			DenyIDs:     deny,
			DenyReasons: denyReasons,
			Reason:      ReasonTooFew,
		}
	}
	return Output{
		Sheet: animation.BeatSheet{
			Source: source,
			Beats:  beats,
		},
		DenyIDs:     deny,
		DenyReasons: denyReasons,
		Reason:      ReasonOK,
	}
}

func structuredCover(in Input, gazeIDs map[string]struct{}, pieces []piece) bool {
	if len(gazeIDs) >= 3 {
		return false
	}
	// 有显式序数切出的多个 clause 时，这不是「一句话概括整页」，而是逐条讲；
	// 哪怕只命中两项也不能按总起页回退。T5b 那种单句擦到两张卡仍走下面的总起判定。
	hard := 0
	for _, p := range pieces {
		if p.NoMerge {
			hard++
		}
	}
	if hard >= 2 && len(gazeIDs) >= 2 {
		return false
	}
	bsegs := make([]bind.Segment, 0, len(in.Segments))
	for _, s := range in.Segments {
		bsegs = append(bsegs, bind.Segment{ID: s.ID, Text: s.Text})
	}
	res := bind.Run(in.Elements, bsegs, bind.Options{})
	if res.Band != bind.BandStructured {
		return false
	}

	pageDur := pageDurationMs(in.Segments)
	var cover *SpeechSeg
	if len(in.Segments) == 1 {
		cover = &in.Segments[0]
	} else {
		for i := range in.Segments {
			s := &in.Segments[i]
			if pageDur > 0 && s.EndMs-s.StartMs >= (pageDur*8)/10 {
				cover = s
				break
			}
		}
	}
	if cover == nil {
		return false
	}

	bound := 0
	for _, b := range res.Bindings {
		if b.SegmentID != cover.ID {
			continue
		}
		bound++
	}
	if bound < 3 {
		return false
	}
	return len(gazeIDs) < 3
}

func pageDurationMs(segs []SpeechSeg) int64 {
	var maxEnd int64
	for _, s := range segs {
		if s.EndMs > maxEnd {
			maxEnd = s.EndMs
		}
	}
	return maxEnd
}
