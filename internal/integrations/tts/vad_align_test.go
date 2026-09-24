package tts

import (
	"math"
	"testing"
)

// burstWAV 生成带"语音段（方波）+ 静音段"的 PCM16 mono WAV，用于验证 VAD。
// segments 依次标注每段时长(ms)与是否语音。
func burstWAV(t *testing.T, sampleRate int, segments []struct {
	ms     int
	voiced bool
}, amp int16) []byte {
	t.Helper()
	totalMS := 0
	for _, s := range segments {
		totalMS += s.ms
	}
	n := sampleRate * totalMS / 1000
	samples := make([]int16, n)
	pos := 0
	for _, s := range segments {
		cnt := sampleRate * s.ms / 1000
		for k := 0; k < cnt; k++ {
			idx := pos + k
			if s.voiced {
				samples[idx] = amp
			} else {
				samples[idx] = 0
			}
		}
		pos += cnt
	}
	return wav16FromSamples(t, sampleRate, samples)
}

func wav16FromSamples(t *testing.T, sampleRate int, samples []int16) []byte {
	t.Helper()
	dataSize := uint32(len(samples) * 2)
	out := make([]byte, 44+dataSize)
	copy(out[0:4], "RIFF")
	putLe32(out[4:], 36+dataSize)
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	putLe32(out[16:], 16)
	putLe16(out[20:], 1) // PCM
	putLe16(out[22:], 1) // mono
	putLe32(out[24:], uint32(sampleRate))
	putLe32(out[28:], uint32(sampleRate*2))
	putLe16(out[32:], 2)
	putLe16(out[34:], 16)
	copy(out[36:40], "data")
	putLe32(out[40:], dataSize)
	for i, s := range samples {
		out[44+i*2], out[44+i*2+1] = byte(s), byte(s>>8)
	}
	return out
}

// segments 便捷构造：一段带间隔的合成"语音"，含：
//
//	300ms 首静音 / 600ms 语音 / 200ms 停顿 / 500ms 语音 / 150ms 停顿 / 700ms 语音 / 300ms 尾静音
func speechSegments() []struct {
	ms     int
	voiced bool
} {
	seg := func(ms int, v bool) struct {
		ms     int
		voiced bool
	} {
		return struct {
			ms     int
			voiced bool
		}{ms, v}
	}
	return []struct {
		ms     int
		voiced bool
	}{
		seg(300, false), seg(600, true), seg(200, false), seg(500, true),
		seg(150, false), seg(700, true), seg(300, false),
	}
}

func TestDetectSilenceRunsFindsInteriorPauses(t *testing.T) {
	const rate = 16000
	wav := burstWAV(t, rate, speechSegments(), 12000)
	samples, sr, ch, ok := wavPCM16(wav)
	if !ok || sr != rate || ch != 1 {
		t.Fatalf("wavPCM16: ok=%v sr=%d ch=%d", ok, sr, ch)
	}
	runs, leadUS, speechEndUS, ok := detectSilenceRuns(samples, sr*ch)
	if !ok {
		t.Fatal("expected ok=true (has speech + interior pauses)")
	}
	if len(runs) != 2 {
		t.Fatalf("interior silence runs = %d, want 2 (%+v)", len(runs), runs)
	}
	// 停顿1 ≈ [900ms, 1100ms)，停顿2 ≈ [1600ms, 1750ms)
	check := func(got silenceRange, wantStart, wantEnd float64) {
		start := float64(got.startUS) / 1000
		end := float64(got.endUS) / 1000
		if math.Abs(start-wantStart) > 25 || math.Abs(end-wantEnd) > 25 {
			t.Fatalf("silence [%v,%v]ms, want [%v,%v]ms", start, end, wantStart, wantEnd)
		}
	}
	check(runs[0], 900, 1100)
	check(runs[1], 1600, 1750)
	if leadUS != 300*1000 {
		t.Fatalf("leadUS = %d, want 300000", leadUS)
	}
	// 末语音结束 ≈ 2450ms；帧粒度(20ms)允许 ±1 帧误差。
	if got := speechEndUS / 1000; got < 2440 || got > 2470 {
		t.Fatalf("speechEndUS = %d ms, want ≈2450ms", got)
	}
}

func TestDetectSilenceRunsRejectsAllSilent(t *testing.T) {
	const rate = 16000
	wav := wav16FromSamples(t, rate, make([]int16, rate))
	samples, sr, _, ok := wavPCM16(wav)
	if !ok {
		t.Fatal("parse failed")
	}
	if _, _, _, ok := detectSilenceRuns(samples, sr); ok {
		t.Fatal("pure silence must not produce usable VAD")
	}
}

func TestBuildEstimatedVADAlignmentAnchorsAtPunctuation(t *testing.T) {
	const rate = 16000
	// 公式化音频：逐字近等时（TTS 中文的合理假设），停顿出现在句子/从句末标点后——
	// 这正是 DP 匹配所依据的时间模型。
	text := "今天天气很好，我们出去走走吧。"
	wav := proportionalTextWAV(t, rate, text, map[rune]int{'，': 200, '。': 320}, 120, 200, 200)
	a := buildEstimatedVADAlignment(text, wav, durationMSOfWAV(wav, rate))
	if a.Method != AlignEstimateVAD {
		t.Fatalf("method = %s, want %s (%+v)", a.Method, AlignEstimateVAD, a)
	}
	if len(a.Tokens) != len([]rune(text)) {
		t.Fatalf("tokens = %d, want %d", len(a.Tokens), len([]rune(text)))
	}
	// 校验单调、在时长内。
	var prev int64
	for _, tok := range a.Tokens {
		if tok.StartUS < prev || tok.EndUS <= tok.StartUS {
			t.Fatalf("non-monotonic token %+v", tok)
		}
		prev = tok.EndUS
	}
	limitUS := durationMSOfWAV(wav, rate) * 1000
	if a.Tokens[len(a.Tokens)-1].EndUS > limitUS {
		t.Fatalf("token beyond duration")
	}
	// 逗号（第 7 个字符，含前 6 字）应结束在逗号停顿（≈950ms 起）而不是被错配到尾句。
	runes := []rune(text)
	commaIdx := -1
	for i, r := range runes {
		if string(r) == "，" {
			commaIdx = i
			break
		}
	}
	if commaIdx < 0 {
		t.Fatal("no comma in text")
	}
	commaEndUS := a.Tokens[commaIdx].EndUS
	if d := math.Abs(float64(commaEndUS)/1000 - 950); d > 40 {
		t.Fatalf("comma EndUS = %d (=%v ms), want ≈950ms (anchored to comma pause)", commaEndUS, float64(commaEndUS)/1000)
	}
}

// proportionalTextWAV 生成与文本逐字等时的合成语音：每个非标点字发 perCharMS，停顿标点后接
// 对应 pauseMS 静音。punctPause 提供标点→停顿时长映射。
func proportionalTextWAV(t *testing.T, sampleRate int, text string, punctPause map[rune]int, perCharMS, leadMS, tailMS int) []byte {
	t.Helper()
	const amp int16 = 12000
	var samples []int16
	appendN := func(ms float64, v int16) {
		n := int(float64(sampleRate) * ms / 1000.0)
		for i := 0; i < n; i++ {
			samples = append(samples, v)
		}
	}
	appendN(float64(leadMS), 0)
	skip := func(r rune) bool { _, ok := punctPause[r]; return ok }
	for _, r := range text {
		if skip(r) {
			appendN(float64(30), amp) // 标点本身几乎不发声
			appendN(float64(punctPause[r]), 0)
			continue
		}
		appendN(float64(perCharMS), amp) // 每个字一个发音块
	}
	appendN(float64(tailMS), 0)
	return wav16FromSamples(t, sampleRate, samples)
}

func TestBuildEstimatedVADAlignmentFallsBackWithoutPunctOrPause(t *testing.T) {
	const rate = 16000
	// 无标点：即使有停顿也不能锚定 → 回退 estimated。
	noPunct := "今天天气很好我们出去走走吧"
	wav := burstWAV(t, rate, speechSegments(), 12000)
	if a := buildEstimatedVADAlignment(noPunct, wav, durationMSOfWAV(wav, rate)); a.Method != AlignEstimate {
		t.Fatalf("no-punct method = %s, want %s", a.Method, AlignEstimate)
	}
	// 纯静音：VAD 无效 → 回退 estimated。
	silent := wav16FromSamples(t, rate, make([]int16, rate*2))
	if a := buildEstimatedVADAlignment("今天天气很好，我们出去走走吧。", silent, durationMSOfWAV(silent, rate)); a.Method != AlignEstimate {
		t.Fatalf("all-silent method = %s, want %s", a.Method, AlignEstimate)
	}
	// 单段连续语音无内部停顿（首尾静音不计入内部停顿）→ 回退 estimated。
	continuous := burstWAV(t, rate, []struct {
		ms     int
		voiced bool
	}{{300, false}, {2000, true}, {300, false}}, 12000)
	if a := buildEstimatedVADAlignment("今天天气很好，我们出去走走吧。", continuous, durationMSOfWAV(continuous, rate)); a.Method != AlignEstimate {
		t.Fatalf("pause-free method = %s, want %s", a.Method, AlignEstimate)
	}
}

// durationMSOfWAV 由 WAV 解析时长（毫秒）。
func durationMSOfWAV(wav []byte, rate int) int64 {
	ms, err := wavDurationMSFromRIFF(wav)
	if err != nil {
		panic(err)
	}
	return ms
}
