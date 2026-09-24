package tts

import (
	"math"
	"testing"
)

// 阶段一量化对比：构造带"已知字级真值"的合成语料（每句含若干短语 + 真实停顿），
// 分别用 当前整体匀速估算(estimated) 与 静音锚点估算(estimated_vad) 生成字级时间轴，
// 统计相对真值的平均绝对偏差(MAE)与最大偏差(MaxAE)。
//
// 说明：受本机条件所限（无法人工听音频标注），这里用合成语料作为受控真值基准；
// 真实 TTS 音频的人工标注基准可作为上线前的补充验收（见 docs 对齐方案说明）。

const (
	benchLeadMS  = 200.0
	benchTailMS  = 200.0
	benchCharMin = 170.0
	benchCharMax = 260.0
)

// 短语用字池（均为非标点普通字）。
var benchPool = []rune("春眠不觉晓处处闻啼鸟夜来风雨声花落知多少明月松间照清泉石上流")

// jitterReal 是贴近真实 TTS 的逐字时长抖动（±6% 级）；jitterStress 是压力抖动（±16% 级）。
// 语料同时包含两类，分别统计，避免用"调样本"的方式美化指标。
var jitterReal = []float64{1.0, 1.05, 0.97, 1.02, 0.95, 1.03, 0.98, 1.06}
var jitterStress = []float64{1.0, 1.16, 0.90, 1.12, 0.88, 1.14, 0.92, 1.18}

type synthUtterance struct {
	text         string
	wav          []byte
	truthStartUS []int64
	durationMS   int64
}

// phrasePlan 描述每个短语的字符数、紧随其后的标点（0=其后无标点）与逐字时长抖动。
type phrasePlan struct {
	chars   int
	punct   rune
	perChar float64
	jitter  []float64
}

func synthFromPlan(t *testing.T, sampleRate int, plans []phrasePlan) synthUtterance {
	t.Helper()
	const amp int16 = 12000
	samples := make([]int16, 0, sampleRate*4)

	// 首静音。
	appendSilence := func(ms float64) {
		n := int(float64(sampleRate) * ms / 1000.0)
		for i := 0; i < n; i++ {
			samples = append(samples, 0)
		}
	}
	// syllableGain 近似真实音节的能量包络（快速起音 + 指数衰减 + 0.12 基底残响），
	// 使块与块之间存在能量谷底——这正是"音节核锚定"能利用、而匀速分布会错位的声学结构。
	syllableGain := func(x float64) float64 {
		if x <= 0 || x >= 1 {
			return 0.12
		}
		rise := x / 0.18
		if rise > 1 {
			rise = 1
		}
		decay := math.Exp(-2.6 * (x - 0.18))
		if decay < 0.12 {
			decay = 0.12
		}
		return rise * decay
	}
	appendVoiced := func(ms float64) {
		n := int(float64(sampleRate) * ms / 1000.0)
		for i := 0; i < n; i++ {
			// 每块内部逐样点应用包络，制造可检测的音节核。
			g := syllableGain(float64(i) / float64(n))
			samples = append(samples, int16(float64(amp)*g))
		}
	}

	appendSilence(benchLeadMS)
	var text []rune
	var truth []int64
	cursorMS := benchLeadMS
	poolIdx := 0
	for pi, p := range plans {
		for c := 0; c < p.chars; c++ {
			r := benchPool[poolIdx%len(benchPool)]
			poolIdx++
			text = append(text, r)
			truth = append(truth, int64(cursorMS*1000))
			// 每个字符的时长用确定性抖动，模拟真实语速变化。
			cm := p.perChar
			if len(p.jitter) > 0 {
				cm *= p.jitter[c%len(p.jitter)]
			}
			appendVoiced(cm)
			cursorMS += cm
		}
		pause := 0.0
		switch p.punct {
		case '，':
			pause = 200
		case '。':
			pause = 360
		}
		if p.punct != 0 {
			text = append(text, p.punct)
			// 标点本身不发声，占用极短（这里给 30ms 避免零时长），真值起点在停顿开始处。
			truth = append(truth, int64(cursorMS*1000))
			appendVoiced(30)
			cursorMS += 30
			_ = pi
		}
		if pause > 0 {
			appendSilence(pause)
			cursorMS += pause
		}
	}
	appendSilence(benchTailMS)
	cursorMS += benchTailMS

	return synthUtterance{
		text:         string(text),
		wav:          wav16FromSamples(t, sampleRate, samples),
		truthStartUS: truth,
		durationMS:   int64(math.Round(cursorMS)),
	}
}

func makeSyntheticCorpus(t *testing.T) []synthUtterance {
	t.Helper()
	const rate = 16000
	base := 215.0
	mk := func(chars int, punct rune, j []float64) phrasePlan {
		return phrasePlan{chars: chars, punct: punct, perChar: base, jitter: j}
	}
	// 常规组（±6% 抖动，贴近真实 TTS）。
	regular := [][]phrasePlan{
		{mk(12, '，', jitterReal), mk(14, '。', jitterReal)},
		{mk(8, '，', jitterReal), mk(9, '，', jitterReal), mk(11, '。', jitterReal)},
		{mk(16, '。', jitterReal), mk(13, '。', jitterReal)},
		{mk(6, '，', jitterReal), mk(7, '，', jitterReal), mk(8, '，', jitterReal), mk(9, '。', jitterReal)},
		{mk(10, '，', jitterReal), mk(12, '。', jitterReal), mk(9, '。', jitterReal)},
		{mk(7, '，', jitterReal), mk(18, '。', jitterReal)},
		{mk(28, '。', jitterReal)}, // 无内部标点长从句：无静音锚点可用，只靠音节核。
	}
	// 压力组（±16% 抖动，检验极端语速起伏下的鲁棒性）。
	stress := [][]phrasePlan{
		{mk(12, '，', jitterStress), mk(14, '。', jitterStress)},
		{mk(6, '，', jitterStress), mk(7, '，', jitterStress), mk(8, '，', jitterStress), mk(9, '。', jitterStress)},
		{mk(15, '，', jitterStress), mk(15, '。', jitterStress)},
		{mk(9, '，', jitterStress), mk(10, '，', jitterStress), mk(8, '。', jitterStress)},
		{mk(24, '。', jitterStress)}, // 压力组同样含无内部标点长从句。
	}
	out := make([]synthUtterance, 0, len(regular)+len(stress))
	for _, p := range regular {
		out = append(out, synthFromPlan(t, rate, p))
	}
	for _, p := range stress {
		out = append(out, synthFromPlan(t, rate, p))
	}
	return out
}

type benchStat struct {
	mae float64
	max float64
	n   int
}

func (s *benchStat) add(d int64) {
	a := math.Abs(float64(d))
	s.mae += a
	if a > s.max {
		s.max = a
	}
	s.n++
}

func (s benchStat) meanMS() float64 {
	if s.n == 0 {
		return 0
	}
	return s.mae / float64(s.n) / 1000.0
}

func (s benchStat) maxMS() float64 { return s.max / 1000.0 }

func TestAlignmentQuantitativeComparison(t *testing.T) {
	corpus := makeSyntheticCorpus(t)
	const regularCount = 7 // 前 7 句为常规组，其余为压力组

	measure := func(start, end int) (cur, anchor, nuclei benchStat) {
		for _, u := range corpus[start:end] {
			curA := buildEstimatedAlignment(u.text, u.durationMS)
			anchorA := buildEstimatedAudioAlignment(u.text, u.wav, u.durationMS, false)
			nucleiA := buildEstimatedVADAlignment(u.text, u.wav, u.durationMS)
			if len(curA.Tokens) != len(u.truthStartUS) || len(anchorA.Tokens) != len(u.truthStartUS) || len(nucleiA.Tokens) != len(u.truthStartUS) {
				t.Fatalf("token/truth length mismatch: cur=%d anchor=%d nuclei=%d truth=%d text=%q",
					len(curA.Tokens), len(anchorA.Tokens), len(nucleiA.Tokens), len(u.truthStartUS), u.text)
			}
			for i := range u.truthStartUS {
				cur.add(curA.Tokens[i].StartUS - u.truthStartUS[i])
				anchor.add(anchorA.Tokens[i].StartUS - u.truthStartUS[i])
				nuclei.add(nucleiA.Tokens[i].StartUS - u.truthStartUS[i])
			}
		}
		return cur, anchor, nuclei
	}

	curAll, anchorAll, nucleiAll := measure(0, len(corpus))
	curReg, anchorReg, nucleiReg := measure(0, regularCount)
	curStr, anchorStr, nucleiStr := measure(regularCount, len(corpus))

	if curAll.n == 0 {
		t.Fatal("empty corpus")
	}
	t.Logf("对齐方法量化对比（合成音节包络语料；真值=逐字真实起始时刻）")
	t.Logf("%-26s %10s %10s %6s", "分组/方法", "MAE(ms)", "MaxAE(ms)", "字数")
	row := func(name string, s benchStat) { t.Logf("%-26s %10.1f %10.1f %6d", name, s.meanMS(), s.maxMS(), s.n) }
	row("全部/estimated(纯盲估)", curAll)
	row("全部/estimated_vad(静音锚点+匀速)", anchorAll)
	row("全部/estimated_vad(音节核锚定,当前)", nucleiAll)
	row("常规/estimated(纯盲估)", curReg)
	row("常规/estimated_vad(静音锚点+匀速)", anchorReg)
	row("常规/estimated_vad(音节核锚定,当前)", nucleiReg)
	row("压力/estimated(纯盲估)", curStr)
	row("压力/estimated_vad(静音锚点+匀速)", anchorStr)
	row("压力/estimated_vad(音节核锚定,当前)", nucleiStr)

	// 断言：音节核锚定必须优于纯盲估与"静音锚点+匀速"两者。
	if nucleiAll.meanMS() >= curAll.meanMS() || nucleiAll.meanMS() >= anchorAll.meanMS() {
		t.Fatalf("音节核锚定未改善：nuclei=%.1fms anchor=%.1fms cur=%.1fms",
			nucleiAll.meanMS(), anchorAll.meanMS(), curAll.meanMS())
	}
	t.Logf("全量 MAE: blind=%.1fms → anchor=%.1fms → nuclei=%.1fms",
		curAll.meanMS(), anchorAll.meanMS(), nucleiAll.meanMS())
	t.Logf("音节核 vs 盲估改进: %.0f%%；音节核 vs 锚点匀速改进: %.0f%%",
		(1-nucleiAll.meanMS()/curAll.meanMS())*100,
		(1-nucleiAll.meanMS()/anchorAll.meanMS())*100)
}
