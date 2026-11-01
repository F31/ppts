// Command verify 是「带音频 PPTX」门禁的判定侧：结构一等（任何平台可跑），
// 客户端证据二等（Windows + PowerPoint 的读回报告，可选）。
//
// 分开的理由：客户端给出的只是"客户端说了什么"，不该把判定逻辑埋在 COM 脚本里。
// 这样同一份判定既能在有 PowerPoint 的机器上验完整链路，也能在 CI 上只验包结构，
// 而不是"没有 PowerPoint 就整体跳过"。
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type slidePlan struct {
	Index       int    `json:"index"`
	TrackKey    string `json:"trackKey"`
	HasAudio    bool   `json:"hasAudio"`
	FreqHz      int    `json:"freqHz"`
	AudioMS     int    `json:"audioMs"`
	AdvanceMS   int    `json:"advanceMs"`
	SilentPadMS int    `json:"silentPadMs"`
}

type plan struct {
	Source  string      `json:"source"`
	Writer  string      `json:"writer"`
	Version string      `json:"version"`
	Slides  []slidePlan `json:"slides"`
}

type mediaShape struct {
	Name      string `json:"name"`
	MediaType int    `json:"mediaType"`
}

type effect struct {
	EffectType int     `json:"effectType"`
	Duration   float64 `json:"duration"`
}

type clientSlide struct {
	Index          int          `json:"index"`
	MediaShapes    []mediaShape `json:"mediaShapes"`
	AdvanceOnTime  bool         `json:"advanceOnTime"`
	AdvanceSeconds float64      `json:"advanceSeconds"`
	Effects        []effect     `json:"effects"`
}

type clientReport struct {
	OK      bool          `json:"ok"`
	Version string        `json:"version"`
	OpenOK  bool          `json:"openOk"`
	SaveOK  bool          `json:"saveOk"`
	Slides  []clientSlide `json:"slides"`
	Resaved []clientSlide `json:"resaved"`
	Error   string        `json:"error"`
}

const (
	// msoAnimEffectMediaPlay。PowerPoint 16.0 读回实测值；换版本若变化，报告里能直接看到。
	effectMediaPlay = 83
	// ppMediaTypeSound：媒体形状是音频而不是视频。
	mediaTypeSound = 2
	// 时长比对容差。PowerPoint 的 AdvanceTime 是 float32 秒，2000ms 读回 1.9999999…
	toleranceMS = 150
)

type checker struct {
	failures []string
	checks   int
}

func (c *checker) ok(cond bool, format string, args ...any) {
	c.checks++
	if !cond {
		c.failures = append(c.failures, fmt.Sprintf(format, args...))
	}
}

func main() {
	pptxPath := flag.String("pptx", "", "生成的带音频 PPTX")
	planPath := flag.String("plan", "", "生成器输出的计划 JSON")
	clientPath := flag.String("client", "", "PowerPoint 读回报告 JSON（缺省则只做结构校验）")
	flag.Parse()
	if *pptxPath == "" || *planPath == "" {
		fmt.Fprintln(os.Stderr, "verify: -pptx and -plan are required")
		os.Exit(2)
	}

	var p plan
	must(readJSON(*planPath, &p), "read plan")
	zr, err := zip.OpenReader(*pptxPath)
	must(err, "open pptx")
	defer zr.Close()

	c := &checker{}
	parts := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		must(err, "open part "+f.Name)
		body, err := io.ReadAll(rc)
		rc.Close()
		// 读到底就验完了 CRC：损坏的包在这里就会报错，不必单独列一步。
		must(err, "read part "+f.Name)
		parts[f.Name] = body
	}

	slideParts := slideNames(parts)
	c.ok(len(slideParts) >= len(p.Slides), "页面数 %d 少于计划配音的页数 %d", len(slideParts), len(p.Slides))

	checkContentTypes(c, parts, p)
	audioParts := checkMedia(c, parts, p)

	// 记录"哪几页用了同一个音频部件"：只比部件哈希抓不到"两页指向同一个部件"，
	// 因为那段音频本身还是唯一的，只是被复用了——而那正是换页串音/错配的形态。
	usedBy := map[string][]string{}

	for _, sp := range p.Slides {
		name := fmt.Sprintf("ppt/slides/slide%d.xml", sp.Index+1)
		body, ok := parts[name]
		c.ok(ok, "%s: 页面部件不存在", name)
		if !ok {
			continue
		}
		xmlText := string(body)
		// advTm 是"自动切页"的唯一载体：没有它，自动放映会停在等点击。
		want := fmt.Sprintf(`advTm="%d"`, sp.AdvanceMS)
		c.ok(strings.Contains(xmlText, want), "%s: 缺自动切页时长 %s", name, want)

		if !sp.HasAudio {
			c.ok(!strings.Contains(xmlText, "<p:audio>"), "%s: 静默页不应有音频节点", name)
			c.ok(!strings.Contains(xmlText, "<a:audioFile"), "%s: 静默页不应有音频引用", name)
			continue
		}

		// 下面四项合起来说明"进页即播，离页即停"：
		//   presetClass=mediacall + playFrom(0.0)  进入页面自动开始
		//   endCond evt=onStopAudio                页面结束时停止（不串到下一页）
		//   无 numSld                               没有"跨页播放"声明
		c.ok(strings.Contains(xmlText, "<p:audio>"), "%s: 缺 <p:audio> 计时节点", name)
		c.ok(strings.Contains(xmlText, `presetClass="mediacall"`), "%s: 缺媒体播放动作（不会自动开始）", name)
		c.ok(strings.Contains(xmlText, `playFrom(0.0)`), "%s: 播放起点不是 0", name)
		c.ok(strings.Contains(xmlText, `evt="onStopAudio"`), "%s: 缺 onStopAudio 结束条件（切页可能串音）", name)
		c.ok(!strings.Contains(xmlText, "numSld="), "%s: 出现 numSld（跨页播放声明）", name)

		checkAudioRel(c, parts, name, sp, audioParts, usedBy)
	}

	for target, slides := range usedBy {
		if len(slides) > 1 {
			c.failures = append(c.failures, fmt.Sprintf("%s 被 %s 共用：每页必须是自己的音轨", target, strings.Join(slides, "、")))
		}
	}

	if *clientPath != "" {
		var rep clientReport
		must(readJSON(*clientPath, &rep), "read client report")
		checkClient(c, rep, p, len(slideParts))
	}

	fmt.Printf("结构+客户端断言：%d 项，失败 %d 项\n", c.checks, len(c.failures))
	if len(c.failures) > 0 {
		for _, f := range c.failures {
			fmt.Println("  FAIL " + f)
		}
		os.Exit(1)
	}
	if *clientPath == "" {
		fmt.Println("注意：未提供客户端报告，只验了包结构（自动播音与重开保存未验）。")
	}
	fmt.Println("说明：是否真的出声无法由 COM 观测，验收时需人工听一次（见 README）。")
}

func slideNames(parts map[string][]byte) []string {
	var names []string
	for n := range parts {
		if regexp.MustCompile(`^ppt/slides/slide\d+\.xml$`).MatchString(n) {
			names = append(names, n)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		return slideNum(names[i]) < slideNum(names[j])
	})
	return names
}

func slideNum(name string) int {
	re := regexp.MustCompile(`slide(\d+)\.xml$`)
	m := re.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func checkContentTypes(c *checker, parts map[string][]byte, p plan) {
	ct, ok := parts["[Content_Types].xml"]
	c.ok(ok, "缺 [Content_Types].xml")
	if !ok {
		return
	}
	text := string(ct)
	for _, sp := range p.Slides {
		if !sp.HasAudio {
			continue
		}
		c.ok(strings.Contains(text, "audio/wav"), "[Content_Types].xml: 未声明 audio/wav")
		break
	}
}

// checkMedia 校验音频部件本身：数量、互不相同的哈希、以及 WAV 时长与计划一致。
// 互不相同的哈希是"每页各有一段音频"的硬证据；只数数量的话，14 页共用一个音轨也能过。
func checkMedia(c *checker, parts map[string][]byte, p plan) map[string]string {
	wantAudio := 0
	for _, sp := range p.Slides {
		if sp.HasAudio {
			wantAudio++
		}
	}
	var wavs []string
	for n := range parts {
		if strings.HasPrefix(n, "ppt/media/") && strings.HasSuffix(strings.ToLower(n), ".wav") {
			wavs = append(wavs, n)
		}
	}
	sort.Strings(wavs)
	c.ok(len(wavs) == wantAudio, "音频部件数 %d，计划要求 %d", len(wavs), wantAudio)

	seen := map[string]string{}
	for _, n := range wavs {
		sum := sha256.Sum256(parts[n])
		h := hex.EncodeToString(sum[:])
		if prev, dup := seen[h]; dup {
			c.failures = append(c.failures, fmt.Sprintf("%s 与 %s 内容相同：两页共用同一段音频", n, prev))
		}
		seen[h] = n
	}
	return seen
}

func checkAudioRel(c *checker, parts map[string][]byte, slideName string, sp slidePlan, _ map[string]string, usedBy map[string][]string) {
	relsName := strings.Replace(slideName, "ppt/slides/", "ppt/slides/_rels/", 1) + ".rels"
	body, ok := parts[relsName]
	c.ok(ok, "%s: 缺关系文件", relsName)
	if !ok {
		return
	}
	var rels struct {
		Items []struct {
			ID     string `xml:"Id,attr"`
			Type   string `xml:"Type,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	c.ok(xml.Unmarshal(body, &rels) == nil, "%s: 关系文件无法解析", relsName)

	audioID, audioTarget := "", ""
	for _, r := range rels.Items {
		if strings.HasSuffix(r.Type, "/audio") {
			audioID, audioTarget = r.ID, r.Target
		}
	}
	c.ok(audioID != "", "%s: 没有 audio 关系", relsName)
	if audioTarget == "" {
		return
	}
	resolved := strings.TrimPrefix(strings.ReplaceAll(audioTarget, "../", "ppt/"), "/")
	usedBy[resolved] = append(usedBy[resolved], slideName)
	data, ok := parts[resolved]
	c.ok(ok, "%s: audio 关系指向不存在的部件 %s", relsName, resolved)
	if !ok {
		return
	}

	// 形状里的 <a:audioFile r:link> 必须指向同一条关系，否则计时节点打到的不是这段音频。
	slideText := string(parts[slideName])
	c.ok(strings.Contains(slideText, fmt.Sprintf(`<a:audioFile r:link="%s"`, audioID)),
		"%s: 音频形状没有引用关系 %s", slideName, audioID)

	// WAV 时长必须等于计划时长：这一条把"第 N 页的音频"和"第 N 页的讲稿"绑在一起，
	// 换成别页的音频（哪怕也是合法音频）会在这里露出来。
	if ms, err := wavDurationMS(data); err == nil {
		c.ok(absInt(ms-sp.AudioMS) <= toleranceMS, "%s: 音频时长 %dms，计划 %dms", resolved, ms, sp.AudioMS)
	} else {
		c.failures = append(c.failures, fmt.Sprintf("%s: 无法解析 WAV 头: %v", resolved, err))
	}
}

// wavDurationMS 只读 fmt 块算时长，不依赖第三方解码库。
func wavDurationMS(data []byte) (int, error) {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, fmt.Errorf("not a RIFF/WAVE file")
	}
	pos := 12
	var byteRate uint32
	var dataLen uint32
	for pos+8 <= len(data) {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		body := pos + 8
		switch id {
		case "fmt ":
			if body+12 > len(data) {
				return 0, fmt.Errorf("truncated fmt chunk")
			}
			byteRate = binary.LittleEndian.Uint32(data[body+8 : body+12])
		case "data":
			dataLen = uint32(size)
		}
		pos = body + size + size%2
	}
	if byteRate == 0 {
		return 0, fmt.Errorf("no byte rate in fmt chunk")
	}
	return int(float64(dataLen) / float64(byteRate) * 1000), nil
}

func checkClient(c *checker, rep clientReport, p plan, packageSlides int) {
	c.ok(rep.OpenOK, "客户端：打开失败（若为修复提示，调用方应以超时判失败）：%s", rep.Error)
	c.ok(rep.SaveOK, "客户端：另存 .pptx 失败：%s", rep.Error)
	c.ok(rep.Version != "", "客户端：没有报告版本号，认证记录无从登记")
	c.ok(len(rep.Slides) == packageSlides, "客户端读到 %d 页，包里有 %d 页", len(rep.Slides), packageSlides)

	for _, sp := range p.Slides {
		got, ok := findSlide(rep.Slides, sp.Index)
		c.ok(ok, "客户端：读不到第 %d 页", sp.Index+1)
		if !ok {
			continue
		}
		wantMedia := 0
		if sp.HasAudio {
			wantMedia = 1
		}
		c.ok(len(got.MediaShapes) == wantMedia,
			"第 %d 页：客户端看到 %d 个媒体形状，期望 %d 个", sp.Index+1, len(got.MediaShapes), wantMedia)
		if sp.HasAudio && len(got.MediaShapes) > 0 {
			c.ok(got.MediaShapes[0].MediaType == mediaTypeSound,
				"第 %d 页：媒体类型是 %d，不是音频(2)", sp.Index+1, got.MediaShapes[0].MediaType)
		}

		// 自动切页：客户端把 advTm 读成 AdvanceOnTime + AdvanceTime 两个字段。
		c.ok(got.AdvanceOnTime, "第 %d 页：客户端认为没有自动切页", sp.Index+1)
		c.ok(absInt(int(got.AdvanceSeconds*1000)-sp.AdvanceMS) <= toleranceMS,
			"第 %d 页：客户端读到自动切页 %.3fs，计划 %dms", sp.Index+1, got.AdvanceSeconds, sp.AdvanceMS)

		if !sp.HasAudio {
			c.ok(len(got.Effects) == 0, "第 %d 页：静默页不应有动画效果", sp.Index+1)
			continue
		}
		// 自动播音在对象模型里就是"进入页面即触发一次媒体播放"。
		play := 0
		var playDur float64
		for _, e := range got.Effects {
			if e.EffectType == effectMediaPlay {
				play++
				playDur = e.Duration
			}
		}
		c.ok(play == 1, "第 %d 页：媒体播放效果 %d 个，期望 1 个", sp.Index+1, play)
		if play == 1 {
			c.ok(absInt(int(playDur*1000)-sp.AudioMS) <= toleranceMS,
				"第 %d 页：媒体效果时长 %.3fs，音频时长 %dms", sp.Index+1, playDur, sp.AudioMS)
		}

		// 重开保存：客户端自己写出的文件再读一遍，上面这些量必须还在。
		again, ok := findSlide(rep.Resaved, sp.Index)
		c.ok(ok, "重开保存后：读不到第 %d 页", sp.Index+1)
		if !ok {
			continue
		}
		c.ok(len(again.MediaShapes) == wantMedia,
			"重开保存后第 %d 页：媒体形状 %d 个，期望 %d 个", sp.Index+1, len(again.MediaShapes), wantMedia)
		c.ok(again.AdvanceOnTime, "重开保存后第 %d 页：自动切页丢了", sp.Index+1)
		c.ok(absInt(int(again.AdvanceSeconds*1000)-sp.AdvanceMS) <= toleranceMS,
			"重开保存后第 %d 页：自动切页 %.3fs，计划 %dms", sp.Index+1, again.AdvanceSeconds, sp.AdvanceMS)
	}
}

func findSlide(list []clientSlide, index int) (clientSlide, bool) {
	for _, s := range list {
		if s.Index == index {
			return s, true
		}
	}
	return clientSlide{}, false
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func readJSON(path string, dst any) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// 手工编辑过的报告常带 BOM（PowerShell 5.1 的 Set-Content 默认会写），
	// 直接 Unmarshal 会报 "invalid character '\ufeff'"，这里统一剥掉。
	body = bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF})
	return json.Unmarshal(body, dst)
}

func must(err error, what string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: %s: %v\n", what, err)
		os.Exit(1)
	}
}
