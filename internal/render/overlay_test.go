package render

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateOverlayPasses(t *testing.T) {
	p := validPlan()
	p.Pages[0].Overlays = []Overlay{{
		ID: "flow-1", Kind: OverlayDot, StartMs: 1000, DurationMs: 900,
		Points: []OverlayPoint{{X: 100, Y: 200}, {X: 400, Y: 300}},
		Color:  "#FFD166", SizePx: 12, Opacity: 1,
	}}
	if err := p.Validate(); err != nil {
		t.Fatalf("valid overlay rejected: %v", err)
	}
}

func TestValidateOverlayRejectsBadKindAndPoints(t *testing.T) {
	p := validPlan()
	p.Pages[0].Overlays = []Overlay{{
		Kind: "signal", StartMs: 0, DurationMs: 500,
		Points: []OverlayPoint{{X: 10, Y: 10}},
	}}
	if err := p.Validate(); err == nil {
		t.Fatal("unknown kind / short path must fail")
	}
}

func TestOverlayOmitEmptyKeepsLegacyJSON(t *testing.T) {
	p := validPlan().Pages[0]
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "overlays") {
		t.Fatalf("nil overlays must be omitted: %s", raw)
	}
	p.Overlays = []Overlay{{Kind: OverlayLine, StartMs: 0, DurationMs: 500,
		Points: []OverlayPoint{{X: 1, Y: 1}, {X: 2, Y: 2}}}}
	raw, err = json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"overlays"`) {
		t.Fatalf("overlays missing: %s", raw)
	}
}
func TestOverlayNilKeepsPlanHash(t *testing.T) {
	p := validPlan()
	if p.Pages[0].Overlays != nil {
		t.Fatal("fixture must start without overlays")
	}
	h1, err := p.Hash(HashOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p.Pages[0].Overlays = []Overlay{}
	h2, err := p.Hash(HashOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("empty overlays must not change plan hash: %s != %s", h1, h2)
	}
}
