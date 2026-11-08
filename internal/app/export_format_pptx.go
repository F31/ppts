package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/F31/ppts/internal/integrations"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/pipeline"
)

// renderAudioPPTX 在源 PPTX 副本上按页嵌入音频并设置自动切页。
// 每页音频由时间轴在该页区间内的片段拼成完整 WAV（含静默间隔），与 MP4/网页播放同源。
func (h *ExportHandler) renderAudioPPTX(ctx context.Context, job *pipeline.Job, bundle *TimelineAsset) ([]byte, error) {
	if h.narrationWriter == nil {
		return nil, errors.New("export job: narrated pptx writer is not configured")
	}
	if h.sourceRevisions == nil {
		return nil, errors.New("export job: source revisions are not available")
	}
	revisionNo := bundle.Timeline.SourceRevisionNo
	if revisionNo <= 0 {
		return nil, errors.New("export job: timeline has no source revision; regenerate narration before exporting narrated pptx")
	}
	rev, err := h.sourceRevisions.GetSourceRevision(ctx, job.TenantID, job.ProjectID, revisionNo)
	if err != nil {
		return nil, fmt.Errorf("export job: load source revision %d: %w", revisionNo, err)
	}
	if rev == nil || rev.ObjectKey == "" {
		return nil, fmt.Errorf("export job: source revision %d has no source object", revisionNo)
	}
	src, err := h.readTenantObject(ctx, job.TenantID, rev.ObjectKey)
	if err != nil {
		return nil, fmt.Errorf("export job: read source pptx: %w", err)
	}
	clips := map[string][]byte{}
	for _, slide := range bundle.Timeline.Slides {
		for _, segment := range slide.Segments {
			if _, ok := clips[segment.AudioKey]; ok {
				continue
			}
			data, err := h.readTenantObject(ctx, job.TenantID, segment.AudioKey)
			if err != nil {
				return nil, err
			}
			clips[segment.AudioKey] = data
		}
	}
	plan := integrations.NarrationPlan{Slides: make([]integrations.SlideNarration, 0, len(bundle.Timeline.Slides))}
	for i, slide := range bundle.Timeline.Slides {
		durationUS := slide.EndUS - slide.StartUS
		if durationUS <= 0 {
			return nil, fmt.Errorf("export job: slide %d has no duration", i)
		}
		trackKey := slide.SlideID
		if trackKey == "" {
			trackKey = fmt.Sprintf("slide-%d", i)
		}
		entry := integrations.SlideNarration{
			Index: i, TrackKey: "narration:" + trackKey,
			Advance: time.Duration(durationUS) * time.Microsecond,
		}
		if len(slide.Segments) > 0 {
			segments := make([]media.SegmentCue, 0, len(slide.Segments))
			for _, segment := range slide.Segments {
				shifted := segment
				shifted.StartUS -= slide.StartUS
				shifted.EndUS -= slide.StartUS
				segments = append(segments, shifted)
			}
			pageTimeline := &media.Timeline{
				DurationUS: durationUS,
				Slides: []media.SlideCue{{
					SlideID: slide.SlideID, StartUS: 0, EndUS: durationUS, Segments: segments,
				}},
			}
			wav, err := media.AssembleTimelineWAV(pageTimeline, clips)
			if err != nil {
				return nil, fmt.Errorf("export job: assemble slide %d audio: %w", i, err)
			}
			entry.Audio, entry.MIME = wav, "audio/wav"
		}
		plan.Slides = append(plan.Slides, entry)
	}
	report, err := h.narrationWriter.Apply(ctx, bytes.NewReader(src), int64(len(src)), plan)
	if err != nil {
		return nil, fmt.Errorf("export job: write narrated pptx: %w", err)
	}
	if len(report.Unsupported) > 0 {
		return nil, fmt.Errorf("export job: narrated pptx writer reported unsupported features: %s", strings.Join(report.Unsupported, ", "))
	}
	if len(report.Output) == 0 {
		return nil, errors.New("export job: narrated pptx writer produced no output")
	}
	return report.Output, nil
}
