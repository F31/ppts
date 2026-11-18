package beats

import (
	"strings"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/integrations/tts"
)

var sixCardNames = []string{
	"智慧城市", "智能办公", "智能制造", "数字政务", "智慧医疗", "产业升级",
}

func sixCards() []bind.Element {
	pos := []bind.Box{
		{X: 64, Y: 194, W: 429, H: 232},
		{X: 506, Y: 194, W: 429, H: 232},
		{X: 947, Y: 194, W: 429, H: 232},
		{X: 64, Y: 438, W: 429, H: 232},
		{X: 506, Y: 438, W: 429, H: 232},
		{X: 947, Y: 438, W: 429, H: 232},
	}
	out := make([]bind.Element, len(sixCardNames))
	for i, name := range sixCardNames {
		out[i] = bind.Element{
			ID:   "c" + string(rune('1'+i)),
			Kind: "title",
			Box:  pos[i],
			Text: name,
		}
	}
	return out
}

func pageFooter() bind.Element {
	return bind.Element{
		ID:   "pn",
		Kind: "page_number",
		Text: "2",
		Box:  bind.Box{X: 1720, Y: 990, W: 112, H: 64},
	}
}

func cardMarkers(cards []bind.Element) []bind.Element {
	out := make([]bind.Element, len(cards))
	for i, c := range cards {
		out[i] = bind.Element{
			ID:   "m" + c.ID,
			Kind: "marker",
			Text: []string{"01", "02", "03", "04", "05", "06"}[i],
			Box:  bind.Box{X: c.Box.X - 44, Y: c.Box.Y + 16, W: 40, H: 40},
		}
	}
	return out
}

func compileIDs(els ...bind.Element) map[string]struct{} {
	m := make(map[string]struct{}, len(els))
	for _, e := range els {
		m[e.ID] = struct{}{}
	}
	return m
}

func charsWithGaps(text string, startMs, runeMs int64, gaps map[int]int64) []Char {
	runes := []rune(text)
	out := make([]Char, len(runes))
	t := startMs
	for i, r := range runes {
		out[i] = Char{StartMs: t, EndMs: t + runeMs, Rune: string(r)}
		t += runeMs
		if g, ok := gaps[i]; ok {
			t += g
		}
	}
	return out
}

func gazeIDs(sheet animation.BeatSheet) []string {
	var ids []string
	seen := map[string]struct{}{}
	for _, b := range sheet.Beats {
		if b.ShapeID == "" || b.Shot == animation.BeatShotHold {
			continue
		}
		if _, ok := seen[b.ShapeID]; ok {
			continue
		}
		seen[b.ShapeID] = struct{}{}
		ids = append(ids, b.ShapeID)
	}
	return ids
}

func gazeCount(sheet animation.BeatSheet) int {
	return len(uniqueGazeIDs(sheet.Beats))
}

func TestT1FooterIsDenied(t *testing.T) {
	cards := sixCards()
	els := append(append([]bind.Element{}, cards...), pageFooter())
	text := "首先看智慧城市。接着看智能办公。再看智能制造。然后是数字政务。还有智慧医疗。最后是产业升级。"
	out := Produce(Input{
		Elements:   els,
		CompileIDs: compileIDs(els...),
		Segments: []SpeechSeg{{
			ID:      "s1",
			Text:    text,
			StartMs: 0,
			EndMs:   60000,
			Method:  tts.AlignEstimate,
		}},
	})
	if _, ok := out.DenyIDs["pn"]; !ok {
		t.Fatalf("DenyIDs 应含 pn: %#v", out.DenyIDs)
	}
	for _, b := range out.Sheet.Beats {
		if b.ShapeID == "pn" {
			t.Fatalf("页脚成拍: %+v", b)
		}
	}

	untyped := pageFooter()
	untyped.Kind = "bullet"
	if !pageNumberLook(untyped) {
		t.Fatal("漏标 Kind 时原点谓词应仍认页码")
	}
}

func TestPageTitleIsDeniedCardsRemain(t *testing.T) {
	heading := bind.Element{
		ID: "heading", Kind: "title", Text: "人工智能应用场景",
		Box: bind.Box{X: 96, Y: 48, W: 1440, H: 96},
	}
	cards := sixCards()
	els := append([]bind.Element{heading}, cards...)
	text := "首先看智慧城市。接着看智能办公。再看智能制造。然后是数字政务。还有智慧医疗。最后是产业升级。"
	out := Produce(Input{
		Elements:   els,
		CompileIDs: compileIDs(els...),
		Segments: []SpeechSeg{{
			ID: "s1", Text: text, StartMs: 0, EndMs: 60000, Method: tts.AlignEstimate,
		}},
	})
	if _, ok := out.DenyIDs["heading"]; !ok {
		t.Fatalf("DenyIDs 应含页标题 heading: %#v", out.DenyIDs)
	}
	if out.DenyReasons["title"] != 1 {
		t.Fatalf("title deny = %d, want 1: %#v", out.DenyReasons["title"], out.DenyReasons)
	}
	for _, b := range out.Sheet.Beats {
		if b.ShapeID == "heading" {
			t.Fatalf("页标题成拍: %+v", b)
		}
	}
	if n := gazeCount(out.Sheet); n != 6 {
		t.Fatalf("cards = %d want 6; beats=%v", n, out.Sheet.Beats)
	}
}

func TestT2OfficeCardNotManufacture(t *testing.T) {
	cards := sixCards()
	text := "首先看智慧城市的建设成果。首先看智能办公场景，它覆盖审批与会议。接下来是智能制造产线。数字政务提升了办事效率。智慧医疗改善了问诊体验。产业升级需要数据底座。"
	out := Produce(Input{
		Elements:   append(append([]bind.Element{}, cards...), pageFooter()),
		CompileIDs: compileIDs(cards...),
		Segments: []SpeechSeg{{
			ID:      "s1",
			Text:    text,
			StartMs: 0,
			EndMs:   60000,
			Method:  tts.AlignEstimate,
		}},
	})
	if out.Reason != ReasonOK {
		t.Fatalf("reason=%s source=%s", out.Reason, out.Sheet.Source)
	}
	if n := gazeCount(out.Sheet); n != 6 {
		t.Fatalf("gaze looks=%d want 6 (K5 不封顶); beats=%v", n, out.Sheet.Beats)
	}
	office := bind.Score("首先看智能办公场景，它覆盖审批与会议。", "智能办公", "title")
	manu := bind.Score("首先看智能办公场景，它覆盖审批与会议。", "智能制造", "title")
	if office < bind.DefaultThreshold || office <= manu {
		t.Fatalf("智能办公 score=%v 智能制造=%v", office, manu)
	}
	found := false
	hasText := false
	for _, b := range out.Sheet.Beats {
		if strings.TrimSpace(b.Text) != "" {
			hasText = true
		}
		if b.ShapeID != "c2" {
			continue
		}
		found = true
		if b.Shot == animation.BeatShotHold {
			t.Fatal("智能办公卡被标成 hold")
		}
	}
	if !found {
		t.Fatalf("未命中智能办公卡 c2; beats=%v ids=%v", out.Sheet.Beats, gazeIDs(out.Sheet))
	}
	if !hasText {
		t.Fatal("Beat.Text must copy piece text for the word pointer")
	}
}

func TestT3EstimateFallsBackToPunct(t *testing.T) {
	cards := sixCards()
	text := "第一张讲智慧城市。第二张讲智能办公。第三张讲智能制造。"
	out := Produce(Input{
		Elements:   cards,
		CompileIDs: compileIDs(cards...),
		Segments: []SpeechSeg{{
			ID:      "s1",
			Text:    text,
			StartMs: 0,
			EndMs:   12000,
			Method:  tts.AlignEstimate,
			Chars:   charsWithGaps(text, 0, 50, nil), // 有 Chars 也走 punct
		}},
	})
	if out.Sheet.Source != animation.BeatSourcePunct {
		t.Fatalf("source=%s want punct", out.Sheet.Source)
	}
	if n := gazeCount(out.Sheet); n != 3 {
		t.Fatalf("gaze=%d want 3; beats=%v", n, out.Sheet.Beats)
	}
	var prev int64 = -1
	for _, b := range out.Sheet.Beats {
		if b.Shot == animation.BeatShotHold {
			continue
		}
		if prev >= 0 && b.StartMs-prev < 400 {
			t.Fatalf("gaze 间距 %d < 400ms (%d → %d)", b.StartMs-prev, prev, b.StartMs)
		}
		prev = b.StartMs
	}
}

func TestT4VADCutsOnCharClock(t *testing.T) {
	cards := sixCards()
	text := "第一张讲智慧城市。第二张讲智能办公。第三张讲智能制造。"
	var gaps map[int]int64
	runes := []rune(text)
	gaps = map[int]int64{}
	for i, r := range runes {
		if r == '。' && i < len(runes)-1 {
			gaps[i] = 800
		}
	}
	chs := charsWithGaps(text, 0, 200, gaps)
	out := Produce(Input{
		Elements:   cards,
		CompileIDs: compileIDs(cards...),
		Segments: []SpeechSeg{{
			ID:      "s1",
			Text:    text,
			StartMs: 0,
			EndMs:   chs[len(chs)-1].EndMs,
			Method:  tts.AlignEstimateVAD,
			Chars:   chs,
		}},
	})
	if out.Sheet.Source != animation.BeatSourceVAD {
		t.Fatalf("source=%s want vad", out.Sheet.Source)
	}
	if n := gazeCount(out.Sheet); n != 3 {
		t.Fatalf("gaze=%d want 3; beats=%v", n, out.Sheet.Beats)
	}
	// 第二拍 t0 = 第一处 。 之后那个字的 StartMs，不是 i*dur/n。
	var firstPunct int
	for i, r := range runes {
		if r == '。' {
			firstPunct = i
			break
		}
	}
	wantT0 := chs[firstPunct+1].StartMs
	equalSpread := (chs[len(chs)-1].EndMs) / 3
	var second animation.Beat
	gazes := 0
	for _, b := range out.Sheet.Beats {
		if b.ShapeID == "" || b.Shot == animation.BeatShotHold {
			continue
		}
		gazes++
		if gazes == 2 {
			second = b
		}
	}
	if second.StartMs != wantT0 {
		t.Fatalf("第二拍 t0=%d want %d (equal-spread would be ~%d)", second.StartMs, wantT0, equalSpread)
	}
}

func TestT4bNoPunctVADGap(t *testing.T) {
	cards := sixCards()
	text := "首先看智能办公接着看智能制造"
	runes := []rune(text)
	// 「公」在「智能办公」末字。
	pub := -1
	for i := 0; i < len(runes)-1; i++ {
		if string(runes[i:i+2]) == "公接" {
			pub = i
			break
		}
	}
	if pub < 0 {
		t.Fatal("夹具文本不含「公接」")
	}

	t.Run("800ms", func(t *testing.T) {
		chs := charsWithGaps(text, 0, 100, map[int]int64{pub: 800})
		out := Produce(Input{
			Elements:   cards,
			CompileIDs: compileIDs(cards...),
			Segments: []SpeechSeg{{
				ID: "s1", Text: text, StartMs: 0, EndMs: chs[len(chs)-1].EndMs,
				Method: tts.AlignEstimateVAD, Chars: chs,
			}},
		})
		if out.Sheet.Source != animation.BeatSourceVAD {
			t.Fatalf("source=%s reason=%s", out.Sheet.Source, out.Reason)
		}
		if n := gazeCount(out.Sheet); n != 2 {
			t.Fatalf("gaze=%d want 2; beats=%v", n, out.Sheet.Beats)
		}
		var t0s []int64
		for _, b := range out.Sheet.Beats {
			if b.ShapeID == "" || b.Shot == animation.BeatShotHold {
				continue
			}
			t0s = append(t0s, b.StartMs)
		}
		if len(t0s) != 2 {
			t.Fatalf("t0s=%v", t0s)
		}
		if t0s[1] != chs[pub+1].StartMs {
			t.Fatalf("第二拍 t0=%d want %d (空隙后)", t0s[1], chs[pub+1].StartMs)
		}
	})

	t.Run("40ms", func(t *testing.T) {
		chs := charsWithGaps(text, 0, 100, map[int]int64{pub: 40})
		seg := SpeechSeg{
			ID: "s1", Text: text, StartMs: 0, EndMs: chs[len(chs)-1].EndMs,
			Method: tts.AlignEstimateVAD, Chars: chs,
		}
		pieces, src := splitAll(Input{Segments: []SpeechSeg{seg}})
		if src != animation.BeatSourceVAD {
			t.Fatalf("source=%s", src)
		}
		if len(pieces) != 1 {
			t.Fatalf("40ms 空隙切成 %d 拍: %+v", len(pieces), pieces)
		}
	})
}

func TestT5StructuredCoverTooFew(t *testing.T) {
	cards := sixCards()
	els := append(append([]bind.Element{}, cards...), pageFooter())
	out := Produce(Input{
		Elements:   els,
		CompileIDs: compileIDs(cards...),
		Segments: []SpeechSeg{{
			ID: "s1", Text: "本页展示六个应用方向。",
			StartMs: 0, EndMs: 80000, Method: tts.AlignEstimate,
		}},
	})
	if out.Reason != ReasonTooFew && out.Reason != ReasonStructuredCover {
		t.Fatalf("reason=%s want too_few|structured_cover", out.Reason)
	}
	if out.Sheet.Valid() {
		t.Fatalf("总起页不应 Valid: %+v", out.Sheet)
	}
	if _, ok := out.DenyIDs["pn"]; !ok {
		t.Fatal("回退仍应带上页脚 DenyIDs")
	}
}

func TestT5bAccidentalTwoHitsFallback(t *testing.T) {
	cards := sixCards()
	markers := cardMarkers(cards)
	els := append(append([]bind.Element{}, cards...), markers...)
	els = append(els, pageFooter())
	text := "应用方向包括六个智能相关能力。"
	band := bind.Run(els, []bind.Segment{{ID: "s1", Text: text}}, bind.Options{})
	if band.Band != bind.BandStructured {
		t.Fatalf("夹具 Band=%s reason=%s signals=%v，需要 structured", band.Band, band.BandReason, band.Signals)
	}
	nonAux := 0
	for _, b := range band.Bindings {
		if b.SegmentID == "s1" {
			nonAux++
		}
	}
	if nonAux < 3 {
		t.Fatalf("bind.Run 非 aux 绑定=%d want ≥3: %+v", nonAux, band.Bindings)
	}
	office := bind.Score(text, "智能办公", "title")
	manu := bind.Score(text, "智能制造", "title")
	city := bind.Score(text, "智慧城市", "title")
	if office < bind.DefaultThreshold || manu < bind.DefaultThreshold {
		t.Fatalf("偶然命中不足: 办公=%v 制造=%v 城市=%v", office, manu, city)
	}
	if city >= bind.DefaultThreshold {
		t.Fatalf("智慧城市不应过阈值: %v", city)
	}

	out := Produce(Input{
		Elements:   els,
		CompileIDs: compileIDs(cards...),
		Segments: []SpeechSeg{{
			ID: "s1", Text: text, StartMs: 0, EndMs: 80000, Method: tts.AlignEstimate,
		}},
	})
	if out.Reason != ReasonStructuredCover {
		t.Fatalf("reason=%s source=%s gaze=%d want structured_cover", out.Reason, out.Sheet.Source, gazeCount(out.Sheet))
	}
	if out.Sheet.Valid() {
		t.Fatal("structured_cover 应置空 Sheet")
	}
}

func TestT7HoldMsMergeNotCap5(t *testing.T) {
	names := append(append([]string{}, sixCardNames...), "绿色能源", "数据中台")
	posY := []float64{80, 80, 80, 80, 400, 400, 400, 400}
	posX := []float64{40, 500, 960, 1420, 40, 500, 960, 1420}
	cards := make([]bind.Element, len(names))
	for i, name := range names {
		cards[i] = bind.Element{
			ID:   "c" + strings.TrimSpace(string(rune('1'+i))),
			Kind: "title",
			Text: name,
			Box:  bind.Box{X: posX[i], Y: posY[i], W: 400, H: 200},
		}
		cards[i].ID = "k" + string(rune('a'+i))
	}
	text := "这一页讲智慧城市。这一页讲智能办公。对。这一页讲智能制造。这一页讲数字政务。这一页讲智慧医疗。这一页讲产业升级。这一页讲绿色能源。"
	out := Produce(Input{
		Elements:   cards,
		CompileIDs: compileIDs(cards...),
		Segments: []SpeechSeg{{
			ID: "s1", Text: text, StartMs: 0, EndMs: 20000, Method: tts.AlignEstimate,
		}},
	})
	n := gazeCount(out.Sheet)
	if n == 5 {
		t.Fatalf("把 8 压成了 5: beats=%v", out.Sheet.Beats)
	}
	if n < 6 || n > 8 {
		t.Fatalf("gaze=%d want 6..8 (短句合并，不是封顶 5); beats=%v", n, out.Sheet.Beats)
	}
	// 「对。」应并入邻拍，拍数少于句读切点数 8。
	pieces, _ := splitAll(Input{
		Segments: []SpeechSeg{{ID: "s1", Text: text, StartMs: 0, EndMs: 20000, Method: tts.AlignEstimate}},
	})
	if len(pieces) >= 8 {
		t.Fatalf("短句未合并: %d 片 %+v", len(pieces), pieces)
	}
}

func TestT10OpeningHoleFromLaterHit(t *testing.T) {
	cards := sixCards()
	text := "今天我们先回顾背景。接着看智能办公。最后看智慧城市。"
	out := Produce(Input{
		Elements:   cards,
		CompileIDs: compileIDs(cards...),
		Segments: []SpeechSeg{{
			ID: "s1", Text: text, StartMs: 0, EndMs: 15000, Method: tts.AlignEstimate,
		}},
	})
	if out.Reason != ReasonOK {
		t.Fatalf("reason=%s beats=%v", out.Reason, out.Sheet.Beats)
	}
	if len(out.Sheet.Beats) < 2 {
		t.Fatalf("beats=%v", out.Sheet.Beats)
	}
	b0 := out.Sheet.Beats[0]
	if b0.ShapeID != "c2" || b0.Shot == animation.BeatShotHold {
		t.Fatalf("拍 0 应为智能办公 c2，got %+v", b0)
	}
	if b0.StartMs != 0 {
		t.Fatalf("开场孔应从第一片 t0=0 起，got %d", b0.StartMs)
	}
	donorHold := false
	for i, b := range out.Sheet.Beats {
		if i == 0 {
			continue
		}
		if b.Shot == animation.BeatShotHold && (b.ShapeID == "" || b.ShapeID == "c2") {
			donorHold = true
		}
	}
	if !donorHold {
		t.Fatalf("原命中应改为 hold: %+v", out.Sheet.Beats)
	}
	if n := gazeCount(out.Sheet); n != 2 {
		t.Fatalf("gaze=%d want 2 (c2 开场 + 智慧城市)", n)
	}

	t.Run("onlyOneHitTooFew", func(t *testing.T) {
		one := "今天我们先回顾背景。接着看智能办公。"
		got := Produce(Input{
			Elements:   cards,
			CompileIDs: compileIDs(cards...),
			Segments: []SpeechSeg{{
				ID: "s1", Text: one, StartMs: 0, EndMs: 10000, Method: tts.AlignEstimate,
			}},
		})
		if got.Reason != ReasonTooFew {
			t.Fatalf("单卡开场孔 reason=%s want too_few; beats=%v", got.Reason, got.Sheet.Beats)
		}
	})
}

func TestT12OrdinalCommaSplitsTOCSections(t *testing.T) {
	elems := []bind.Element{
		{ID: "sec-1", Kind: "title", Text: "行业趋势", Box: bind.Box{X: 160, Y: 240, W: 560, H: 80}},
		{ID: "sec-2", Kind: "title", Text: "AI 大模型", Box: bind.Box{X: 160, Y: 380, W: 560, H: 80}},
		{ID: "sec-3", Kind: "title", Text: "智能体", Box: bind.Box{X: 160, Y: 520, W: 560, H: 80}},
	}
	text := "报告目录。第二部分是 AI 大模型，第三部分是智能体。"
	out := Produce(Input{
		Elements:   elems,
		CompileIDs: compileIDs(elems...),
		Segments: []SpeechSeg{{
			ID: "s1", Text: text, StartMs: 0, EndMs: 15000, Method: tts.AlignEstimate,
		}},
	})
	if out.Reason != ReasonOK {
		t.Fatalf("reason=%s beats=%v", out.Reason, out.Sheet.Beats)
	}
	ids := gazeIDs(out.Sheet)
	if len(ids) != 2 || ids[0] != "sec-2" || ids[1] != "sec-3" {
		t.Fatalf("TOC gaze=%v want [sec-2 sec-3]; beats=%v", ids, out.Sheet.Beats)
	}
	var thirdStart int64 = -1
	for _, b := range out.Sheet.Beats {
		if b.ShapeID == "sec-3" && b.Shot != animation.BeatShotHold {
			thirdStart = b.StartMs
		}
	}
	if thirdStart < 0 {
		t.Fatal("第三部分没有独立拍点")
	}
	if thirdStart <= out.Sheet.Beats[0].StartMs {
		t.Fatalf("第三部分必须排在开场孔之后: %v", out.Sheet.Beats)
	}
}

func TestT11DecorativeBindIDDoesNotForceFallback(t *testing.T) {
	cards := sixCards()[:2]
	deco := bind.Element{
		ID:   "deco",
		Kind: "bullet",
		Text: "智能办公",
		Box:  bind.Box{X: 10, Y: 10, W: 80, H: 20},
	}
	els := append(append([]bind.Element{}, cards...), deco)
	text := "首先看智能办公。接着看智慧城市。"
	out := Produce(Input{
		Elements:   els,
		CompileIDs: compileIDs(cards...),
		Segments: []SpeechSeg{{
			ID: "s1", Text: text, StartMs: 0, EndMs: 8000, Method: tts.AlignEstimate,
		}},
	})
	if out.Reason != ReasonOK {
		t.Fatalf("reason=%s want ok; beats=%v", out.Reason, out.Sheet.Beats)
	}
	if n := gazeCount(out.Sheet); n != 2 {
		t.Fatalf("gaze=%d want 2; beats=%v", n, out.Sheet.Beats)
	}
	for _, b := range out.Sheet.Beats {
		if b.ShapeID == "deco" {
			t.Fatal("装饰 ID 不成拍")
		}
	}
}
