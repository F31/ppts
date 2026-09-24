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
const vadMinSilenceMS = 40

// vadSilenceRel 是相对峰值能量的静音阈值（帧 RMS < peak*此值 判为静音）。
const vadSilenceRel = 0.08

// vadSilenceAbs 是绝对静音下限（int16 幅度）；低于它即使相对阈值也判静音，用于处理近纯静音。
const vadSilenceAbs = 40.0

// silenceRange 是检测到的内部静音区间（微秒，相对音频起点）。
type silenceRange struct {
	startUS int64
	endUS   int64
}

// 音节核锚定（P3）参数：对块内能量包络做峰/谷检测，把字符边界锚到真实音节起始。
// 仅用音频本身（能量包络），不依赖任何本地/远程模型。
const (
	syllableFrameMS      = 12.0  // 音节能量分析的帧长（越小定位越细，代价是噪感）
	syllableMinSpacingMS = 60.0  // 两个音节核的最短间隔；过近视为同一音节/伪峰
	syllablePeakRel      = 0.18  // 相对峰值能量的音节核门槛（低于此的帧不成核）
	syllableAbsFloor     = 120.0 // 绝对门槛（int16 幅度），纯静音附近无效值
	syllableMaxOvershoot = 2     // 核数允许超过字数的上限（绝对量），超出视为检测噪声
	syllableFloorRel     = 0.10  // 有声地板：低于此判为"离开本音节"（静音/前一音节残响），回退起点止步
)

// anchorPt 是"字符下标 → 起始时刻"折线锚点，用于在锚点间做保序插值。
type anchorPt struct {
	c int
	t int64
}

// buildEstimatedVADAlignment 由真实 WAV 音频构造"静音锚点 + 音节核"估算对齐；不符合条件时
// 回退到 buildEstimatedAlignment（Method=AlignEstimate）。返回的 tokens 以音频起点为基准
// （含首静音在前，逐字 EndUS 不越过 speechEndUS/durationMS 对应时刻）。
func buildEstimatedVADAlignment(text string, wav []byte, durationMS int64) Alignment {
	return buildEstimatedAudioAlignment(text, wav, durationMS, true)
}

// buildEstimatedAudioAlignment 是 buildEstimatedVADAlignment 的核心实现；withNuclei 为 false
// 时退化为旧的"静音锚点 + 区间内匀速"（用于量化对比的对照组）。
func buildEstimatedAudioAlignment(text string, wav []byte, durationMS int64, withNuclei bool) Alignment {
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
	if !ok {
		return fallback() // 纯静音：无可用语音
	}
	runes := []rune(text)
	if len(runes) == 0 {
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

	var onsets []int64
	if withNuclei {
		onsets = detectSyllableOnsets(samples, effRate, leadUS, speechEndUS)
	}
	wholeSpan := func() (Alignment, bool) {
		if win := onsetsIn(onsets, leadUS, speechEndUS); nucleiEvidence(len(runes), win) {
			if tokens, ok := distributeByOnsets(runes, leadUS, speechEndUS, win); ok {
				return Alignment{Text: text, Tokens: tokens, Method: AlignEstimateVAD}, true
			}
		}
		return Alignment{}, false
	}

	punctIdx := pausePunctIndexes(runes)
	var kept []anchor
	if len(punctIdx) > 0 {
		anchors := mapPunctToSilence(punctIdx, len(runes), runs, leadUS, speechEndUS)
		// 仅保留严格落在 (leadUS, speechEndUS) 内且自身合法的锚点；过滤掉会撑爆区间的锚。
		for _, a := range anchors {
			if a.s >= leadUS && a.e <= speechEndUS && a.s < a.e {
				kept = append(kept, a)
			}
		}
	}
	// 无有效锚点（无内部停顿 / 无标点 / 映射失败）时尝试整段音节核，否则整体匀速。
	if len(kept) == 0 {
		if a, ok := wholeSpan(); ok {
			return a
		}
		return fallback()
	}
	tokens, ok := distributeByAnchors(runes, kept, leadUS, speechEndUS, onsets)
	if !ok || len(tokens) != len(runes) {
		if a, ok := wholeSpan(); ok {
			return a
		}
		return fallback()
	}
	return Alignment{Text: text, Tokens: tokens, Method: AlignEstimateVAD}
}

// pausePunctIndexes 返回 runes 中"可停顿标点"的下标（末尾标点不参与锚定：其后无字符，
// 其停顿会与音频尾部静音合并，锚了反而把最后一个停顿错配到前面的标点上）。
func pausePunctIndexes(runes []rune) []int {
	var out []int
	last := len(runes) - 1
	for i, r := range runes {
		if i == last {
			break
		}
		if isPausePunct(r) {
			out = append(out, i)
		}
	}
	return out
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
//   - runs：音频**内部**的静音区间（不含首尾静音段），时长 >= vadMinSilenceMS（可能为空）；
//   - leadUS / speechEndUS：首末语音帧起止时刻（微秒）；
//   - ok：音频含可用的语音（有语音帧；纯静音时为 false）。runs 为空不代表 ok=false——
//     长句无内部停顿是合法的"无锚点但有语音"情形，仍需走音节核/匀速候选。
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
		return runs, leadUS, speechEndUS, true // 无内部停顿但有语音：仍有效
	}
	return runs, leadUS, speechEndUS, true
}

// isPausePunct 判断一个字符是否代表"会带来停顿/换气"的句读。仅保留口语里真正断句的标点：
// 顿号、冒号等枚举/冒读符号在 TTS 中并不停顿，若参与锚定反而把正常的顺读短语错误切成块。
func isPausePunct(r rune) bool {
	switch r {
	case '。', '，', '；', '！', '？', '…', '.', ',', ';', '!', '?':
		return true
	}
	return false
}

// anchor 把一个停顿映射到文本标点：pause 之后为停顿区间 [s,e]。
type anchor struct {
	runeIdx int
	s, e    int64
}

// mapPunctToSilence 把"文本标点"单调地匹配到"静音区间"，只锚置信的对应，其余宁可整块
// 回退也不要错配。旧的等比抽样把"停顿数量≠标点数量"的音频胡乱切块，产生 900ms 吃到一字、
// 或把一整个短语压成每字 10ms 的畸形。
//
// 匹配依据：按字符数线性预估的"该标点应到时刻"（TTS 中文大体逐字等时）与静音中点的归一化
// 距离。即使标点与停顿数量不等，也能各自跳过、只成对距离足够近的项。
func mapPunctToSilence(punctIdx []int, totalRunes int, sil []silenceRange, leadUS, speechEndUS int64) []anchor {
	P, S := len(punctIdx), len(sil)
	if P == 0 || S == 0 {
		return nil
	}
	span := speechEndUS - leadUS
	if span <= 0 {
		return nil
	}
	expectedUS := func(pi int) int64 {
		f := float64(punctIdx[pi]) / float64(max(totalRunes-1, 1))
		return leadUS + int64(f*float64(span))
	}
	silMid := func(j int) int64 { return (sil[j].startUS + sil[j].endUS) / 2 }

	// 归一化距离的平方与收益；落在 reward 半径内的对应才会被采用。
	const reward = 0.12 * 0.12
	w := func(i, j int) float64 {
		d := float64(expectedUS(i)-silMid(j)) / float64(span)
		return reward - d*d
	}

	// DP f(i,j) = 前 i 个标点、前 j 个静音的单调最优收益（允许任意单向跳过）。
	vals := make([][]float64, P+1)
	ch := make([][]byte, P+1) // 1=跳过标点 2=跳过静音 3=匹配
	for i := 0; i <= P; i++ {
		vals[i] = make([]float64, S+1)
		ch[i] = make([]byte, S+1)
	}
	for i := 1; i <= P; i++ {
		for j := 1; j <= S; j++ {
			up, le := vals[i-1][j], vals[i][j-1]
			diag := vals[i-1][j-1] + w(i-1, j-1)
			switch {
			case diag >= up && diag >= le:
				vals[i][j], ch[i][j] = diag, 3
			case up >= le:
				vals[i][j], ch[i][j] = up, 1
			default:
				vals[i][j], ch[i][j] = le, 2
			}
		}
	}
	i, j := P, S
	type pair struct{ pIdx, sIdx int }
	var pairs []pair
	for i > 0 && j > 0 {
		switch ch[i][j] {
		case 1:
			i--
		case 2:
			j--
		default:
			pairs = append(pairs, pair{pIdx: i - 1, sIdx: j - 1})
			i--
			j--
		}
	}
	anchors := make([]anchor, 0, len(pairs))
	for k := len(pairs) - 1; k >= 0; k-- {
		p := pairs[k]
		anchors = append(anchors, anchor{runeIdx: punctIdx[p.pIdx], s: sil[p.sIdx].startUS, e: sil[p.sIdx].endUS})
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

// distributeByAnchors 以锚点为界把 runes 切成子区间。每个块的导通时序用"音节核"在真实音频上
// 定位（distributeBlock），停顿区间保留在锚点处（前区结束于 pause.s，后区开始于 pause.e）。
// 注意：块对音节的"归属窗口"用**停顿中点**（而非停顿端点）划分——停顿端点的帧量化会把
// 下一块第一个音节裁掉，导致整块后移一个音节。
func distributeByAnchors(runes []rune, anchors []anchor, leadUS, speechEndUS int64, onsets []int64) ([]TokenOffset, bool) {
	type seg struct {
		runeStart, runeEnd int // inclusive
		tStart, tEnd       int64
		winLo, winHi       int64
	}
	prevRune := -1
	prevCut := leadUS
	prevTime := leadUS
	tokens := make([]TokenOffset, 0, len(runes))
	flush := func(sg seg) bool {
		n := sg.runeEnd - sg.runeStart + 1
		if n < 1 || sg.tEnd <= sg.tStart {
			return false
		}
		segTokens, ok := distributeBlock(runes[sg.runeStart:sg.runeEnd+1], sg.tStart, sg.tEnd, sg.winLo, sg.winHi, onsets)
		if !ok {
			return false
		}
		tokens = append(tokens, segTokens...)
		return true
	}
	for _, a := range anchors {
		mid := (a.s + a.e) / 2
		if a.runeIdx <= prevRune || a.s < prevTime || a.e <= a.s {
			return nil, false // 锚点破坏单调性：放弃（外部回退）
		}
		if !flush(seg{runeStart: prevRune + 1, runeEnd: a.runeIdx, tStart: prevTime, tEnd: a.s, winLo: prevCut, winHi: mid}) {
			return nil, false
		}
		prevRune, prevTime, prevCut = a.runeIdx, a.e, mid
	}
	// 末锚点之后若还有字符，追加尾段。
	if prevRune+1 < len(runes) {
		if speechEndUS <= prevTime {
			return nil, false
		}
		if !flush(seg{runeStart: prevRune + 1, runeEnd: len(runes) - 1, tStart: prevTime, tEnd: speechEndUS, winLo: prevCut, winHi: speechEndUS}) {
			return nil, false
		}
	}
	if len(tokens) != len(runes) {
		return nil, false
	}
	return tokens, true
}

// charWeight 估算一个字符的"朗读时长权重"：CJK>数字>拉丁字母>空白>标点。用于在锚点区间内
// 按权重而非纯等分分配时长，减小中英混排/标点造成的字级漂移。
func charWeight(r rune) float64 {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // CJK 统一表意
		return 1.0
	case r >= '0' && r <= '9':
		return 0.6
	case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		return 0.55
	case r == ' ' || r == '\t' || r == '\n':
		return 0.3
	case isPausePunct(r):
		return 0.05 // 句读几乎不占用朗读时间，把时间让给实词
	default:
		return 0.6
	}
}

// allocateWeighted 在 [tStart,tEnd] 内按字符权重分配时长，保证下标与区间精确落点、
// 逐字 EndUS 单调不减且不越界（每个字符至少 1µs）。
func allocateWeighted(runes []rune, tStart, tEnd int64) ([]TokenOffset, bool) {
	n := len(runes)
	dur := tEnd - tStart
	if n == 0 || dur < int64(n) {
		return nil, false
	}
	weights := make([]float64, n)
	var sum float64
	for i, r := range runes {
		weights[i] = charWeight(r)
		sum += weights[i]
	}
	if sum <= 0 {
		return nil, false
	}
	tokens := make([]TokenOffset, 0, n)
	cur := tStart
	remain := dur
	for i := 0; i < n; i++ {
		var d int64
		if i == n-1 {
			d = remain
		} else {
			d = int64(math.Round(float64(dur) * weights[i] / sum))
			if d < 1 {
				d = 1
			}
			if capLeft := remain - int64(n-1-i); d > capLeft { // 给后续每字至少留 1µs
				d = capLeft
			}
		}
		if d < 1 {
			d = 1
		}
		tokens = append(tokens, TokenOffset{StartUS: cur, EndUS: cur + d, Char: string(runes[i])})
		cur += d
		remain -= d
	}
	if cur != tEnd {
		return nil, false
	}
	return tokens, true
}

// RecomputeAlignment 用已缓存的音频与文本重新生成字级对齐（不重新合成音频）。用于对齐算法
// 升级后对既有缓存做"仅重算对齐"的迁移：音频非 PCM16 WAV / VAD 失败时回退为整体估算。
func RecomputeAlignment(text string, wav []byte, durationMS int64) Alignment {
	return buildEstimatedVADAlignment(text, wav, durationMS)
}

// detectSyllableOnsets 在 [startUS,endUS] 窗口内检测"音节起始"（相邻音节间的能量谷底）。
// 返回升序微秒时刻，供字符边界锚定。纯能量包络方法：帧 RMS → 邻域平滑 → 局部峰 →
// 回退到峰前谷底。检测不可靠时返回较少/空集，由上层证据检查（distributeBlock）兜底。
func detectSyllableOnsets(samples []int16, sampleRate int, startUS, endUS int64) []int64 {
	if sampleRate <= 0 || startUS >= endUS {
		return nil
	}
	frameLen := int(float64(sampleRate) * syllableFrameMS / 1000.0)
	if frameLen <= 0 {
		return nil
	}
	nFrames := len(samples) / frameLen
	if nFrames < 3 {
		return nil
	}
	frameDurUS := int64(syllableFrameMS * 1000)

	rms := make([]float64, nFrames)
	peak := 0.0
	for i := 0; i < nFrames; i++ {
		f := samples[i*frameLen : (i+1)*frameLen]
		var acc int64
		for _, s := range f {
			acc += int64(s) * int64(s)
		}
		r := math.Sqrt(float64(acc) / float64(len(f)))
		rms[i] = r
		if r > peak {
			peak = r
		}
	}
	if peak <= 0 {
		return nil
	}
	// 邻域平滑，抑制同一音节内的噪声抖点。
	sm := make([]float64, nFrames)
	for i := 0; i < nFrames; i++ {
		lo, hi := maxInt(0, i-1), minInt(nFrames-1, i+1)
		sum := 0.0
		for k := lo; k <= hi; k++ {
			sum += rms[k]
		}
		sm[i] = sum / float64(hi-lo+1)
	}

	loF := int(float64(startUS) / float64(frameDurUS))
	hiF := int(float64(endUS) / float64(frameDurUS))
	if loF < 0 {
		loF = 0
	}
	if hiF > nFrames-1 {
		hiF = nFrames - 1
	}
	if hiF <= loF {
		return nil
	}
	thresh := peak * syllablePeakRel
	if thresh < syllableAbsFloor {
		thresh = syllableAbsFloor
	}
	silFloor := peak * syllableFloorRel
	if silFloor < syllableAbsFloor {
		silFloor = syllableAbsFloor
	}
	minGapUS := int64(syllableMinSpacingMS * 1000)

	var onsets []int64
	prevPeakF := -1
	for i := loF; i <= hiF; i++ {
		if sm[i] < thresh {
			continue
		}
		if i > loF && sm[i] < sm[i-1] {
			continue
		}
		if i < hiF && sm[i] < sm[i+1] {
			continue
		}
		// 过近的核：保留能量更大者（模拟音节收紧而非两个音节）。
		if prevPeakF >= 0 && int64(i-prevPeakF)*frameDurUS < minGapUS {
			if sm[i] > sm[prevPeakF] {
				prevPeakF = i
			}
			continue
		}
		// 回退到峰前能量谷底作为音节起始（保序）；但不得越过"有声地板"穿进前一个音节/
		// 前一个爆发或静音——否则会把块边界前一个片段误当成本音节起点。
		valley := i
		for valley > loF && sm[valley] >= silFloor && sm[valley-1] <= sm[valley] {
			valley--
		}
		onset := int64(valley) * frameDurUS
		if onset < startUS {
			onset = startUS
		}
		if onset >= endUS {
			break
		}
		if len(onsets) > 0 && onset-onsets[len(onsets)-1] < minGapUS {
			continue
		}
		onsets = append(onsets, onset)
		prevPeakF = i
	}
	return onsets
}

// distributeBlock 分配一个锚点块在 [tStart,tEnd] 内的 word 级时间。音节核的采集窗口为
// [winLo,winHi)（停顿中点界定的归属区间，避免端点量化裁掉块首音节），证据足够时按核锚定，
// 否则回退到按权重匀速（allocateWeighted）。
func distributeBlock(runes []rune, tStart, tEnd, winLo, winHi int64, onsets []int64) ([]TokenOffset, bool) {
	n := len(runes)
	if n < 1 || tEnd <= tStart {
		return nil, false
	}
	win := onsetsIn(onsets, winLo, winHi)
	if nucleiEvidence(n, win) {
		if tokens, ok := distributeByOnsets(runes, tStart, tEnd, win); ok && len(tokens) == n {
			return tokens, true
		}
	}
	return allocateWeighted(runes, tStart, tEnd)
}

// onsetsIn 返回 onsets 落在 [lo,hi) 内的子序列（保序）。
func onsetsIn(onsets []int64, lo, hi int64) []int64 {
	out := onsets[:0:0]
	for _, o := range onsets {
		if o >= lo && o < hi {
			out = append(out, o)
		}
	}
	return out
}

// nucleiEvidence 判断音节核证据是否足以支撑按核锚定。要求核数接近字数（≥80% 且不过量），
// 否则不要用不完整的核映射（真实 TTS 音节常弱化/被检测漏，m 明显小于 n 时宁可用匀速）。
func nucleiEvidence(n int, win []int64) bool {
	if len(win) == 0 {
		return false
	}
	minNuclei := 2
	if need := int(math.Ceil(0.80 * float64(n))); need > minNuclei {
		minNuclei = need
	}
	return len(win) >= minNuclei && len(win) <= n+syllableMaxOvershoot
}

// distributeByOnsets 用检测到的音节起始时刻锚定字符起点；核数与字数不等时在锚点之间做保序
// 折线插值/抽稀，保证单调、覆盖 [tStart,tEnd]、逐字至少 1µs、末字符 EndUS==tEnd。
func distributeByOnsets(runes []rune, tStart, tEnd int64, onsets []int64) ([]TokenOffset, bool) {
	n := len(runes)
	m := len(onsets)
	if n < 1 || m < 1 || tEnd <= tStart {
		return nil, false
	}
	if n == 1 {
		return []TokenOffset{{StartUS: tStart, EndUS: tEnd, Char: string(runes[0])}}, true
	}
	starts := make([]int64, n)

	if m == 1 {
		// 单核：第一个字符锚到该核，后续字符在剩余区间内尽量均摊。
		c0 := clampOnset(onsets[0], tStart, tEnd-int64(n-1))
		step := (tEnd - c0) / int64(n-1)
		if step < 1 {
			step = 1
		}
		for i := 0; i < n; i++ {
			if i == 0 {
				starts[i] = c0
				continue
			}
			v := c0 + step*int64(i)
			if capL := tEnd - int64(n-1-i); v > capL {
				v = capL
			}
			starts[i] = v
		}
	} else {
		// 折线锚点：(0 → onsets[0])、(round(j*(n-1)/(m-1)) → onsets[j])、末点收到 tEnd。
		pts := make([]anchorPt, 0, m+2)
		pts = append(pts, anchorPt{c: 0, t: clampOnset(onsets[0], tStart, tEnd-int64(n-1))})
		for j := 1; j < m; j++ {
			ci := int(math.Round(float64(j) * float64(n-1) / float64(m-1)))
			if ci < pts[len(pts)-1].c+1 {
				continue
			}
			if ci > n-1 {
				ci = n - 1
			}
			t := clampOnset(onsets[j], pts[len(pts)-1].t+1, tEnd-int64(n-1-ci))
			if t <= pts[len(pts)-1].t {
				continue
			}
			pts = append(pts, anchorPt{c: ci, t: t})
		}
		if pts[len(pts)-1].c < n-1 {
			pts = append(pts, anchorPt{c: n - 1, t: tEnd - 1})
		} else if pts[len(pts)-1].t >= tEnd {
			pts[len(pts)-1].t = tEnd - 1
		}
		for i := 0; i < n; i++ {
			starts[i] = lerpAnchors(pts, i, tStart, tEnd)
		}
	}

	// 单调化 + 边界约束（每字至少 1µs，末字起点早于 tEnd）。
	for i := 0; i < n; i++ {
		lo := tStart
		if i > 0 {
			lo = starts[i-1] + 1
		}
		if starts[i] < lo {
			starts[i] = lo
		}
		if capL := tEnd - int64(n-1-i); starts[i] > capL {
			starts[i] = capL
		}
	}
	if starts[0] < tStart || starts[n-1] >= tEnd {
		return nil, false
	}

	tokens := make([]TokenOffset, 0, n)
	for i := 0; i < n; i++ {
		end := tEnd
		if i < n-1 {
			end = starts[i+1]
			if end <= starts[i] {
				end = starts[i] + 1
			}
			if end > tEnd {
				end = tEnd
			}
		}
		if end <= starts[i] {
			return nil, false
		}
		tokens = append(tokens, TokenOffset{StartUS: starts[i], EndUS: end, Char: string(runes[i])})
	}
	return tokens, true
}

// lerpAnchors 在折线锚点间做保序线性插值；ci 在锚点范围外（末端）用最后一段斜率外推并收束。
func lerpAnchors(pts []anchorPt, ci int, tStart, tEnd int64) int64 {
	if len(pts) == 0 {
		return tStart
	}
	if len(pts) == 1 {
		v := pts[0].t
		if v < tStart {
			return tStart
		}
		return v
	}
	k := 0
	for k+1 < len(pts) && pts[k+1].c <= ci {
		k++
	}
	if k+1 >= len(pts) {
		v := pts[len(pts)-1].t
		if v < tStart {
			return tStart
		}
		return v
	}
	p0, p1 := pts[k], pts[k+1]
	if p1.c == p0.c {
		return p0.t
	}
	ratio := float64(ci-p0.c) / float64(p1.c-p0.c)
	v := int64(float64(p0.t) + ratio*float64(p1.t-p0.t))
	if v < tStart {
		v = tStart
	}
	if v > tEnd {
		v = tEnd
	}
	return v
}

func clampOnset(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
