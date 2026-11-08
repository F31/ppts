// share-verify 对成品包做结构校验、外链扫描与体积构成统计。
//
// 接受 zip 或目录（隐私模式产物是目录），两者走同一套 Manifest.Validate 与
// ScanExternalRefs——CI 里对构建出的样例包跑一遍，就能拦住"有人给播放器加了
// CDN 字体/遥测脚本"这类回归。
//
// 退出码：0 通过；1 结构不合规；2 检出外链（可用 -allow-external 降级为警告）；
// 3 超体积预算（仅 -budget 时）。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/F31/ppts/internal/share"
)

type result struct {
	Target        string `json:"target"`
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schemaVersion"`
	Slides        int    `json:"slides"`
	AudioSegments int    `json:"audioSegments"`
	AudioMix      bool   `json:"audioMix"`
	// AudioCodec 是混音轨的实际编码（aac / pcm / none）。体积结论随它而变：
	// 同样的包在装了 ffmpeg 的机器上小 6 倍，只报一个总体积会让人以为「这版变好了」。
	AudioCodec   string `json:"audioCodec"`
	DurationUS   int64  `json:"durationUs"`
	Entries      int    `json:"entries"`
	TotalBytes   int64  `json:"totalBytes"`
	ExternalRefs int    `json:"externalRefs"`
	// ExternalRefSamples 给出「哪个条目里的哪一段」。只报数量的话排查要从头 grep。
	ExternalRefSamples map[string][]string `json:"externalRefSamples,omitempty"`
	Measure            share.Measurement   `json:"measure"`
	// Font 是内嵌字体的覆盖率结论（V15）。Embedded 为 false 时其余字段无意义——
	// 那是「这份包没有内嵌字体」，与「内嵌了但缺字」是两件事。
	Font share.FontCoverage `json:"font"`
}

func main() {
	jsonOut := flag.Bool("json", false, "以 JSON 输出")
	allowExternal := flag.Bool("allow-external", false, "检出外链时只警告，不返回失败")
	checkBudget := flag.Bool("budget", false, "超体积预算时返回失败（默认只报告）")
	checkFont := flag.Bool("font", false, "内嵌字体覆盖率不足时返回失败（默认只报告）")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "用法：share-verify [-json] [-allow-external] [-budget] [-font] <bundle.zip|bundle-dir>\n")
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(1)
	}
	target := flag.Arg(0)

	manifest, files, kind, err := load(target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "share-verify: %v\n", err)
		os.Exit(1)
	}

	measure := share.MeasureFiles(files, manifest)
	fontRep, fontErr := share.CheckEmbeddedFont(manifest, files)
	res := result{
		Target: target, Kind: kind,
		SchemaVersion:      manifest.SchemaVersion,
		Slides:             len(manifest.Slides),
		AudioSegments:      len(manifest.Audio),
		AudioMix:           manifest.AudioMix != "",
		AudioCodec:         audioCodec(manifest.AudioMix),
		DurationUS:         manifest.Artifact.DurationUS,
		Entries:            len(files),
		TotalBytes:         measure.TotalBytes,
		ExternalRefs:       share.CountExternalRefs(files),
		ExternalRefSamples: share.ExternalRefSamples(files),
		Measure:            measure,
		Font:               fontRep,
	}
	if fontErr != nil {
		// 结构性问题（声明了字体却没文件 / 字体解析不了）直接失败：
		// 这不是「覆盖率不足」，而是包自相矛盾，浏览器拿到也加载不了。
		fmt.Fprintf(os.Stderr, "share-verify: %v\n", fontErr)
		os.Exit(1)
	}

	if *jsonOut {
		raw, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(raw))
	} else {
		printResult(res)
	}

	if res.ExternalRefs > 0 {
		if *allowExternal {
			fmt.Fprintf(os.Stderr, "share-verify: 警告：检出 %d 处外链（-allow-external 已放行）\n", res.ExternalRefs)
		} else {
			fmt.Fprintf(os.Stderr, "share-verify: 失败：检出 %d 处外链，成品包必须可离线播放\n", res.ExternalRefs)
			os.Exit(2)
		}
	}
	if *checkBudget && measure.OverBudget {
		fmt.Fprintf(os.Stderr, "share-verify: 失败：体积 %.2f MiB 超出预算 %.2f MiB\n",
			float64(measure.TotalBytes)/(1<<20), float64(measure.BudgetBytes)/(1<<20))
		os.Exit(3)
	}
	if *checkFont && res.Font.Embedded && len(res.Font.Missing) > 0 {
		fmt.Fprintf(os.Stderr, "share-verify: 失败：内嵌字体缺 %d 个片中字符（首个 %q）\n",
			len(res.Font.Missing), string(res.Font.Missing[:min(len(res.Font.Missing), 8)]))
		os.Exit(4)
	}
}

func load(target string) (*share.Manifest, map[string][]byte, string, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, nil, "", err
	}
	if info.IsDir() {
		m, files, err := share.VerifyDir(target)
		return m, files, "dir", err
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, nil, "", err
	}
	m, files, err := share.VerifyBundle(data)
	return m, files, "zip", err
}

var kindLabels = map[string]string{
	share.KindPages:     "页图",
	share.KindLayers:    "层图",
	share.KindAudio:     "音频",
	share.KindPlayer:    "播放器",
	share.KindFont:      "字体",
	share.KindSubtitles: "字幕",
	share.KindManifest:  "清单",
	share.KindOther:     "其它",
}

// audioCodec 由混音轨的文件名给出实际编码。
//
// 不读文件头：清单里的 audioMix 就是播放器用它去请求的那条路径，二者必须一致；
// 用扩展名判断也让「清单说 m4a、包里却是 wav」这类不一致在报告里直接可见。
func audioCodec(mix string) string {
	switch {
	case mix == "":
		return "none"
	case strings.HasSuffix(strings.ToLower(mix), ".m4a"):
		return "aac（" + mix + "）"
	case strings.HasSuffix(strings.ToLower(mix), ".wav"):
		return "pcm（" + mix + "，未转码）"
	default:
		return mix
	}
}

func printResult(r result) {
	m := r.Measure
	fmt.Printf("目标：%s（%s）\n", r.Target, r.Kind)
	fmt.Printf("清单：schema v%d，%d 页（含动画 %d 页 / %d 层），%d 段音频，混音轨=%s，时长 %s\n",
		r.SchemaVersion, r.Slides, m.AnimatedSlides, m.Layers, r.AudioSegments, r.AudioCodec,
		formatDuration(r.DurationUS))
	budgetState := "在预算内"
	if m.OverBudget {
		budgetState = "超预算"
	}
	fmt.Printf("体积：%d 个条目，%.2f MiB（预算 %.2f MiB，%s）\n",
		r.Entries, float64(m.TotalBytes)/(1<<20), float64(m.BudgetBytes)/(1<<20), budgetState)
	fmt.Printf("外链：%d 处\n\n", r.ExternalRefs)

	fmt.Println("| 类别 | 体积 | 占比 |")
	fmt.Println("| --- | ---: | ---: |")
	for _, kind := range m.SortedKinds() {
		bytes := m.ByKind[kind]
		label := kindLabels[kind]
		if label == "" {
			label = kind
		}
		ratio := 0.0
		if m.TotalBytes > 0 {
			ratio = float64(bytes) * 100 / float64(m.TotalBytes)
		}
		fmt.Printf("| %s | %.2f MiB | %.1f%% |\n", label, float64(bytes)/(1<<20), ratio)
	}
	// 字体子集与 WebP 都还没做，所以这两项在本实现下不适用。
	// 写成 not_applicable 而不是 pass：门禁报告不该为没做的事打勾。
	switch {
	case !r.Font.Embedded:
		fmt.Println("\n字体子集：not_applicable（未内嵌字体，使用系统字体栈）")
	case len(r.Font.Missing) == 0:
		fmt.Printf("\n字体子集：pass（%s，%d 字节，覆盖 %d 个字符）\n",
			r.Font.Family, r.Font.FontBytes, r.Font.CharsetSize)
	default:
		fmt.Printf("\n字体子集：FAIL（%s，%d 字节，%d 个字符中缺 %d 个：%s）\n",
			r.Font.Family, r.Font.FontBytes, r.Font.CharsetSize, len(r.Font.Missing),
			string(r.Font.Missing))
	}

	if len(r.ExternalRefSamples) > 0 {
		fmt.Println("\n外链明细：")
		for name, refs := range r.ExternalRefSamples {
			for _, ref := range refs {
				fmt.Printf("  %s → %s\n", name, ref)
			}
		}
	}
}

func formatDuration(us int64) string {
	return (time.Duration(us) * time.Microsecond).Round(time.Second).String()
}
