package animation

import "testing"

func TestStaleSegmentIDs(t *testing.T) {
	rev := &Revision{Tracks: []*Track{
		{ElementID: "a", Clips: []*Clip{{Trigger: "withSegment:seg-old"}}},
		{ElementID: "b", Clips: []*Clip{{Trigger: "withSegment:seg-1"}}},
		{ElementID: "c", Clips: []*Clip{{Trigger: "onEnter"}}},
		{ElementID: "d", Clips: []*Clip{{Trigger: "withSegment:seg-old"}}},
	}}
	got := StaleSegmentIDs(rev, []string{"seg-1", "seg-2"})
	if len(got) != 1 || got[0] != "seg-old" {
		t.Fatalf("got %v, want [seg-old]", got)
	}
	if StaleSegmentIDs(rev, []string{"seg-old", "seg-1"}) != nil {
		t.Fatal("live ids must not be stale")
	}
}
