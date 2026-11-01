// Command audiogate 为「带音频 PPTX」真机门禁生成样本。
//
// 它走的是产品同一条写入路径（internal/integrations.GoPPTXNarrationWriter），
// 不是另写一份 XML 拼装：门禁要验的是产品写出来的文件，而不是脚本能拼出的文件。
//
// 与真实导出相比只有两点不同，都是为了让断言可判定：
//   - 音频用合成音（每页不同频率），而不是 TTS 结果：这样才能按频率区分
//     「这一页放的是不是自己的那段」，而不是只能确认"有声音"。
//   - 页面时长固定可预测，便于核对 advTm 与媒体时长是否一致。
//
// 源文件用仓库里的真实 deck（14 页、含图片与多级文本），不是合成的最小页：
// 修复提示往往由复杂版面触发，用最小页测出来的"能打开"证明力很弱。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/F31/ppts/internal/integrations"
)

// wavTone 生成一段 8kHz/8bit 单声道 PCM 正弦波。
// 8bit 8kHz 是 go-pptx 的音频探测最容易接受的组合，且体积小（约 8KB/s）。
func wavTone(freqHz, durationMS int) []byte {
	const (
		header     = 44
		sampleRate = 8000
	)
	n := sampleRate * durationMS / 1000
	buf := make([]byte, header+n)
	put32 := func(b []byte, v uint32) { b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24) }
	put16 := func(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }

	copy(buf[0:4], "RIFF")
	put32(buf[4:], uint32(header-8+n))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	put32(buf[16:], 16)
	put16(buf[20:], 1)          // PCM
	put16(buf[22:], 1)          // mono
	put32(buf[24:], sampleRate) // sample rate
	put32(buf[28:], sampleRate) // byte rate
	put16(buf[32:], 1)          // block align
	put16(buf[34:], 8)          // bits
	copy(buf[36:40], "data")
	put32(buf[40:], uint32(n))

	for i := 0; i < n; i++ {
		// 正弦比方波更不容易被客户端判为异常波形，且频率差异在听感上仍可分辨。
		v := 128 + int(100*math.Sin(2*math.Pi*float64(i)*float64(freqHz)/float64(sampleRate)))
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		buf[header+i] = byte(v)
	}
	return buf
}

type slidePlan struct {
	Index       int    `json:"index"`
	TrackKey    string `json:"trackKey"`
	HasAudio    bool   `json:"hasAudio"`
	FreqHz      int    `json:"freqHz,omitempty"`
	AudioMS     int    `json:"audioMs,omitempty"`
	AdvanceMS   int    `json:"advanceMs"`
	SilentPadMS int    `json:"silentPadMs,omitempty"`
}

func main() {
	src := flag.String("src", "internal/animation/animdefault/testdata/sample-customer-deck.pptx", "源 PPTX")
	out := flag.String("out", "", "输出 PPTX（必填）")
	planOut := flag.String("plan-out", "", "把写入计划写成 JSON 文件（供 verify 使用；不填则打到 stdout）")
	pages := flag.Int("pages", 14, "要配音的页数（从第 1 页起）")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "audiogate: -out is required")
		os.Exit(2)
	}

	source, err := os.ReadFile(*src)
	must(err, "read source")

	plan := integrations.NarrationPlan{}
	report := struct {
		Source  string      `json:"source"`
		Output  string      `json:"output"`
		Writer  string      `json:"writer"`
		Version string      `json:"version"`
		Slides  []slidePlan `json:"slides"`
	}{Source: *src, Output: *out}

	for i := 0; i < *pages; i++ {
		// 第 3 页故意留成静默页：真实 deck 里会有只有标题、没有讲稿的页。
		// 静默页仍必须设置停留时长，否则自动放映会瞬间跳过它。
		silent := i == 2
		sp := slidePlan{Index: i, TrackKey: fmt.Sprintf("narration:slide-%d", i+1)}

		// 时长在 2.0s–3.8s 之间变化，且每页不同：若客户端把音轨错配到别页，
		// 对象模型里的时长就对不上，不必靠耳朵听。
		audioMS := 2000 + (i%4)*600
		// 有一页留 700ms 静默尾巴：验证「音频比切页时长短」这条正常情况。
		// 其余页 advance 与音频等长。
		if i == 5 {
			audioMS = 2400
		}

		advanceMS := audioMS
		if i == 5 {
			advanceMS = audioMS + 700
			sp.SilentPadMS = 700
		}

		if silent {
			// 静默页：advance 取 1.5s，仍要自动切页。
			sp.HasAudio = false
			sp.AdvanceMS = 1500
			plan.Slides = append(plan.Slides, integrations.SlideNarration{
				Index: i, TrackKey: sp.TrackKey, Advance: 1500 * time.Millisecond,
			})
			report.Slides = append(report.Slides, sp)
			continue
		}

		freq := 420 + i*37
		sp.HasAudio = true
		sp.FreqHz, sp.AudioMS, sp.AdvanceMS = freq, audioMS, advanceMS
		plan.Slides = append(plan.Slides, integrations.SlideNarration{
			Index: i, TrackKey: sp.TrackKey,
			Audio:   wavTone(freq, audioMS),
			MIME:    "audio/wav",
			Advance: time.Duration(advanceMS) * time.Millisecond,
		})
		report.Slides = append(report.Slides, sp)
	}

	writer := integrations.NewGoPPTXNarrationWriter()
	rep, err := writer.Apply(context.Background(), bytes.NewReader(source), int64(len(source)), plan)
	must(err, "apply narration")
	if len(rep.Unsupported) > 0 {
		fmt.Fprintf(os.Stderr, "audiogate: writer reported unsupported: %v\n", rep.Unsupported)
		os.Exit(1)
	}
	must(os.WriteFile(*out, rep.Output, 0o644), "write output")

	report.Writer, report.Version = rep.Writer, rep.Version
	// 直接写文件而不是让调用方重定向：PowerShell 的 `>` 默认写 UTF-16，
	// Go 侧读出来就是一串 NUL。由本程序写，编码只有一种可能。
	var w io.Writer = os.Stdout
	var planFile *os.File
	if *planOut != "" {
		planFile, err = os.Create(*planOut)
		must(err, "create plan file")
		w = planFile
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	must(enc.Encode(report), "encode report")
	if planFile != nil {
		must(planFile.Close(), "close plan file")
		fmt.Fprintf(os.Stderr, "audiogate: plan written to %s\n", *planOut)
	}
}

func must(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "audiogate: %s: %v\n", what, err)
		os.Exit(1)
	}
}
