package tts

// 阶段一：静音锚点估算（estimated_vad）。
//
// 目标：不引入任何外部模型/服务，只用 TTS 合成回来的真实 WAV 音频本身，把匀速估算对齐的
// 漂移压低。做法：对整段音频做基于能量的静音检测（VAD），找出真实存在的停顿区间；把停顿按
// 顺序映射到文本标点位置作为"锚点"，把整段时间轴切成若干子区间；每个子区间内部才做匀速
// 字符分布（复用 buildEstimatedAlignment 的区间内逻辑，只是把作用范围收窄）。
//
// 兜底：检测不到有效静音、文本没有标点、或映射后子区间非法时，一律回退到原有整体匀速估算
// （AlignEstimate），保证对齐流程绝不因 VAD 失败报错或空转。

import (
	"math"
)

// vadFrameMS 是能量分析的帧长。
const vadFrameMS = 20

// vadMinSilenceMS 是"算一个停顿"的最短静音时长；过短的停顿不锚定，避免把喘息/齿音当停顿。
const vadMinSilenceMS = 90

// vadSilenceRel 是相对峰值能量的静音阈值（帧 RMS < peak*此值 判为静音）。
const vadSilenceRel = 0.08

// vadSilenceAbs 是绝对静音下限（int16 幅度）；低于它即使相对阈值也判静音，用于处理近纯静音。
const vadSilenceAbs = 40.0

// silenceRange 是检测到的内部静音区间（微秒，相对音频起点）。
type silenceRange struct {
	startUS int64
	endUS   int64
}

// buildEstimatedVADAlignment 由真实 WAV 音频构造"静音锚点估算"对齐；不符合条件时回退到
// buildEstimatedAlignment（Method=AlignEstimate）。返回的 tokens 以音频起点为基准（与既有
// buildEstimatedAlignment 一致：含首静音在前，逐字 EndUS 不越过 durationMS 对应时刻）。
func buildEstimatedVADAlignment(text string, wav []byte, durationMS int64) Alignment {
	fallback := func() Alignment { return buildEstimatedAlignment(text, durationMS) }
	if durationMS <= 0 {
		return fallback()
	}
	samples, sampleRate, channels, ok := wavPCM16(wav)
	if !ok || sampleRate <= 0 || channels <= 0 {
		return fallback()
	}
	// 多声道时把声道数并入"有效采样率"：时长换算与单声道一致。
	effRate := sampleRate * channels
	runs, leadUS, speechEndUS, ok := detectSilenceRuns(samples, effRate)
	if !ok || len(runs) == 0 {
		return fallback()
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return fallback()
	}
	var punctIdx []int
	lastRune := len(runes) - 1
	for i, r := range runes {
		// 末尾标点之后没有字符，其停顿会与音频尾部静音合并，不参与锚定（锚了反而把
		// 最后一个停顿错配到前面的标点上）。
		if i == lastRune {
			break
		}
		if isPausePunct(r) {
			punctIdx = append(punctIdx, i)
		}
	}
	if len(punctIdx) == 0 {
		return fallback()
	}
	anchors := mapPunctToSilence(punctIdx, len(runes), runs, leadUS, speechEndUS)
	if len(anchors) == 0 {
		return fallback()
	}

	durationUS := durationMS * 1000
	// 兜底校正：lead/speechEnd 异常时回落默认值，保证后续区间单调且在音频范围内。
	if leadUS < 0 || leadUS > durationUS/2 {
		leadUS = 200_000 // 与既有估算的首静音假设一致
	}
	if speechEndUS <= leadUS || speechEndUS > durationUS {
		speechEndUS = durationUS
		if speechEndUS-leadUS < vadMinSilenceMS*1000 {
			return fallback()
		}
	}
	// 仅保留严格落在 (leadUS, speechEndUS) 内且自身合法的锚点；过滤掉会撑爆区间的锚。
	kept := anchors[:0:0]
	for _, a := range anchors {
		if a.s >= leadUS && a.e <= speechEndUS && a.s < a.e {
			kept = append(kept, a)
		}
	}
	if len(kept) == 0 {
		return fallback()
	}
	tokens, ok := distributeByAnchors(runes, kept, leadUS, speechEndUS)
	if !ok || len(tokens) != len(runes) {
		return fallback()
	}
	return Alignment{Text: text, Tokens: tokens, Method: AlignEstimateVAD}
}

// wavPCM16 解析 PCM16 WAV 的采样与参数；多声道时返回交错采样（有效采样率=sampleRate*channels）。
func wavPCM16(data []byte) (samples []int16, sampleRate int, channels int, ok bool) {
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, 0, false
	}
	var bits int
	i := 12
	for i+8 <= len(data) {
		chunkID := string(data[i : i+4])
		size := int(le32(data[i+4 : i+8]))
		start := i + 8
		switch chunkID {
		case "fmt ":
			if size >= 16 {
				sampleRate = int(le32(data[start+4 : start+8]))
				channels = int(le16(data[start+2 : start+4]))
				bits = int(le16(data[start+14 : start+16]))
			}
		case "data":
			end := start + size
			if end > len(data) || size < 0 {
				end = len(data)
			}
			pcm := data[start:end]
			n := len(pcm) / 2
			out := make([]int16, n)
			for k := 0; k < n; k++ {
				out[k] = int16(le16(pcm[k*2 : k*2+2]))
			}
			return out, sampleRate, channels, sampleRate > 0 && channels > 0 && bits == 16
		}
		step := 8 + size
		if step <= 0 {
			break
		}
		i += step
	}
	return nil, 0, 0, false
}

// detectSilenceRuns 做基于能量的静音检测。返回：
//   - runs：音频**内部**的静音区间（不含首尾静音段），时长 >= vadMinSilenceMS；
//   - leadUS / speechEndUS：首末语音帧起止时刻（微秒）；
//   - ok：音频含可用的语音（有语音帧且至少一个内部静音）。
func detectSilenceRuns(samples []int16, sampleRate int) (runs []silenceRange, leadUS, speechEndUS int64, ok bool) {
	if sampleRate <= 0 {
		return nil, 0, 0, false
	}
	frameLen := sampleRate * vadFrameMS / 1000
	if frameLen <= 0 {
		return nil, 0, 0, false
	}
	nFrames := len(samples) / frameLen
	if nFrames < 4 {
		return nil, 0, 0, false
	}
	frameDurUS := int64(vadFrameMS * 1000)

	rms := make([]float64, nFrames)
	peak := 0.0
	for i := 0; i < nFrames; i++ {
		frames := samples[i*frameLen : (i+1)*frameLen]
		var acc int64
		for _, s := range frames {
			acc += int64(s) * int64(s)
		}
		r := math.Sqrt(float64(acc) / float64(len(frames)))
		rms[i] = r
		if r > peak {
			peak = r
		}
	}
	if peak <= 0 {
		return nil, 0, 0, false // 纯静音：无语音
	}
	thresh := peak * vadSilenceRel
	if thresh < vadSilenceAbs {
		thresh = vadSilenceAbs
	}
	silent := make([]bool, nFrames)
	for i, r := range rms {
		silent[i] = r < thresh
	}

	// 首尾静音段。
	leadFrames := 0
	for leadFrames < nFrames && silent[leadFrames] {
		leadFrames++
	}
	tailFrames := 0
	for tailFrames < nFrames && silent[nFrames-1-tailFrames] {
		tailFrames++
	}
	if leadFrames+tailFrames >= nFrames {
		return nil, 0, 0, false // 全部静音
	}
	leadUS = int64(leadFrames) * frameDurUS
	speechEndUS = int64(nFrames-tailFrames) * frameDurUS

	// 内部静音段。
	minDurUS := int64(vadMinSilenceMS * 1000)
	i := leadFrames
	for i < nFrames-tailFrames {
		if !silent[i] {
			i++
			continue
		}
		j := i
		for j < nFrames-tailFrames && silent[j] {
			j++
		}
		if int64(j-i)*frameDurUS >= minDurUS {
			runs = append(runs, silenceRange{startUS: int64(i) * frameDurUS, endUS: int64(j) * frameDurUS})
		}
		i = j
	}
	if len(runs) == 0 {
		return runs, leadUS, speechEndUS, false
	}
	return runs, leadUS, speechEndUS, true
}

// isPausePunct 判断一个字符是否代表"可停顿"的标点（语音中通常伴随换气停顿）。
func isPausePunct(r rune) bool {
	switch r {
	case '。', '，', '；', '、', '：', '！', '？', '…', '.', ',', ';', ':', '!', '?':
		return true
	}
	return false
}

// anchor 把一个停顿映射到文本标点：pause 之后为停顿区间 [s,e]。
type anchor struct {
	runeIdx int
	s, e    int64
}

// mapPunctToSilence 把"文本标点"映射到"静音区间"，保证锚点单调（标点序与时间序一致）。
// 数量相等时一一对应；不等时按比例抽样（两端对齐）。单个标点/单个静音时，用归一化位置
// （字符相对位置 vs 静音相对时间）就近匹配，避免把末尾停顿锚到前面的标点上。
func mapPunctToSilence(punctIdx []int, totalRunes int, sil []silenceRange, leadUS, speechEndUS int64) []anchor {
	P, S := len(punctIdx), len(sil)
	if P == 0 || S == 0 {
		return nil
	}
	normChar := func(idx int) float64 {
		if totalRunes <= 1 {
			return 0
		}
		return float64(idx) / float64(totalRunes-1)
	}
	silMidNorm := func(r silenceRange) float64 {
		span := speechEndUS - leadUS
		if span <= 0 {
			return 0
		}
		mid := (r.startUS + r.endUS) / 2
		return math.Min(1, math.Max(0, float64(mid-leadUS)/float64(span)))
	}

	anchors := make([]anchor, 0, minInt(P, S))
	if S >= P {
		lastJ := -1
		for i, p := range punctIdx {
			var j int
			if P == 1 {
				j = int(math.Round(normChar(p) * float64(S-1)))
			} else {
				j = int(math.Round(float64(i) * float64(S-1) / float64(P-1)))
			}
			j = clampInt(j, 0, S-1)
			if j == lastJ {
				continue
			}
			lastJ = j
			anchors = append(anchors, anchor{runeIdx: p, s: sil[j].startUS, e: sil[j].endUS})
		}
		return anchors
	}
	// S < P：为每个静音挑选最匹配的标点（数量少的一侧做等比抽样）。
	lastI := -1
	for j := range sil {
		var i int
		if S == 1 {
			// 单个静音：挑归一化字符位置最接近该静音归一化时间的标点。
			best, bestDist := 0, math.MaxFloat64
			sm := silMidNorm(sil[j])
			for k, p := range punctIdx {
				if d := math.Abs(normChar(p) - sm); d < bestDist {
					best, bestDist = k, d
				}
			}
			i = best
		} else {
			i = int(math.Round(float64(j) * float64(P-1) / float64(S-1)))
		}
		i = clampInt(i, 0, P-1)
		if i == lastI {
			continue
		}
		lastI = i
		anchors = append(anchors, anchor{runeIdx: punctIdx[i], s: sil[j].startUS, e: sil[j].endUS})
	}
	return anchors
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// distributeByAnchors 以锚点为界把 runes 切成子区间，区间内匀速分布（复用估算思路），
// 停顿区间保留在锚点处（前区结束于 pause.s，后区开始于 pause.e）。
func distributeByAnchors(runes []rune, anchors []anchor, leadUS, speechEndUS int64) ([]TokenOffset, bool) {
	type seg struct {
		runeStart, runeEnd int // inclusive
		tStart, tEnd       int64
	}
	segs := make([]seg, 0, len(anchors)+1)
	prevRune, prevTime := -1, leadUS
	for _, a := range anchors {
		if a.runeIdx <= prevRune || a.s < prevTime || a.e <= a.s {
			return nil, false // 锚点破坏单调性：放弃（外部回退）
		}
		segs = append(segs, seg{runeStart: prevRune + 1, runeEnd: a.runeIdx, tStart: prevTime, tEnd: a.s})
		prevRune, prevTime = a.runeIdx, a.e
	}
	// 末锚点之后若还有字符，追加尾段。
	if prevRune+1 < len(runes) {
		if speechEndUS <= prevTime {
			return nil, false
		}
		segs = append(segs, seg{runeStart: prevRune + 1, runeEnd: len(runes) - 1, tStart: prevTime, tEnd: speechEndUS})
	}

	tokens := make([]TokenOffset, 0, len(runes))
	for _, sg := range segs {
		n := sg.runeEnd - sg.runeStart + 1
		if n < 1 || sg.tEnd <= sg.tStart {
			return nil, false
		}
		dur := sg.tEnd - sg.tStart
		usPer := dur / int64(n)
		if usPer <= 0 {
			return nil, false // 区间时长不足以容纳字符
		}
		start := sg.tStart
		for i := 0; i < n; i++ {
			end := start + usPer
			if i == n-1 {
				end = sg.tEnd // 余量并入最后一个字符，保证区间精确落点
			}
			r := runes[sg.runeStart+i]
			tokens = append(tokens, TokenOffset{StartUS: start, EndUS: end, Char: string(r)})
			start = end
		}
	}
	return tokens, true
}