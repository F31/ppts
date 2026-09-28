package textnorm

import (
	"context"
	"testing"
)

func TestParseLegacyMarkers(t *testing.T) {
	m := ParseLegacyMarkers("A〔读：x〕B‖C")
	if len(m.Readings) != 1 {
		t.Fatalf("readings = %+v", m.Readings)
	}
	rd := m.Readings[0]
	if rd.Outer != (Range{1, 6}) || rd.Inner != (Range{4, 5}) || rd.Text != "x" {
		t.Fatalf("reading = %+v", rd)
	}
	if len(m.Pauses) != 1 || m.Pauses[0] != 7 {
		t.Fatalf("pauses = %v", m.Pauses)
	}
	if m.Display != "AxBC" {
		t.Fatalf("display = %q", m.Display)
	}
	if want := []int{0, 4, 6, 8}; !equalInts(m.SrcIndex, want) {
		t.Fatalf("srcIndex = %v, want %v", m.SrcIndex, want)
	}
}

func TestParseLegacyMarkersUnclosedKeptLiteral(t *testing.T) {
	for _, in := range []string{"〔读：abc", "prefix〔读：x", "test〔读：abc"} {
		m := ParseLegacyMarkers(in)
		if m.Display != in {
			t.Fatalf("ParseLegacyMarkers(%q) display = %q, want identity", in, m.Display)
		}
		if len(m.Readings) != 0 {
			t.Fatalf("ParseLegacyMarkers(%q): unexpected reading %+v", in, m.Readings)
		}
	}
}

func TestLegacyMarkupReadingAndPause(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHDecimalRule()}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{
		Text:         "首都〔读：shǒu dū〕北京‖再见",
		LegacyMarkup: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "首都shǒu dū北京再见" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Controls) != 1 {
		t.Fatalf("controls = %+v", res.Controls)
	}
	// 首都(2) + shǒu dū(7) + 北京(2) = 11 runes before the pause.
	if res.Controls[0].AfterRunes != 11 || res.Controls[0].DurationMS != 300 {
		t.Fatalf("control = %+v", res.Controls[0])
	}
	if len(res.Edits) != 2 {
		t.Fatalf("edits = %+v, want markup.reading + markup.pause", res.Edits)
	}
}

func TestLegacyMarkupOffKeepsMarkersLiteral(t *testing.T) {
	engine, err := Compile(Config{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "A〔读：x〕B‖C"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "A〔读：x〕B‖C" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Controls) != 0 || len(res.Edits) != 0 {
		t.Fatalf("controls=%v edits=%v, want none", res.Controls, res.Edits)
	}
}

func TestLegacyMarkupPauseMSConfig(t *testing.T) {
	engine, err := Compile(Config{PauseMS: 500})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "a‖b", LegacyMarkup: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "ab" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Controls) != 1 || res.Controls[0].AfterRunes != 1 || res.Controls[0].DurationMS != 500 {
		t.Fatalf("controls = %+v", res.Controls)
	}
}

func TestLegacyMarkupReadingBlocksRulesInside(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{ZHDecimalRule()}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "A〔读：3.14〕B", LegacyMarkup: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "A3.14B" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.Edits) != 1 || res.Edits[0].RuleID != "markup.reading" {
		t.Fatalf("edits = %+v, want markup.reading only", res.Edits)
	}
}

func TestDisplayToEffective(t *testing.T) {
	engine, err := Compile(Config{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "A〔读：x〕B", LegacyMarkup: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "AxB" {
		t.Fatalf("text = %q", res.Text)
	}
	got := DisplayToEffective("A〔读：x〕B", res)
	if want := []int{0, 1, 2}; !equalInts(got, want) {
		t.Fatalf("DisplayToEffective = %v, want %v", got, want)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
