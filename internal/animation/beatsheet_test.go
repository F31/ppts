package animation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBeatSheetValidCountsGlobalUniqueIDs(t *testing.T) {
	if (*BeatSheet)(nil).Valid() {
		t.Fatal("nil sheet 不应 Valid")
	}
	if (&BeatSheet{}).Valid() {
		t.Fatal("空 Beats 不应 Valid")
	}
	one := &BeatSheet{Beats: []Beat{
		{ShapeID: "a", Shot: BeatShotGaze},
		{ShapeID: "a", Shot: BeatShotGaze},
	}}
	if one.Valid() {
		t.Fatal("同一 ShapeID 两次仍是 1，不应 Valid")
	}
	aba := &BeatSheet{Beats: []Beat{
		{ShapeID: "a", Shot: BeatShotGaze},
		{ShapeID: "b", Shot: BeatShotGaze},
		{ShapeID: "a", Shot: BeatShotGaze},
	}}
	if !aba.Valid() {
		t.Fatal("[A,B,A] 全局互异 2，应 Valid")
	}
	hold := &BeatSheet{Beats: []Beat{
		{ShapeID: "a", Shot: BeatShotGaze},
		{ShapeID: "b", Shot: BeatShotHold},
	}}
	if hold.Valid() {
		t.Fatal("hold 的 ShapeID 不计入")
	}
	emptyShot := &BeatSheet{Beats: []Beat{
		{ShapeID: "a"},
		{ShapeID: "b"},
	}}
	if !emptyShot.Valid() {
		t.Fatal("空 Shot 视为 gaze")
	}
}
func TestBeatIntentIsOptional(t *testing.T) {
	b := Beat{StartMs: 1000, SegmentID: "s1", ShapeID: "sec-1", Shot: BeatShotGaze, Text: "甲"}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "intent") {
		t.Fatalf("nil intent must be omitted: %s", raw)
	}
	b.Intent = &BeatIntent{MotionIntent: "focus", Confidence: 0.8}
	raw, err = json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"intent"`) {
		t.Fatalf("intent missing: %s", raw)
	}
}
