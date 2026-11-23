package app

import (
	"context"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/beats"
	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/integrations/tts"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/observability"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/render"
)

// PageBeatSheet 从本页绑定元素与时间轴字幕产出拍表。预览走这一条，只跑阶段 1。
//
// Text 一律 DisplayText（SubtitleCue.Text）。禁止改用 SpokenText。
func PageBeatSheet(
	bindElems []bind.Element,
	compileIDs map[string]struct{},
	slide media.SlideCue,
	subtitles []media.SubtitleCue,
	pageDurMs int64,
) beats.Output {
	_, out := producePageBeatSheet(bindElems, compileIDs, slide, subtitles, pageDurMs)
	recordBeatMetrics(out)
	return out
}

func producePageBeatSheet(
	bindElems []bind.Element,
	compileIDs map[string]struct{},
	slide media.SlideCue,
	subtitles []media.SubtitleCue,
	pageDurMs int64,
) (beats.Input, beats.Output) {
	var segs []beats.SpeechSeg
	for _, cue := range subtitles {
		if cue.SlideID != slide.SlideID {
			continue
		}
		start := usToPageMs(cue.StartUS, slide.StartUS)
		end := usToPageMs(cue.EndUS, slide.StartUS)
		if pageDurMs > 0 && end > pageDurMs {
			end = pageDurMs
		}
		segs = append(segs, beats.SpeechSeg{
			ID:      media.UnscopedSegmentID(slide.SlideID, cue.SegmentID),
			Text:    cue.Text,
			StartMs: start,
			EndMs:   end,
			Method:  segmentAlignmentMethod(slide, cue.SegmentID),
			Chars:   charsToPage(cue.Chars, slide.StartUS),
		})
	}
	in := beats.Input{
		Elements:   bindElems,
		CompileIDs: compileIDs,
		Segments:   segs,
	}
	return in, beats.Produce(in)
}

func recordBeatMetrics(out beats.Output) {
	src := out.Sheet.Source
	if src == "" {
		src = animation.BeatSourceFallback
	}
	observability.BeatsSource(src)
	for reason, n := range out.DenyReasons {
		observability.BeatsDenied(reason, n)
	}
}

func directPageBeatSheet(ctx context.Context, in beats.Input, phase1 beats.Output, model bind.ModelCompleter, cache bind.CompletionCache, modelID string, refine bool) beats.Output {
	return beats.Direct(ctx, in, phase1, beats.DirectOptions{
		Model:   model,
		Cache:   cache,
		ModelID: modelID,
		Refine:  refine,
	})
}

func compileIDsOf(elements []render.Element) map[string]struct{} {
	if len(elements) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(elements))
	for _, e := range elements {
		if e.ID != "" {
			out[e.ID] = struct{}{}
		}
	}
	return out
}

func segmentAlignmentMethod(slide media.SlideCue, segmentID string) tts.AlignmentMethod {
	want := media.UnscopedSegmentID(slide.SlideID, segmentID)
	for _, s := range slide.Segments {
		if media.UnscopedSegmentID(slide.SlideID, s.SegmentID) == want {
			return s.AlignmentMethod
		}
	}
	return ""
}

func charsToPage(chars []media.CharCue, slideStartUS int64) []beats.Char {
	if len(chars) == 0 {
		return nil
	}
	out := make([]beats.Char, len(chars))
	for i, c := range chars {
		out[i] = beats.Char{
			StartMs: usToPageMs(c.StartUS, slideStartUS),
			EndMs:   usToPageMs(c.EndUS, slideStartUS),
			Rune:    c.Char,
		}
	}
	return out
}

func usToPageMs(us, slideStartUS int64) int64 {
	ms := (us - slideStartUS) / 1000
	if ms < 0 {
		return 0
	}
	return ms
}

func beatSheetForPage(page *project.Page, elements []render.Element, rev *animation.Revision, slide media.SlideCue, subtitles []media.SubtitleCue, durationMS, slideWEMU, slideHEMU int64) (beats.Input, beats.Output, bool) {
	if rev == nil || page == nil {
		return beats.Input{}, beats.Output{}, false
	}
	if animation.InferMotionMode(rev) == animation.MotionModeEntrance {
		return beats.Input{}, beats.Output{}, false
	}
	in, out := producePageBeatSheet(PageBindElements(page, slideWEMU, slideHEMU), compileIDsOf(elements), slide, subtitles, durationMS)
	return in, out, true
}
