package animation

import (
	"encoding/json"
	"testing"
)

func TestEmbeddedCatalogParses(t *testing.T) {
	reg, err := ParseCatalog(catalogJSON)
	if err != nil {
		t.Fatalf("parse embedded catalog: %v", err)
	}
	if got, want := len(reg.Effects()), 9; got != want {
		t.Fatalf("effects = %d, want %d", got, want)
	}
	if _, ok := reg.Effect("tilt"); !ok {
		t.Fatal("catalog missing tilt")
	}
	if got, want := len(reg.Presets()), 4; got != want {
		t.Fatalf("presets = %d, want %d", got, want)
	}
}

func TestCatalogCoversEveryPresetKind(t *testing.T) {
	reg := Catalog()
	for _, p := range reg.Presets() {
		if len(p.Clips[KindPage]) != 0 {
			t.Errorf("preset %q must not emit page fadeIn", p.ID)
		}
		if !p.GazeFollowsSegment {
			t.Errorf("preset %q missing gazeFollowsSegment", p.ID)
		}
		if p.Transition.Type != TransitionFade {
			t.Errorf("preset %q transition = %s, want fade", p.ID, p.Transition.Type)
		}
		for _, kind := range []string{KindTitle, KindBullet, KindImage} {
			if len(p.Clips[kind]) == 0 || p.Clips[kind][0].Type != "spotlight" {
				t.Errorf("preset %q %s clips must be spotlight", p.ID, kind)
			}
			if len(p.EntranceClips[kind]) == 0 {
				t.Errorf("preset %q missing entranceClips for %s", p.ID, kind)
			}
		}
	}
	tech, ok := reg.Preset("tech-dynamic")
	if !ok || !tech.LookCrop {
		t.Fatal("tech-dynamic must set lookCrop")
	}
	biz, ok := reg.Preset("business-calm")
	if !ok || biz.Callout != "off" || biz.Intensity != 1.0 {
		t.Fatalf("business-calm callout/intensity = %q %v", biz.Callout, biz.Intensity)
	}
	for _, p := range reg.Presets() {
		for kind, clips := range p.Clips {
			for _, c := range clips {
				if c.Type == "tilt" {
					t.Errorf("preset %q clips[%s] must not emit tilt", p.ID, kind)
				}
			}
		}
		for kind, clips := range p.EntranceClips {
			for _, c := range clips {
				if c.Type == "tilt" {
					t.Errorf("preset %q entranceClips[%s] must not emit tilt", p.ID, kind)
				}
			}
		}
	}
}

func TestParseCatalogRejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{
			name: "schema mismatch",
			json: `{"schemaVersion":"animation/v2","effects":[],"presets":[]}`,
		},
		{
			name: "duplicate effect",
			json: `{"schemaVersion":"animation/v1","effects":[
				{"id":"fadeIn","class":"entrance","appliesTo":["title"],"durationRange":[200,1000],"defaultDuration":400},
				{"id":"fadeIn","class":"entrance","appliesTo":["title"],"durationRange":[200,1000],"defaultDuration":400}
			],"presets":[]}`,
		},
		{
			name: "unknown class",
			json: `{"schemaVersion":"animation/v1","effects":[
				{"id":"fadeIn","class":"magic","appliesTo":["title"],"durationRange":[200,1000],"defaultDuration":400}
			],"presets":[]}`,
		},
		{
			name: "duration range outside contract",
			json: `{"schemaVersion":"animation/v1","effects":[
				{"id":"fadeIn","class":"entrance","appliesTo":["title"],"durationRange":[50,1000],"defaultDuration":400}
			],"presets":[]}`,
		},
		{
			name: "default duration outside range",
			json: `{"schemaVersion":"animation/v1","effects":[
				{"id":"fadeIn","class":"entrance","appliesTo":["title"],"durationRange":[200,500],"defaultDuration":900}
			],"presets":[]}`,
		},
		{
			name: "preset references unknown effect",
			json: `{"schemaVersion":"animation/v1","effects":[
				{"id":"fadeIn","class":"entrance","appliesTo":["title"],"durationRange":[200,1000],"defaultDuration":400}
			],"presets":[{"id":"p","intensity":1,"staggerMs":0,"clips":{"title":[{"type":"nope","durationMs":400,"easing":"easeOut"}]}}]}`,
		},
		{
			name: "preset uses effect on unsupported kind",
			json: `{"schemaVersion":"animation/v1","effects":[
				{"id":"grow","class":"entrance","appliesTo":["chart"],"durationRange":[400,2000],"defaultDuration":900}
			],"presets":[{"id":"p","intensity":1,"staggerMs":0,"clips":{"title":[{"type":"grow","durationMs":900,"easing":"easeOut"}]}}]}`,
		},
		{
			name: "unknown field",
			json: `{"schemaVersion":"animation/v1","effects":[],"presets":[],"typo":true}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseCatalog([]byte(tt.json)); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestParamsRejectUnknownKeys(t *testing.T) {
	var p Params
	err := json.Unmarshal([]byte(`{"direction":"up","wobble":3}`), &p)
	if err == nil {
		t.Fatal("expected unknown param to be rejected")
	}
}

func TestParamsRoundTrip(t *testing.T) {
	in := Params{Direction: "left", Offset: 48, ScaleFrom: 0.9, Axis: "y", Loop: true}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Params
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("round trip mismatch: %+v != %+v", out, in)
	}
}

func TestCatalogAccessorsAreSorted(t *testing.T) {
	reg := Catalog()
	ids := reg.EffectIDs()
	for i := 1; i < len(ids); i++ {
		if ids[i-1] > ids[i] {
			t.Fatalf("effect ids not sorted: %v", ids)
		}
	}
	presets := reg.Presets()
	for i := 1; i < len(presets); i++ {
		if presets[i-1].ID > presets[i].ID {
			t.Fatalf("presets not sorted: %v / %v", presets[i-1].ID, presets[i].ID)
		}
	}
}
