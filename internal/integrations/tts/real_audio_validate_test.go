package tts

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestRealAudioAlignmentDiff 用真实的 CosyVoice2 分段音频对比老算法（静音锚点+匀速）与新算法
// （音节核锚定）。文件路径在 /tmp/opencode/realalign/，缺环境即跳过。
//
// 客观指标（都不依赖合成真值）：
//  1. 音节核数 m 是否接近字数 n（太少=检测失败回退恒速，太多=过切割）。
//  2. 逐字时长变异系数 CV：真实音节长短不一，贴合对齐应 CV 明显高于匀速分布。
//  3. "能量核心偏离"：每个字区间 [s,e) 内 RMS 峰值所处的归一化位置期望接近 0.5；
//     区间被系统性平移/错位时峰值会贴边。nuclei 应显著小于兜底匀速。
func TestRealAudioAlignmentDiff(t *testing.T) {
	dir := "/tmp/opencode/realalign"
	wavBytes, err := os.ReadFile(filepath.Join(dir, "seg01.wav"))
	if err != nil {
		t.Skipf("no real wav: %v", err)
	}
	text := strings.TrimSpace(mustReadFile(t, filepath.Join(dir, "seg01.txt")))
	durMS, err := wavDurationMSFromRIFF(wavBytes)
	if err != nil {
		t.Fatalf("duration: %v", err)
	}
	samples, sr, ch, ok := wavPCM16(wavBytes)
	if !ok {
		t.Fatalf("wav parse")
	}
	effRate := sr * ch
	_, leadUS, speechEndUS, ok := detectSilenceRuns(samples, effRate)
	if !ok {
		t.Fatalf("vad")
	}
	onsets := detectSyllableOnsets(samples, effRate, leadUS, speechEndUS)

	newA := buildEstimatedVADAlignment(text, wavBytes, durMS)
	oldA := buildEstimatedAudioAlignment(text, wavBytes, durMS, false)
	n := len([]rune(text))
	t.Logf("real segment: n=%d m(nuclei)=%d lead=%dms speechEnd=%dms dur=%dms new.method=%s",
		n, len(onsets), leadUS/1000, speechEndUS/1000, durMS, newA.Method)
	if len(onsets) < int(n-int(n/5)) {
		t.Logf("nuclei under-detection m=%d n=%d（CosyVoice 音节连续，按块匀速兜底属预期）", len(onsets), n)
	}
	if newA.Method != AlignEstimateVAD {
		t.Fatalf("new method = %s, want estimated_vad", newA.Method)
	}

	perDurStats := func(a Alignment, label string) (maxUS int64, cv float64) {
		ds := make([]int64, 0, len(a.Tokens))
		total := int64(0)
		for _, tok := range a.Tokens {
			d := tok.EndUS - tok.StartUS
			ds = append(ds, d)
			total += d
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		mean := float64(total) / float64(len(ds))
		var sq float64
		for _, d := range ds {
			sq += (float64(d) - mean) * (float64(d) - mean)
		}
		cv = math.Sqrt(sq/float64(len(ds))) / mean
		med := ds[len(ds)/2]
		t.Logf("  %-10s per-char ms: min=%d med=%d max=%d  CV=%.2f", label, ds[0]/1000, med/1000, ds[len(ds)-1]/1000, cv)
		return ds[len(ds)-1], cv
	}
	oldMax, _ := perDurStats(oldA, "old")
	newMax, newCV := perDurStats(newA, "new")
	// 回归防线：畸形块表现为单字时长爆炸 / 全长 CV 巨大（旧的 P2 曾到 max=1463ms、CV=1.06）。
	// 真实音频上按块匀速后应明显收敛。
	if newMax > 500*1000 {
		t.Errorf("new alignment still has degenerate long chars: max=%dms", newMax/1000)
	}
	if newCV > 0.6 {
		t.Errorf("new alignment still over-skewed durations: CV=%.2f", newCV)
	}
	_ = oldMax

	// 能量核心偏离：RMS 峰值在字区间内的归一化位置。
	peakOffset := func(a Alignment) float64 {
		const frameMS = 10
		frameLen := effRate * frameMS / 1000
		rms := make([]float64, len(samples)/frameLen)
		for i := range rms {
			f := samples[i*frameLen : (i+1)*frameLen]
			var acc int64
			for _, s := range f {
				acc += int64(s) * int64(s)
			}
			rms[i] = math.Sqrt(float64(acc) / float64(len(f)))
		}
		frameUS := int64(frameMS * 1000)
		var sum float64
		var cnt int
		for _, tok := range a.Tokens {
			lo, hi := int(tok.StartUS/frameUS), int((tok.EndUS)/frameUS)
			if hi <= lo {
				continue
			}
			bi := lo
			for k := lo + 1; k <= hi; k++ {
				if rms[k] > rms[bi] {
					bi = k
				}
			}
			i0, i1 := int64(tok.StartUS), int64(tok.EndUS)
			mid := i0 + (i1-i0)/2
			peak := int64(bi) * frameUS
			var off float64
			if i1 > i0 {
				off = math.Abs(float64(peak-mid)) / float64(i1-i0)
			}
			sum += off
			cnt++
		}
		return 100 * sum / float64(cnt) // % 偏离区间中点
	}
	oldPeak, newPeak := peakOffset(oldA), peakOffset(newA)
	t.Logf("  RMS 峰值偏离区间中点: old=%.1f%% new=%.1f%% (越小贴合越好)", oldPeak, newPeak)
	if newA.Method == AlignEstimateVAD && nucleiEvidence(n, onsetsIn(onsets, leadUS, speechEndUS)) {
		if newPeak >= oldPeak {
			t.Errorf("nucleus peak-centering did not improve: old=%.1f new=%.1f", oldPeak, newPeak)
		}
	}

	// 与线上当前缓存（P2 estimated_vad）对比逐字起点的差异规模。
	if b, err := os.ReadFile(filepath.Join(dir, "cached.json")); err == nil {
		var cached []TokenOffset
		if json.Unmarshal(b, &cached) == nil && len(cached) == len(newA.Tokens) {
			var tot, mx int64
			for i := range cached {
				d := newA.Tokens[i].StartUS - cached[i].StartUS
				if d < 0 {
					d = -d
				}
				tot += d
				if d > mx {
					mx = d
				}
			}
			t.Logf("  new vs cached(P2): MAE=%.1fms Max=%dms (差异来自音节核而非匀速)",
				float64(tot)/float64(len(cached))/1000, mx/1000)
		}
	}
}

func mustReadFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
