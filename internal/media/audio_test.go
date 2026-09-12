package media

import (
	"encoding/binary"
	"testing"
)

func pcmWAVForTest(sampleRate uint32, samples []int16) []byte {
	dataSize := uint32(len(samples) * 2)
	out := make([]byte, 44+dataSize)
	writeWAVHeader(out[:44], sampleRate, 1, 2, dataSize)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(out[44+i*2:], uint16(sample))
	}
	return out
}

func TestAssembleTimelineWAVPlacesClipsAndSilence(t *testing.T) {
	timeline, err := BuildTimeline([]SlideInput{{
		SlideID: "slide-1",
		Segments: []SegmentInput{
			{SegmentID: "seg-1", DisplayText: "one", AudioKey: "audio-1", DurationMS: 100},
			{SegmentID: "seg-2", DisplayText: "two", AudioKey: "audio-2", DurationMS: 100},
		},
	}}, Timing{LeadInMS: 100, GapMS: 50, TailHoldMS: 100})
	if err != nil {
		t.Fatal(err)
	}
	ones := make([]int16, 100)
	twos := make([]int16, 100)
	for i := range ones {
		ones[i], twos[i] = 1000, -1000
	}
	assembled, err := AssembleTimelineWAV(timeline, map[string][]byte{
		"audio-1": pcmWAVForTest(1000, ones),
		"audio-2": pcmWAVForTest(1000, twos),
	})
	if err != nil {
		t.Fatalf("AssembleTimelineWAV: %v", err)
	}
	wav, err := parsePCM16WAV(assembled)
	if err != nil {
		t.Fatal(err)
	}
	if len(wav.data)/2 != 500 {
		t.Fatalf("frames = %d", len(wav.data)/2)
	}
	assertSampleRange(t, wav.data, 0, 100, 0)
	assertSampleRange(t, wav.data, 100, 200, 1000)
	assertSampleRange(t, wav.data, 200, 250, 0)
	assertSampleRange(t, wav.data, 250, 350, -1000)
	assertSampleRange(t, wav.data, 350, 500, 0)
}

func TestAssembleTimelineWAVRejectsDurationAndFormatMismatch(t *testing.T) {
	timeline, err := BuildTimeline([]SlideInput{{
		SlideID:  "slide-1",
		Segments: []SegmentInput{{SegmentID: "seg-1", DisplayText: "one", AudioKey: "audio-1", DurationMS: 100}},
	}}, Timing{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AssembleTimelineWAV(timeline, map[string][]byte{"audio-1": pcmWAVForTest(1000, make([]int16, 90))}); err == nil {
		t.Fatal("expected duration mismatch")
	}
	if _, err := AssembleTimelineWAV(timeline, map[string][]byte{"audio-1": []byte("not wav")}); err == nil {
		t.Fatal("expected format error")
	}
	overlap := &Timeline{DurationUS: 200_000, Slides: []SlideCue{{
		SlideID: "slide-1", StartUS: 0, EndUS: 200_000,
		Segments: []SegmentCue{
			{SegmentID: "a", AudioKey: "a", StartUS: 0, EndUS: 100_000},
			{SegmentID: "b", AudioKey: "b", StartUS: 50_000, EndUS: 150_000},
		},
	}}}
	clip := pcmWAVForTest(1000, make([]int16, 100))
	if _, err := AssembleTimelineWAV(overlap, map[string][]byte{"a": clip, "b": clip}); err == nil {
		t.Fatal("expected overlapping segment error")
	}
}

func assertSampleRange(t *testing.T, pcm []byte, start, end int, want int16) {
	t.Helper()
	for i := start; i < end; i++ {
		got := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		if got != want {
			t.Fatalf("sample[%d] = %d, want %d", i, got, want)
		}
	}
}
