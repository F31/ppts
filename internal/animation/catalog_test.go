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
	if got, want := len(reg.Effects()), 7; got != want {
		t.Fatalf("effects = %d, want %d", got, want)
	}
	if got, want := len(reg.Presets()), 4; got != want {
		t.Fatalf("presets = %d, want %d", got, want)
	}
}

func TestCatalogCoversEveryPresetKind(t *testing.T) {
	reg := Catalog()
	for _, p := range reg.Presets() {
		// 每种预设都必须能处理整页轨道：否则"无可挂载元素"的页面会生成空规格。
		if len(p.Clips[KindPage]) == 0 {
			t.Errorf("preset %q has no page fallback clip", p.ID)
		}
		// 四种常用版式元素都应有默认动效，避免同一预设下部分元素"静止不动"。
		for _, kind := range []string{KindTitle, KindBullet, KindImage} {
			if len(p.Clips[kind]) == 0 {
				t.Errorf("preset %q has no %s clip", p.ID, kind)
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
