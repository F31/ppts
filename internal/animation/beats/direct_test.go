package beats

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/integrations/tts"
)

type fixedDirector struct {
	calls int
	fn    func(ctx context.Context, req bind.ModelPrompt) (bind.ModelCompletion, error)
}

func (f *fixedDirector) Complete(ctx context.Context, req bind.ModelPrompt) (bind.ModelCompletion, error) {
	f.calls++
	return f.fn(ctx, req)
}

type mapDirectorCache struct {
	data map[string]string
	gets int
	puts int
}

func (m *mapDirectorCache) Get(_ context.Context, key string) (string, bool, error) {
	m.gets++
	v, ok := m.data[key]
	return v, ok, nil
}

func (m *mapDirectorCache) Put(_ context.Context, key, text string) error {
	m.puts++
	if m.data == nil {
		m.data = map[string]string{}
	}
	m.data[key] = text
	return nil
}

func coverInput() (Input, Output) {
	cards := sixCards()
	in := Input{
		Elements:   append(append([]bind.Element{}, cards...), pageFooter()),
		CompileIDs: compileIDs(cards...),
		Segments: []SpeechSeg{{
			ID: "s1", Text: "本页展示六个应用方向。",
			StartMs: 0, EndMs: 80000, Method: tts.AlignEstimate,
		}},
	}
	return in, Produce(in)
}

func validDirectorJSON() string {
	return `{"beats":[{"startMs":0,"endMs":4000,"segmentId":"s1","shapeId":"c1","shot":"gaze"},{"startMs":4000,"endMs":0,"segmentId":"s1","shapeId":"c2","shot":"gaze"}]}`
}

func TestDirectSkipsWhenPhase1Valid(t *testing.T) {
	cards := sixCards()
	text := "首先看智慧城市。接着看智能办公。再看智能制造。然后是数字政务。还有智慧医疗。最后是产业升级。"
	in := Input{
		Elements:   cards,
		CompileIDs: compileIDs(cards...),
		Segments: []SpeechSeg{{
			ID: "s1", Text: text, StartMs: 0, EndMs: 60000, Method: tts.AlignEstimate,
		}},
	}
	phase1 := Produce(in)
	if !phase1.Sheet.Valid() {
		t.Fatalf("fixture should be Valid: %s", phase1.Reason)
	}
	f := &fixedDirector{fn: func(context.Context, bind.ModelPrompt) (bind.ModelCompletion, error) {
		t.Fatal("must not call model when Phase 1 is Valid")
		return bind.ModelCompletion{}, nil
	}}
	got := Direct(context.Background(), in, phase1, DirectOptions{Model: f})
	if f.calls != 0 {
		t.Fatalf("calls=%d want 0", f.calls)
	}
	if got.Sheet.Source != phase1.Sheet.Source {
		t.Fatalf("source=%s want %s", got.Sheet.Source, phase1.Sheet.Source)
	}
}

func TestDirectSkipsNoSpeech(t *testing.T) {
	cards := sixCards()
	in := Input{Elements: cards, CompileIDs: compileIDs(cards...)}
	phase1 := Produce(in)
	if phase1.Reason != ReasonNoSpeech {
		t.Fatalf("reason=%s want no_speech", phase1.Reason)
	}
	f := &fixedDirector{fn: func(context.Context, bind.ModelPrompt) (bind.ModelCompletion, error) {
		t.Fatal("must not call model without speech")
		return bind.ModelCompletion{}, nil
	}}
	got := Direct(context.Background(), in, phase1, DirectOptions{Model: f})
	if f.calls != 0 {
		t.Fatalf("calls=%d", f.calls)
	}
	if got.DirectorFallback != DirectorFallbackNoSpeech {
		t.Fatalf("fallback=%s", got.DirectorFallback)
	}
}

func TestDirectNilModelKeepsPhase1(t *testing.T) {
	in, phase1 := coverInput()
	if phase1.Sheet.Valid() {
		t.Fatal("cover fixture should be invalid")
	}
	got := Direct(context.Background(), in, phase1, DirectOptions{})
	if got.DirectorFallback != DirectorFallbackNoModel {
		t.Fatalf("fallback=%s", got.DirectorFallback)
	}
	if got.Sheet.Valid() {
		t.Fatal("nil model must not invent a sheet")
	}
}

func TestDirectRescuesCoverPage(t *testing.T) {
	in, phase1 := coverInput()
	f := &fixedDirector{fn: func(_ context.Context, req bind.ModelPrompt) (bind.ModelCompletion, error) {
		if !strings.Contains(req.System, "shapeId") {
			t.Fatalf("system prompt missing shapeId: %s", req.System)
		}
		if strings.Contains(req.User, "pn") {
			t.Fatalf("prompt leaked denied page number: %s", req.User)
		}
		if !strings.Contains(req.User, "E c1") || !strings.Contains(req.User, "S s1") {
			t.Fatalf("prompt missing candidates/segments: %s", req.User)
		}
		if strings.Contains(strings.ToLower(req.User), "png") || strings.Contains(req.User, "http") {
			t.Fatalf("prompt must not send images or URLs: %s", req.User)
		}
		return bind.ModelCompletion{Text: validDirectorJSON()}, nil
	}}
	got := Direct(context.Background(), in, phase1, DirectOptions{Model: f})
	if f.calls != 1 {
		t.Fatalf("calls=%d want 1", f.calls)
	}
	if got.Reason != ReasonOK || got.Sheet.Source != animation.BeatSourceLLM {
		t.Fatalf("reason=%s source=%s", got.Reason, got.Sheet.Source)
	}
	if !got.Sheet.Valid() {
		t.Fatalf("rescued sheet not Valid: %+v", got.Sheet)
	}
	if got.DirectorFallback != "" {
		t.Fatalf("fallback=%s want empty", got.DirectorFallback)
	}
	if _, ok := got.DenyIDs["pn"]; !ok {
		t.Fatalf("DenyIDs lost page number: %#v", got.DenyIDs)
	}
	ids := gazeIDs(got.Sheet)
	if len(ids) != 2 || ids[0] != "c1" {
		t.Fatalf("gaze ids=%v", ids)
	}
}

func TestDirectRejectsUnknownShape(t *testing.T) {
	in, phase1 := coverInput()
	f := &fixedDirector{fn: func(context.Context, bind.ModelPrompt) (bind.ModelCompletion, error) {
		return bind.ModelCompletion{Text: `{"beats":[{"startMs":0,"endMs":0,"segmentId":"s1","shapeId":"invented","shot":"gaze"},{"startMs":1000,"endMs":0,"segmentId":"s1","shapeId":"c2","shot":"gaze"}]}`}, nil
	}}
	got := Direct(context.Background(), in, phase1, DirectOptions{Model: f})
	if got.Sheet.Valid() || got.DirectorFallback != DirectorFallbackInvalidJSON {
		t.Fatalf("reason=%s fallback=%s sheet=%+v", got.Reason, got.DirectorFallback, got.Sheet)
	}
}

func TestDirectRejectsForbiddenPayload(t *testing.T) {
	in, phase1 := coverInput()
	f := &fixedDirector{fn: func(context.Context, bind.ModelPrompt) (bind.ModelCompletion, error) {
		return bind.ModelCompletion{Text: `{"beats":[{"startMs":0,"endMs":0,"segmentId":"s1","shapeId":"c1","shot":"gaze"},{"startMs":1000,"endMs":0,"segmentId":"s1","shapeId":"c2","shot":"gaze"}],"filter":"ffmpeg -filter_complex https://evil.example/x.jsx"}`}, nil
	}}
	got := Direct(context.Background(), in, phase1, DirectOptions{Model: f})
	if got.Sheet.Valid() || got.DirectorFallback != DirectorFallbackInvalidJSON {
		t.Fatalf("forbidden payload accepted: fallback=%s sheet=%+v", got.DirectorFallback, got.Sheet)
	}
}

func TestDirectRejectsUnknownFields(t *testing.T) {
	in, phase1 := coverInput()
	f := &fixedDirector{fn: func(context.Context, bind.ModelPrompt) (bind.ModelCompletion, error) {
		return bind.ModelCompletion{Text: `{"beats":[{"startMs":0,"endMs":0,"segmentId":"s1","shapeId":"c1","shot":"gaze","jsx":"x"},{"startMs":1000,"endMs":0,"segmentId":"s1","shapeId":"c2","shot":"gaze"}]}`}, nil
	}}
	got := Direct(context.Background(), in, phase1, DirectOptions{Model: f})
	if got.Sheet.Valid() {
		t.Fatal("unknown field must fail closed")
	}
}

func TestDirectRejectsNonIncreasingTimes(t *testing.T) {
	in, phase1 := coverInput()
	f := &fixedDirector{fn: func(context.Context, bind.ModelPrompt) (bind.ModelCompletion, error) {
		return bind.ModelCompletion{Text: `{"beats":[{"startMs":1000,"endMs":0,"segmentId":"s1","shapeId":"c1","shot":"gaze"},{"startMs":1000,"endMs":0,"segmentId":"s1","shapeId":"c2","shot":"gaze"}]}`}, nil
	}}
	got := Direct(context.Background(), in, phase1, DirectOptions{Model: f})
	if got.Sheet.Valid() {
		t.Fatal("equal StartMs must fail")
	}
}

func TestDirectTimeoutKeepsPhase1(t *testing.T) {
	in, phase1 := coverInput()
	f := &fixedDirector{fn: func(ctx context.Context, _ bind.ModelPrompt) (bind.ModelCompletion, error) {
		<-ctx.Done()
		return bind.ModelCompletion{}, ctx.Err()
	}}
	got := Direct(context.Background(), in, phase1, DirectOptions{Model: f, Timeout: 20 * time.Millisecond})
	if got.Sheet.Valid() || got.DirectorFallback != DirectorFallbackTimeout {
		t.Fatalf("fallback=%s valid=%v", got.DirectorFallback, got.Sheet.Valid())
	}
}

func TestDirectErrorKeepsPhase1(t *testing.T) {
	in, phase1 := coverInput()
	f := &fixedDirector{fn: func(context.Context, bind.ModelPrompt) (bind.ModelCompletion, error) {
		return bind.ModelCompletion{}, errors.New("gateway down")
	}}
	got := Direct(context.Background(), in, phase1, DirectOptions{Model: f})
	if got.DirectorFallback != DirectorFallbackError || got.Sheet.Valid() {
		t.Fatalf("fallback=%s valid=%v", got.DirectorFallback, got.Sheet.Valid())
	}
}

func TestDirectCacheHitSkipsModel(t *testing.T) {
	in, phase1 := coverInput()
	cache := &mapDirectorCache{data: map[string]string{}}
	f := &fixedDirector{fn: func(context.Context, bind.ModelPrompt) (bind.ModelCompletion, error) {
		return bind.ModelCompletion{Text: validDirectorJSON()}, nil
	}}
	first := Direct(context.Background(), in, phase1, DirectOptions{Model: f, Cache: cache, ModelID: "m1"})
	if !first.Sheet.Valid() || cache.puts != 1 {
		t.Fatalf("first valid=%v puts=%d", first.Sheet.Valid(), cache.puts)
	}
	f2 := &fixedDirector{fn: func(context.Context, bind.ModelPrompt) (bind.ModelCompletion, error) {
		t.Fatal("cache hit must not call model")
		return bind.ModelCompletion{}, nil
	}}
	second := Direct(context.Background(), in, phase1, DirectOptions{Model: f2, Cache: cache, ModelID: "m1"})
	if f2.calls != 0 || !second.Sheet.Valid() || second.Sheet.Source != animation.BeatSourceLLM {
		t.Fatalf("calls=%d valid=%v source=%s", f2.calls, second.Sheet.Valid(), second.Sheet.Source)
	}
}

func TestParseDirectorRejectsOrdinalWrongSection(t *testing.T) {
	cands := []bind.Element{
		{ID: "sec-1", Kind: "title", Text: "行业趋势", Box: bind.Box{X: 160, Y: 240, W: 560, H: 80}},
		{ID: "sec-2", Kind: "title", Text: "AI 大模型", Box: bind.Box{X: 160, Y: 380, W: 560, H: 80}},
		{ID: "sec-3", Kind: "title", Text: "智能体", Box: bind.Box{X: 160, Y: 520, W: 560, H: 80}},
	}
	segs := []SpeechSeg{{ID: "s1", Text: "第二部分是 AI 大模型，第三部分是智能体。", StartMs: 0, EndMs: 10000}}
	raw := `{"beats":[{"startMs":0,"endMs":0,"segmentId":"s1","shapeId":"sec-3","shot":"gaze"},{"startMs":4000,"endMs":0,"segmentId":"s1","shapeId":"sec-1","shot":"gaze"}]}`
	if _, ok := parseDirectorBeats(raw, cands, segs); ok {
		t.Fatal("第二部分不应被模型指到第三部分")
	}
}

func TestDirectorPromptJSONRoundTrip(t *testing.T) {
	var top llmBeatSheet
	if err := json.Unmarshal([]byte(validDirectorJSON()), &top); err != nil {
		t.Fatal(err)
	}
	if len(top.Beats) != 2 {
		t.Fatalf("beats=%d", len(top.Beats))
	}
}
