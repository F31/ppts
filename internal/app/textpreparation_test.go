package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/F31/ppts/internal/integrations/tts"
)

// This alternative implementation deliberately uses no textnorm types.
type alternateTextPreparation struct {
	result PreparedText
	err    error
}

func (p alternateTextPreparation) Resolve(context.Context, string, string) (TextPreparer, error) {
	return p, p.err
}
func (p alternateTextPreparation) Prepare(context.Context, string, string) (PreparedText, error) {
	return p.result, p.err
}
func (p alternateTextPreparation) Display(string) string { return "显示文本" }

func TestNarrationAcceptsApplicationTextPreparation(t *testing.T) {
	store := &narrationStoreStub{revision: approvedRevision(segmentsWithMarkers()...)}
	h, provider, _, _ := newTextNormHandler(t, store, nil, false, nil)
	h.WithTextPreparation(alternateTextPreparation{result: PreparedText{
		Text: "替代实现的口播文本", Pauses: []tts.Pause{{AfterChars: 2, DurationMS: 300}},
	}})
	if err := h.Handle(context.Background(), narrationJob(t)); err != nil {
		t.Fatal(err)
	}
	if provider.lastReq.Text != "替代实现的口播文本" {
		t.Fatalf("TTS text = %q", provider.lastReq.Text)
	}
	if len(provider.lastReq.SpeechControl.Pauses) != 1 {
		t.Fatal("prepared pauses lost")
	}
}

func TestTextPreparationErrorsAndMapping(t *testing.T) {
	wantErr := errors.New("preparation unavailable")
	store := &narrationStoreStub{revision: approvedRevision(segmentsWithMarkers()...)}
	h, _, _, _ := newTextNormHandler(t, store, nil, false, nil)
	h.WithTextPreparation(alternateTextPreparation{err: wantErr})
	if err := h.Handle(context.Background(), narrationJob(t)); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v", err)
	}
	p := alternateTextPreparation{result: PreparedText{DisplayToSpoken: []int{0, 2, 3}}}
	// The caller requires alignment before using precise mapping.
	if got := buildCharToToken(context.Background(), p, "text", "zh", nil); got != nil {
		t.Fatal("mapping without alignment")
	}
	got := buildCharToToken(context.Background(), p, "text", "zh", &tts.Alignment{Tokens: []tts.TokenOffset{{}}})
	if !reflect.DeepEqual(got, []int{0, 2, 3}) {
		t.Fatal("mapping changed")
	}
}

func TestTextNormAdapterPreparesMarkersAndPropagatesCancellation(t *testing.T) {
	f := AdaptTextNorm(staticTextNorm{meta: &textNormStoreStub{}})
	p, err := f.Resolve(context.Background(), "tenant", "zh")
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.Prepare(context.Background(), "A〔读：乙〕‖B", "zh")
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "A乙B" || len(r.Pauses) != 1 || r.Pauses[0].AfterChars != 2 {
		t.Fatalf("result = %+v", r)
	}
	if p.Display("A〔读：乙〕‖B") != "A乙B" {
		t.Fatal("display marker conversion")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Prepare(ctx, "A", "zh"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}
