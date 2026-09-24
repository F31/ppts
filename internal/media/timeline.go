package media

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/F31/ppts/internal/integrations/tts"
)

const TimelineSchemaVersion = "1.0"

// Timing defines the non-overlapping page timing preset.
type Timing struct {
	LeadInMS   int64 `json:"leadInMs"`
	GapMS      int64 `json:"gapMs"`
	TailHoldMS int64 `json:"tailHoldMs"`
}

// SegmentInput is one synthesized segment used to build a timeline.
type SegmentInput struct {
	SegmentID   string
	DisplayText string
	AudioKey    string
	DurationMS  int64
	Alignment   *tts.Alignment
}

// SlideInput preserves the caller-provided page order.
type SlideInput struct {
	SlideID  string
	Segments []SegmentInput
}

// Timeline is the sole playback and export clock model.
type Timeline struct {
	SchemaVersion string        `json:"schemaVersion"`
	DurationUS    int64         `json:"durationUs"`
	Slides        []SlideCue    `json:"slides"`
	Subtitles     []SubtitleCue `json:"subtitles"`
	// SourceRevisionNo 是该时间轴所源自的源版本号（配音导出时由 narration 写入，见 internal/app/narration.go），
	// 供成品库按"PPT 名称 + 版本"精确展示，避免依赖成品行到 source_revisions 缺失的外键。
	// 0 表示未知（历史时间轴 / 测试直接构造的时间轴）。
	SourceRevisionNo int `json:"sourceRevisionNo,omitempty"`
	// SourceDisplayName 是该源版本的展示名（source_revisions.display_name，用户未设置时为空），
	// 与 SourceRevisionNo 一并供成品库展示"PPT 名称 vN"。空时前端回退到项目名称或默认标签。
	SourceDisplayName string `json:"sourceDisplayName,omitempty"`
}

// SlideCue identifies the exact interval in which one page is visible.
type SlideCue struct {
	SlideID  string       `json:"slideId"`
	StartUS  int64        `json:"startUs"`
	EndUS    int64        `json:"endUs"`
	Segments []SegmentCue `json:"segments"`
}

// SegmentCue locates one audio asset on the global clock.
type SegmentCue struct {
	SegmentID       string              `json:"segmentId"`
	AudioKey        string              `json:"audioKey"`
	StartUS         int64               `json:"startUs"`
	EndUS           int64               `json:"endUs"`
	AlignmentMethod tts.AlignmentMethod `json:"alignmentMethod,omitempty"`
}

// SubtitleCue is shared by WebVTT and SRT renderers.
type SubtitleCue struct {
	SlideID   string `json:"slideId"`
	SegmentID string `json:"segmentId"`
	StartUS   int64  `json:"startUs"`
	EndUS     int64  `json:"endUs"`
	Text      string `json:"text"`
	// Chars 是可选字符级时间戳（相对全局时钟），供播放器按朗读位置逐行轮换 + 高亮。
	// SRT/VTT 渲染只消费 Text，忽略此字段以保持导出格式稳定。
	Chars []CharCue `json:"chars,omitempty"`
}

// CharCue 定位单个字符（或词）在全局时钟上的朗读区间。
type CharCue struct {
	StartUS int64  `json:"startUs"`
	EndUS   int64  `json:"endUs"`
	Char    string `json:"char"`
}

// BuildTimeline applies D_page = lead_in + sum(audio + gap) + tail_hold.
func BuildTimeline(slides []SlideInput, timing Timing) (*Timeline, error) {
	if len(slides) == 0 {
		return nil, errors.New("media: timeline requires at least one slide")
	}
	if timing.LeadInMS < 0 || timing.GapMS < 0 || timing.TailHoldMS < 0 {
		return nil, errors.New("media: timing values cannot be negative")
	}
	leadUS, err := millisecondsToUS(timing.LeadInMS)
	if err != nil {
		return nil, err
	}
	gapUS, err := millisecondsToUS(timing.GapMS)
	if err != nil {
		return nil, err
	}
	tailUS, err := millisecondsToUS(timing.TailHoldMS)
	if err != nil {
		return nil, err
	}

	timeline := &Timeline{SchemaVersion: TimelineSchemaVersion}
	seenSlides := make(map[string]struct{}, len(slides))
	seenSegments := make(map[string]struct{})
	var cursor int64
	for _, inputSlide := range slides {
		if strings.TrimSpace(inputSlide.SlideID) == "" || len(inputSlide.Segments) == 0 {
			return nil, errors.New("media: every slide requires an id and at least one segment")
		}
		if _, exists := seenSlides[inputSlide.SlideID]; exists {
			return nil, fmt.Errorf("media: duplicate slide id %q", inputSlide.SlideID)
		}
		seenSlides[inputSlide.SlideID] = struct{}{}
		slide := SlideCue{SlideID: inputSlide.SlideID, StartUS: cursor}
		cursor, err = addUS(cursor, leadUS)
		if err != nil {
			return nil, err
		}
		for _, inputSegment := range inputSlide.Segments {
			if strings.TrimSpace(inputSegment.SegmentID) == "" || strings.TrimSpace(inputSegment.DisplayText) == "" || inputSegment.DurationMS <= 0 {
				return nil, errors.New("media: every segment requires id, display text, and positive duration")
			}
			if _, exists := seenSegments[inputSegment.SegmentID]; exists {
				return nil, fmt.Errorf("media: duplicate segment id %q", inputSegment.SegmentID)
			}
			seenSegments[inputSegment.SegmentID] = struct{}{}
			durationUS, err := millisecondsToUS(inputSegment.DurationMS)
			if err != nil {
				return nil, err
			}
			segmentEnd, err := addUS(cursor, durationUS)
			if err != nil {
				return nil, err
			}
			segment := SegmentCue{
				SegmentID: inputSegment.SegmentID, AudioKey: inputSegment.AudioKey,
				StartUS: cursor, EndUS: segmentEnd,
			}
			cueStart, cueEnd := cursor, segmentEnd
			if inputSegment.Alignment != nil {
				if err := validateAlignment(inputSegment.Alignment, durationUS); err != nil {
					return nil, fmt.Errorf("media: segment %s: %w", inputSegment.SegmentID, err)
				}
				segment.AlignmentMethod = inputSegment.Alignment.Method
				if len(inputSegment.Alignment.Tokens) > 0 {
					cueStart, err = addUS(cursor, inputSegment.Alignment.Tokens[0].StartUS)
					if err != nil {
						return nil, err
					}
					cueEnd, err = addUS(cursor, inputSegment.Alignment.Tokens[len(inputSegment.Alignment.Tokens)-1].EndUS)
					if err != nil {
						return nil, err
					}
				}
			}
			slide.Segments = append(slide.Segments, segment)
			var chars []CharCue
			if inputSegment.Alignment != nil && len(inputSegment.Alignment.Tokens) > 0 {
				chars = make([]CharCue, 0, len(inputSegment.Alignment.Tokens))
				for _, token := range inputSegment.Alignment.Tokens {
					tokenStart, err := addUS(cursor, token.StartUS)
					if err != nil {
						return nil, err
					}
					tokenEnd, err := addUS(cursor, token.EndUS)
					if err != nil {
						return nil, err
					}
					chars = append(chars, CharCue{StartUS: tokenStart, EndUS: tokenEnd, Char: token.Char})
				}
			}
			// 显示文本与朗读文本（发音词典替换后）字数可能不同；按比例把字级时间戳重映射到
			// 显示文本，保证高亮下标与渲染文本一一对应（否则会系统性错位）。
			if len(chars) > 0 {
				if runes := []rune(inputSegment.DisplayText); len(runes) != len(chars) {
					chars = remapCharsToCount(chars, runes)
				}
			}
			timeline.Subtitles = append(timeline.Subtitles, SubtitleCue{
				SlideID: inputSlide.SlideID, SegmentID: inputSegment.SegmentID,
				StartUS: cueStart, EndUS: cueEnd, Text: inputSegment.DisplayText, Chars: chars,
			})
			cursor, err = addUS(segmentEnd, gapUS)
			if err != nil {
				return nil, err
			}
		}
		cursor, err = addUS(cursor, tailUS)
		if err != nil {
			return nil, err
		}
		slide.EndUS = cursor
		timeline.Slides = append(timeline.Slides, slide)
	}
	timeline.DurationUS = cursor
	return timeline, nil
}

func validateAlignment(alignment *tts.Alignment, durationUS int64) error {
	var previousEnd int64
	for _, token := range alignment.Tokens {
		if token.StartUS < previousEnd || token.EndUS <= token.StartUS || token.EndUS > durationUS {
			return errors.New("invalid alignment offsets")
		}
		previousEnd = token.EndUS
	}
	return nil
}

func millisecondsToUS(milliseconds int64) (int64, error) {
	if milliseconds > math.MaxInt64/1000 {
		return 0, errors.New("media: timeline duration overflow")
	}
	return milliseconds * 1000, nil
}

func addUS(a, b int64) (int64, error) {
	if b < 0 || a > math.MaxInt64-b {
		return 0, errors.New("media: timeline duration overflow")
	}
	return a + b, nil
}

// remapCharsToCount 把 M 个字级时间戳按比例重映射到 N 个显示字符（M≠N，通常因发音词典
// 把朗读文本替换成了字数不同的文本）。用「边界时间插值」生成 N 个时间单调不减、整体落在
// [首字起点, 末字终点] 的 CharCue，供播放器按显示文本高亮，避免下标错位。
func remapCharsToCount(chars []CharCue, runes []rune) []CharCue {
	m := len(chars)
	n := len(runes)
	if m == 0 || n == 0 {
		return nil
	}
	if m == n {
		return chars
	}
	// b[0..m]：b[j]=chars[j].StartUS (j<m)，b[m]=chars[m-1].EndUS（单调不减）。
	b := make([]int64, m+1)
	for j := 0; j < m; j++ {
		b[j] = chars[j].StartUS
	}
	b[m] = chars[m-1].EndUS
	at := func(x float64) int64 {
		if x <= 0 {
			return b[0]
		}
		if x >= float64(m) {
			return b[m]
		}
		j := int(x)
		frac := x - float64(j)
		return b[j] + int64(math.Round(frac*float64(b[j+1]-b[j])))
	}
	out := make([]CharCue, n)
	prev := b[0]
	for i := 0; i < n; i++ {
		start := at(float64(i) * float64(m) / float64(n))
		end := at(float64(i+1) * float64(m) / float64(n))
		if start < prev {
			start = prev
		}
		if end <= start {
			end = start + 1
		}
		if end > b[m] {
			end = b[m]
			if end <= start {
				end = start
			}
		}
		out[i] = CharCue{StartUS: start, EndUS: end, Char: string(runes[i])}
		prev = end
	}
	return out
}
