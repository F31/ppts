package tts

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
)

// 字幕对齐 SLO 的实测口径（技术方案 V4.0 §14.4）：
//
//	抽样句首偏差 P95 ≤ 200 ms，专业词与中英混读单列。
//
// 这里测的是「句首」（一段话第一个字出现的时刻），不是逐字平均误差。
// 两者会给出完全不同的结论：逐字 MAE 30ms 的算法，句首可能整体偏移
// 200ms —— 而观众感知到的正是"这句话什么时候开始"。
//
// 真值来源必须写明：本机没有人工标注的 CosyVoice 语料（需要付费合成 +
// 人工听标），所以这里用合成音节包络作为**受控真值**。它能验证算法的
// 定位能力，但不能替代上线前的人工标注验收；两者结论不一致时以人工为准。

const (
	// alignmentSLOP95MS 是句首偏差的 P95 门槛。
	alignmentSLOP95MS = 200.0
	// sloSampleRate 与生产 TTS 输出一致，避免用高低采样率差别掩盖问题。
	sloSampleRate = 16000
)

// onsetStats 收集句首偏差的**绝对值**。
// 用绝对值而不是带符号偏差：早 300ms 与晚 300ms 对观众的伤害一样。
type onsetStats struct {
	devs []float64
}

func (s *onsetStats) add(us int64) {
	s.devs = append(s.devs, math.Abs(float64(us))/1000.0)
}

// percentile 用 nearest-rank：样本量小的时候插值会给出"看起来更漂亮"
// 的 P95，而 nearest-rank 一定落在真实样本上，便于复核。
func (s *onsetStats) percentile(p float64) float64 {
	if len(s.devs) == 0 {
		return 0
	}
	sorted := append([]float64(nil), s.devs...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(p / 100.0 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func (s *onsetStats) max() float64 {
	if len(s.devs) == 0 {
		return 0
	}
	m := s.devs[0]
	for _, d := range s.devs {
		if d > m {
			m = d
		}
	}
	return m
}

func (s *onsetStats) count() int { return len(s.devs) }

// sentenceOnsetIndices 返回"句首"字符的下标：正文第一个非标点字符，
// 以及每个句末标点（。！？）之后的第一个非标点、非空白字符。
//
// 逗号后不算句首：那是同一个句子的内部停顿，字幕通常整句一起出现。
func sentenceOnsetIndices(text string) []int {
	runes := []rune(text)
	var out []int
	isBoundary := func(r rune) bool { return strings.ContainsRune("，。！？；：、", r) }
	for i, r := range runes {
		if i == 0 {
			if !isBoundary(r) && r != ' ' {
				out = append(out, i)
			}
			continue
		}
		prev := runes[i-1]
		if !strings.ContainsRune("。！？", prev) {
			continue
		}
		if isBoundary(r) || r == ' ' {
			continue
		}
		out = append(out, i)
	}
	return out
}

func mkPhrase(chars int, punct rune, j []float64) phrasePlan {
	return phrasePlan{chars: chars, punct: punct, perChar: 215.0, jitter: j}
}

func mkText(text string, punct rune, j []float64) phrasePlan {
	return phrasePlan{text: text, punct: punct, perChar: 215.0, jitter: j}
}

// noiseFloorAmp 是给合成音频加的确定性底噪幅度（±）。语音幅度 12000，
// 相当于约 -46dB 的信噪比——接近真实录音，又不至于把静音段判成语音。
//
// 为什么必须加：干净的合成静音是数字零，VAD 一测一个准，句首偏差会
// 恒等于 0。那种"全部 0.0ms"的 SLO 只证明语料是合成的，不证明算法抗噪。
const noiseFloorAmp = 50

// addNoiseFloor 叠加确定性底噪，保持真值不变（噪声不改变真实发声时刻）。
func addNoiseFloor(t *testing.T, u synthUtterance, amp int16) synthUtterance {
	t.Helper()
	samples, rate, ch, ok := wavPCM16(u.wav)
	if !ok {
		t.Fatalf("wav parse: %d bytes", len(u.wav))
	}
	state := uint32(0x9E3779B9)
	for i := range samples {
		state = state*1664525 + 1013904223
		n := int32(state>>16)%(2*int32(amp)+1) - int32(amp)
		v := int32(samples[i]) + n
		if v > 32767 {
			v = 32767
		}
		if v < -32768 {
			v = -32768
		}
		samples[i] = int16(v)
	}
	_ = ch
	u.wav = wav16FromSamples(t, rate, samples)
	return u
}

// sloCorpus 返回三组语料。lead 刻意在 120–340ms 之间变化：
// 算法内部有一个固定 lead 假设，只有真实 lead 偏离它，句首偏差才被真正检验。
func sloCorpus(t *testing.T) map[string][]synthUtterance {
	t.Helper()
	// 覆盖 60–680ms：真实 TTS 的段首静音跨度就有这么大（有的模型开头补
	// 200ms 静音，有的几乎不等）。把范围收窄会让"句首偏差"失去检验力 ——
	// 一个硬编码 200ms 的实现也能落在 200ms 门槛内。
	leads := []float64{60, 150, 200, 420, 680}

	// 每组 20 句：P95 在 20 个样本上是"第二大的那个"，再少就只是最大值。
	const perGroup = 20
	var regular, stress [][]phrasePlan
	for i := 0; i < perGroup; i++ {
		switch i % 4 {
		case 0:
			regular = append(regular, []phrasePlan{
				mkPhrase(7+i%5, '，', jitterReal), mkPhrase(9+(i*3)%7, '。', jitterReal)})
		case 1:
			regular = append(regular, []phrasePlan{
				mkPhrase(6, '，', jitterReal), mkPhrase(7+i%4, '，', jitterReal), mkPhrase(11, '。', jitterReal)})
		case 2:
			regular = append(regular, []phrasePlan{
				mkPhrase(16+i%3, '。', jitterReal), mkPhrase(13, '。', jitterReal)})
		default:
			regular = append(regular, []phrasePlan{
				mkPhrase(10, '，', jitterReal), mkPhrase(12, '。', jitterReal), mkPhrase(8+i%5, '。', jitterReal)})
		}
		switch i % 3 {
		case 0:
			stress = append(stress, []phrasePlan{
				mkPhrase(12, '，', jitterStress), mkPhrase(14, '。', jitterStress)})
		case 1:
			stress = append(stress, []phrasePlan{
				mkPhrase(6, '，', jitterStress), mkPhrase(7+i%4, '，', jitterStress), mkPhrase(8, '。', jitterStress)})
		default:
			stress = append(stress, []phrasePlan{
				mkPhrase(20+i%6, '。', jitterStress)})
		}
	}
	// 中英混读 / 专业词：型号、单位、版本号与英文单词连读。
	// 这是 V4.0 明确要求单列的一组——TTS 对它们的分词与时长分布
	// 与纯中文不同，混在一起统计会把差异平均掉。
	var mixed [][]phrasePlan
	for i := 0; i < perGroup; i++ {
		switch i % 5 {
		case 0:
			mixed = append(mixed, []phrasePlan{
				mkText(fmt.Sprintf("PCIe %d.0 x16 链路", 4+i%3), '，', jitterReal),
				mkText(fmt.Sprintf("实测 %d GB/s", 2039+i*7), '。', jitterReal)})
		case 1:
			mixed = append(mixed, []phrasePlan{
				mkText(fmt.Sprintf("HBM3 3.%02dTB/s 带宽", i), '。', jitterReal)})
		case 2:
			mixed = append(mixed, []phrasePlan{
				mkText(fmt.Sprintf("A100-SXM%d-80GB", 4+i%2), '，', jitterReal),
				mkText(fmt.Sprintf("NVLink %d GB/s", 900+i), '。', jitterReal)})
		case 3:
			mixed = append(mixed, []phrasePlan{
				mkText(fmt.Sprintf("Throughput reached %d GB/s", 2039+i*13), '。', jitterReal)})
		default:
			mixed = append(mixed, []phrasePlan{
				mkText(fmt.Sprintf("2024-03-%02d 发布", 10+i%9), '，', jitterReal),
				mkText(fmt.Sprintf("延迟 %d.%d ms", 1+i%3, i%10), '。', jitterReal)})
		}
	}

	build := func(plans [][]phrasePlan) []synthUtterance {
		out := make([]synthUtterance, 0, len(plans))
		for i, p := range plans {
			u := synthFromPlanLead(t, sloSampleRate, p, leads[i%len(leads)])
			out = append(out, addNoiseFloor(t, u, noiseFloorAmp))
		}
		return out
	}
	return map[string][]synthUtterance{
		"常规中文":       build(regular),
		"压力抖动(±16%)": build(stress),
		"中英混读/专业词":   build(mixed),
	}
}

// TestAlignmentSLOSentenceOnsetP95 是字幕对齐 SLO 的实测用例。
func TestAlignmentSLOSentenceOnsetP95(t *testing.T) {
	corpus := sloCorpus(t)
	// 组名固定顺序，日志与结论可复核；map 遍历顺序随机会让两次运行对不上。
	order := []string{"常规中文", "压力抖动(±16%)", "中英混读/专业词"}

	all := &onsetStats{}
	t.Logf("字幕对齐 SLO：句首偏差 P95 ≤ %.0fms（合成音节包络受控真值）", alignmentSLOP95MS)
	t.Logf("%-20s %6s %8s %8s %8s", "分组", "样本", "P50(ms)", "P95(ms)", "Max(ms)")

	for _, name := range order {
		utts, ok := corpus[name]
		if !ok || len(utts) == 0 {
			t.Fatalf("分组 %q 没有语料：空语料会让 SLO 变成真空通过", name)
		}
		stats := &onsetStats{}
		for _, u := range utts {
			got := buildEstimatedVADAlignment(u.text, u.wav, u.durationMS)
			if len(got.Tokens) != len(u.truthStartUS) {
				t.Fatalf("token 数 %d != 真值 %d（text=%q）", len(got.Tokens), len(u.truthStartUS), u.text)
			}
			onsets := sentenceOnsetIndices(u.text)
			if len(onsets) == 0 {
				t.Fatalf("语料没有句首：%q", u.text)
			}
			for _, idx := range onsets {
				stats.add(got.Tokens[idx].StartUS - u.truthStartUS[idx])
				all.add(got.Tokens[idx].StartUS - u.truthStartUS[idx])
			}
		}
		t.Logf("%-20s %6d %8.1f %8.1f %8.1f", name, stats.count(),
			stats.percentile(50), stats.percentile(95), stats.max())
		if p95 := stats.percentile(95); p95 > alignmentSLOP95MS {
			t.Errorf("分组 %s 句首偏差 P95 = %.1fms，超过 SLO %.0fms", name, p95, alignmentSLOP95MS)
		}
	}

	t.Logf("%-20s %6d %8.1f %8.1f %8.1f", "全部", all.count(),
		all.percentile(50), all.percentile(95), all.max())
	if all.count() < 30 {
		// 样本太少时 P95 只是"最大的那一个"，称不上分位数。
		t.Fatalf("句首样本只有 %d 个，不足以谈 P95", all.count())
	}
	if p95 := all.percentile(95); p95 > alignmentSLOP95MS {
		t.Errorf("全量句首偏差 P95 = %.1fms，超过 SLO %.0fms", p95, alignmentSLOP95MS)
	}
}
