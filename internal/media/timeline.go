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
			timeline.Subtitles = append(timeline.Subtitles, SubtitleCue{
				SlideID: inputSlide.SlideID, SegmentID: inputSegment.SegmentID,
				StartUS: cueStart, EndUS: cueEnd, Text: inputSegment.DisplayText,
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
