// share-verify 对成品包做结构校验与外链扫描（tasks 6.1/7.2）。
//
// 接受 zip 或目录（隐私模式产物是目录），两者走同一套 Manifest.Validate 与
// CountExternalRefs——CI 里对构建出的样例包跑一遍，就能拦住"有人给播放器加了
// CDN 字体/遥测脚本"这类回归。
//
// 退出码：0 通过；1 结构不合规；2 检出外链（可用 -allow-external 降级为警告）。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
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
	DurationUS    int64  `json:"durationUs"`
	Entries       int    `json:"entries"`
	TotalBytes    int64  `json:"totalBytes"`
	ExternalRefs  int    `json:"externalRefs"`
}

func main() {
	jsonOut := flag.Bool("json", false, "以 JSON 输出")
	allowExternal := flag.Bool("allow-external", false, "检出外链时只警告，不返回失败")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "用法：share-verify [-json] [-allow-external] <bundle.zip|bundle-dir>\n")
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

	var total int64
	for _, data := range files {
		total += int64(len(data))
	}
	res := result{
		Target: target, Kind: kind,
		SchemaVersion: manifest.SchemaVersion,
		Slides:        len(manifest.Slides),
		AudioSegments: len(manifest.Audio),
		AudioMix:      manifest.AudioMix != "",
		DurationUS:    manifest.Artifact.DurationUS,
		Entries:       len(files),
		TotalBytes:    total,
		ExternalRefs:  share.CountExternalRefs(files),
	}

	if *jsonOut {
		raw, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(raw))
	} else {
		printResult(res, files)
	}

	if res.ExternalRefs > 0 {
		if *allowExternal {
			fmt.Fprintf(os.Stderr, "share-verify: 警告：检出 %d 处外链（-allow-external 已放行）\n", res.ExternalRefs)
			return
		}
		fmt.Fprintf(os.Stderr, "share-verify: 失败：检出 %d 处外链，成品包必须可离线播放\n", res.ExternalRefs)
		os.Exit(2)
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

func printResult(r result, files map[string][]byte) {
	fmt.Printf("目标：%s（%s）\n", r.Target, r.Kind)
	fmt.Printf("清单：schema v%d，%d 页，%d 段音频，混音轨=%v，时长 %s\n",
		r.SchemaVersion, r.Slides, r.AudioSegments, r.AudioMix, formatDuration(r.DurationUS))
	fmt.Printf("体积：%d 个条目，%.2f MB\n", r.Entries, float64(r.TotalBytes)/(1<<20))
	fmt.Printf("外链：%d 处\n\n", r.ExternalRefs)

	// 按顶层目录/前缀汇总，便于一眼看出体积构成。
	groups := map[string]struct {
		files int
		bytes int64
	}{}
	for name, data := range files {
		key := name
		if i := strings.IndexByte(name, '/'); i >= 0 {
			key = name[:i] + "/"
		}
		g := groups[key]
		g.files++
		g.bytes += int64(len(data))
		groups[key] = g
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return groups[keys[i]].bytes > groups[keys[j]].bytes })
	fmt.Println("| 条目 | 文件数 | 体积 |")
	fmt.Println("| --- | ---: | ---: |")
	for _, k := range keys {
		fmt.Printf("| %s | %d | %.2f MB |\n", k, groups[k].files, float64(groups[k].bytes)/(1<<20))
	}
}

func formatDuration(us int64) string {
	d := time.Duration(us) * time.Microsecond
	return d.Round(time.Second).String()
}
