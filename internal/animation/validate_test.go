package animation

import (
	"errors"
	"strings"
	"testing"
)

func validPageContext() PageContext {
	return PageContext{
		DurationMS: 6000,
		FPS:        30,
		Elements: []Anchor{
			{ID: "shape-1", Kind: KindTitle, HasBounds: true},
			{ID: "shape-2", Kind: KindBullet, HasBounds: true},
		},
		SegmentIDs: []string{"seg-1", "seg-2"},
	}
}

func validRevision() *Revision {
	return &Revision{
		PresetID:  "business-calm",
		Intensity: 1.0,
		Status:    StatusDraft,
		Tracks: []*Track{
			{
				ElementID: "shape-1",
				Kind:      KindTitle,
				Clips: []*Clip{{
					ID: "c1", Type: "slideIn", StartMs: 0, DurationMs: 600, Easing: "easeOut",
					Params: Params{Direction: "up", Offset: 40}, Trigger: "onEnter",
				}},
			},
			{
				ElementID: "shape-2",
				Kind:      KindBullet,
				Clips: []*Clip{{
					ID: "c2", Type: "fadeIn", StartMs: 0, DurationMs: 500, Easing: "easeOut",
					Trigger: "withSegment:seg-1",
				}},
			},
		},
	}
}

func TestValidateAcceptsLegalSpec(t *testing.T) {
	rev := validRevision()
	if err := rev.Validate(validPageContext()); err != nil {
		t.Fatalf("legal spec rejected: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*Revision, *PageContext)
		wantRule   string
		wantStatus int
	}{
		{
			name: "V1 unknown effect",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[0].Clips[0].Type = "explode"
			},
			wantRule: RuleType, wantStatus: 400,
		},
		{
			name: "V1 effect not applicable to kind",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[0].Clips[0].Type = "grow"
			},
			wantRule: RuleType, wantStatus: 400,
		},
		{
			name: "V1 param not declared by effect",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[1].Clips[0].Params.Offset = 20
			},
			wantRule: RuleType, wantStatus: 400,
		},
		{
			name: "V1 param out of range",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[0].Clips[0].Params.Offset = 500
			},
			wantRule: RuleType, wantStatus: 400,
		},
		{
			name: "V2 unknown element",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[0].ElementID = "shape-gone"
			},
			wantRule: RuleAnchor, wantStatus: 400,
		},
		{
			name: "V2 page level track needs page kind",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[0].ElementID = ""
			},
			wantRule: RuleAnchor, wantStatus: 400,
		},
		{
			name: "V3 negative start",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[0].Clips[0].StartMs = -1
			},
			wantRule: RuleTime, wantStatus: 400,
		},
		{
			name: "V3 exceeds page end",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[0].Clips[0].StartMs = 6000
			},
			wantRule: RuleTime, wantStatus: 400,
		},
		{
			name: "V3 unknown page duration",
			mutate: func(_ *Revision, pc *PageContext) {
				pc.DurationMS = 0
			},
			wantRule: RuleTime, wantStatus: 400,
		},
		{
			name: "V4 two entrance clips on one element",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[0].Clips = append(r.Tracks[0].Clips, &Clip{
					ID: "c3", Type: "fadeIn", StartMs: 1000, DurationMs: 400, Easing: "easeOut", Trigger: "onEnter",
				})
			},
			wantRule: RuleOverlap, wantStatus: 400,
		},
		{
			name: "V4 overlapping emphasis clips of same type",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[1].Clips = []*Clip{
					{ID: "c2", Type: "highlight", StartMs: 500, DurationMs: 900, Easing: "easeOut", Trigger: "onEnter"},
					{ID: "c3", Type: "highlight", StartMs: 900, DurationMs: 900, Easing: "easeOut", Trigger: "onEnter"},
				}
			},
			wantRule: RuleOverlap, wantStatus: 400,
		},
		{
			name: "V5 intensity out of range",
			mutate: func(r *Revision, _ *PageContext) {
				r.Intensity = 2.0
			},
			wantRule: RuleIntensity, wantStatus: 400,
		},
		{
			name: "V5 duration below global floor",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[0].Clips[0].DurationMs = 100
			},
			wantRule: RuleIntensity, wantStatus: 400,
		},
		{
			name: "V5 duration outside effect range",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[1].Clips[0].DurationMs = 1200
			},
			wantRule: RuleIntensity, wantStatus: 400,
		},
		{
			name: "V7 stale segment anchor",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[1].Clips[0].Trigger = "withSegment:seg-9"
			},
			wantRule: RuleSegment, wantStatus: 409,
		},
		{
			name: "V8 unknown preset",
			mutate: func(r *Revision, _ *PageContext) {
				r.PresetID = "neon-chaos"
			},
			wantRule: RulePreset, wantStatus: 400,
		},
		{
			name: "V11 too many tracks",
			mutate: func(r *Revision, _ *PageContext) {
				for i := len(r.Tracks); i <= MaxTracksPerSlide; i++ {
					r.Tracks = append(r.Tracks, &Track{ElementID: "shape-1", Kind: KindTitle, Clips: r.Tracks[0].Clips})
				}
			},
			wantRule: RuleScale, wantStatus: 400,
		},
		{
			name: "V11 empty track",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks = append(r.Tracks, &Track{ElementID: "shape-1", Kind: KindTitle})
			},
			wantRule: RuleScale, wantStatus: 400,
		},
		{
			name: "V12 oversized spec",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[0].ElementID = strings.Repeat("x", MaxSpecBytes+1)
			},
			wantRule: RuleBytes, wantStatus: 413,
		},
		{
			name: "V0 null track",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks = append(r.Tracks, nil)
			},
			wantRule: RuleStructure, wantStatus: 400,
		},
		{
			name: "V0 unknown easing",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[0].Clips[0].Easing = "bounce"
			},
			wantRule: RuleStructure, wantStatus: 400,
		},
		{
			name: "V0 malformed trigger",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[0].Clips[0].Trigger = "whenever"
			},
			wantRule: RuleStructure, wantStatus: 400,
		},
		{
			name: "V3 atTime beyond page",
			mutate: func(r *Revision, _ *PageContext) {
				r.Tracks[0].Clips[0].Trigger = "atTime:999999"
			},
			wantRule: RuleTime, wantStatus: 400,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rev := validRevision()
			pc := validPageContext()
			tt.mutate(rev, &pc)
			err := rev.Validate(pc)
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error type = %T, want *ValidationError (%v)", err, err)
			}
			if ve.Rule != tt.wantRule {
				t.Fatalf("rule = %s, want %s (%v)", ve.Rule, tt.wantRule, err)
			}
			if ve.Status != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%v)", ve.Status, tt.wantStatus, err)
			}
			if ve.Message == "" {
				t.Fatal("validation error must carry a message")
			}
		})
	}
}

func TestValidateAcceptsPageLevelTrack(t *testing.T) {
	rev := &Revision{
		Intensity: 1.0,
		Tracks: []*Track{{
			Kind: KindPage,
			Clips: []*Clip{{
				ID: "c1", Type: "fadeIn", DurationMs: 400, Easing: "easeOut", Trigger: "onEnter",
			}},
		}},
	}
	if err := rev.Validate(validPageContext()); err != nil {
		t.Fatalf("page level track rejected: %v", err)
	}
}

func TestCheckWritable(t *testing.T) {
	rev := validRevision()
	rev.Revision = 7

	if err := rev.CheckWritable(7); err != nil {
		t.Fatalf("matching revision rejected: %v", err)
	}

	var conflict *ErrConflict
	if err := rev.CheckWritable(6); !errors.As(err, &conflict) {
		t.Fatalf("stale write: got %v, want *ErrConflict", err)
	} else if conflict.Latest != rev {
		t.Fatal("conflict must carry the latest revision")
	}

	rev.Status = StatusLocked
	if err := rev.CheckWritable(7); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked write: got %v, want ErrLocked", err)
	}
}

func TestUnionLength(t *testing.T) {
	tests := []struct {
		name  string
		spans []span
		want  int64
	}{
		{"empty", nil, 0},
		{"disjoint", []span{{0, 100}, {200, 300}}, 200},
		{"overlap", []span{{0, 200}, {100, 300}}, 300},
		{"nested", []span{{0, 500}, {100, 200}}, 500},
		{"touch", []span{{0, 100}, {100, 200}}, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unionLength(tt.spans); got != tt.want {
				t.Fatalf("unionLength = %d, want %d", got, tt.want)
			}
		})
	}
}
