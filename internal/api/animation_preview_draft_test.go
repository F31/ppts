package api

import (
	"context"
	"testing"

	"github.com/F31/ppts/internal/animation"
)

func TestPreviewRevisionUsesDraftSpecWithoutReadingStore(t *testing.T) {
	pc := animation.PageContext{
		DurationMS: 6000,
		FPS:        30,
		Elements:   []animation.Anchor{{ID: "shape-1", Kind: "title", HasBounds: true}},
	}
	spec := &animationPutRequest{
		PresetID:  "business-calm",
		Intensity: 1,
		Tracks: []*animation.Track{{
			ElementID: "shape-1",
			Kind:      "title",
			Clips: []*animation.Clip{{
				ID: "c1", Type: "fadeIn", StartMs: 0, DurationMs: 400,
				Easing: "easeOut", Trigger: "onEnter",
			}},
		}},
	}
	rev, err := previewRevision(context.Background(), nil, animation.Key{SlideID: "slide-1"}, pc, spec)
	if err != nil {
		t.Fatalf("draft spec: %v", err)
	}
	if rev.PresetID != "business-calm" || len(rev.Tracks) != 1 {
		t.Fatalf("revision = %+v", rev)
	}
}
