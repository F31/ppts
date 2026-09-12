package media

import (
	"errors"
	"fmt"
	"html"
	"strings"
)

// RenderSRT renders subtitle cues without recalculating their offsets.
func RenderSRT(cues []SubtitleCue) ([]byte, error) {
	if err := validateSubtitleCues(cues); err != nil {
		return nil, err
	}
	var out strings.Builder
	for i, cue := range cues {
		fmt.Fprintf(&out, "%d\r\n%s --> %s\r\n%s\r\n\r\n",
			i+1, formatTimestamp(cue.StartUS, ','), formatTimestamp(cue.EndUS, ','), normalizeCueText(cue.Text))
	}
	return []byte(out.String()), nil
}

// RenderWebVTT renders browser subtitles from the same cue list as SRT.
func RenderWebVTT(cues []SubtitleCue) ([]byte, error) {
	if err := validateSubtitleCues(cues); err != nil {
		return nil, err
	}
	var out strings.Builder
	out.WriteString("WEBVTT\n\n")
	for _, cue := range cues {
		fmt.Fprintf(&out, "%s --> %s\n%s\n\n",
			formatTimestamp(cue.StartUS, '.'), formatTimestamp(cue.EndUS, '.'), html.EscapeString(normalizeCueText(cue.Text)))
	}
	return []byte(out.String()), nil
}

func validateSubtitleCues(cues []SubtitleCue) error {
	var previousEnd int64
	for _, cue := range cues {
		if cue.StartUS < previousEnd || cue.EndUS <= cue.StartUS || cue.StartUS < 0 {
			return errors.New("media: subtitle cues must be positive, ordered, and non-overlapping")
		}
		if cue.EndUS/1000 <= cue.StartUS/1000 {
			return errors.New("media: subtitle cue collapses at millisecond precision")
		}
		if strings.TrimSpace(cue.Text) == "" {
			return errors.New("media: subtitle cue text is empty")
		}
		previousEnd = cue.EndUS
	}
	return nil
}

func formatTimestamp(microseconds int64, millisecondSeparator byte) string {
	totalMS := microseconds / 1000
	hours := totalMS / 3_600_000
	minutes := totalMS / 60_000 % 60
	seconds := totalMS / 1000 % 60
	milliseconds := totalMS % 1000
	return fmt.Sprintf("%02d:%02d:%02d%c%03d", hours, minutes, seconds, millisecondSeparator, milliseconds)
}

func normalizeCueText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimSpace(text)
}
