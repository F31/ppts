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
	DurationUS    int64  `json:"durationUs"`
	Entries       int    `json:"entries"`
	TotalBytes    int64  `json:"totalBytes"`
	ExternalRefs  int    `json:"externalRefs"`
	// ExternalRefSamples 给出「哪个条目里的哪一段」。只报数量的话排查要从头 grep。
	ExternalRefSamples map[string][]string `json:"externalRefSamples,omitempty"`
	Measure            share.Measurement   `json:"measure"`
}

func main() {
	jsonOut := flag.Bool("json", false, "以 JSON 输出")
	allowExternal := flag.Bool("allow-external", false, "检出外链时只警告，不返回失败")
	checkBudget := flag.Bool("budget", false, "超体积预算时返回失败（默认只报告）")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "用法：share-verify [-json] [-allow-external] [-budget] <bundle.zip|bundle-dir>\n")
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
	res := result{
		Target: target, Kind: kind,
		SchemaVersion:      manifest.SchemaVersion,
		Slides:             len(manifest.Slides),
		AudioSegments:      len(manifest.Audio),
		AudioMix:           manifest.AudioMix != "",
		DurationUS:         manifest.Artifact.DurationUS,
		Entries:            len(files),
		TotalBytes:         measure.TotalBytes,
		ExternalRefs:       share.CountExternalRefs(files),
		ExternalRefSamples: share.ExternalRefSamples(files),
		Measure:            measure,
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
	share.KindSubtitles: "字幕",
	share.KindManifest:  "清单",
	share.KindOther:     "其它",
}

func printResult(r result) {
	m := r.Measure
	fmt.Printf("目标：%s（%s）\n", r.Target, r.Kind)
	fmt.Printf("清单：schema v%d，%d 页（含动画 %d 页 / %d 层），%d 段音频，混音轨=%v，时长 %s\n",
		r.SchemaVersion, r.Slides, m.AnimatedSlides, m.Layers, r.AudioSegments, r.AudioMix,
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
	fmt.Println("\n字体子集：not_applicable（当前不内嵌字体，使用系统字体栈）")

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
