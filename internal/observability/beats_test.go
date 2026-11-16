package observability

import (
	"expvar"
	"testing"
)

func TestLooksBucket(t *testing.T) {
	if looksBucket(0) != "0" || looksBucket(5) != "5" || looksBucket(6) != "6plus" || looksBucket(12) != "6plus" {
		t.Fatalf("looksBucket 0/5/6/12 = %s %s %s %s", looksBucket(0), looksBucket(5), looksBucket(6), looksBucket(12))
	}
}

func TestBeatsCountersWriteExpvar(t *testing.T) {
	BeatsSource("vad")
	BeatsLooks(6)
	BeatsDenied("page_number", 1)
	if expvar.Get("ppts_animation_beats_total") == nil {
		t.Fatal("missing ppts_animation_beats_total")
	}
	if expvar.Get("ppts_animation_beats_looks_total") == nil {
		t.Fatal("missing ppts_animation_beats_looks_total")
	}
	if expvar.Get("ppts_animation_beats_denied_total") == nil {
		t.Fatal("missing ppts_animation_beats_denied_total")
	}
}
