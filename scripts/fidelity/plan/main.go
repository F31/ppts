// 把 scripts/fidelity/gen_pages.py 产出的底图编译成渲染计划（compiled-timeline/v1）。
//
// 为什么单独一个程序：保真检查要的是「能提交给服务的 plan.json」，
// 而编译器在 internal/render 里，不是命令行工具。这里只做胶水：
// 读 elements.json → 组 PageInput → Compile → 落盘。
//
//	go run ./scripts/fidelity/plan -in /tmp/fidelity -out /tmp/job
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/design"
	"github.com/F31/ppts/internal/render"
)

const emuPerPx = 6350 // 12192000 / 1920

// 画布尺寸：速度分层按面积占比判档，必须知道画布大小才能把 box 换算成占比
const (
	canvasW = 1920
	canvasH = 1080
)

type element struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Box  [4]int `json:"box"`
}

type pageMeta struct {
	Index     int       `json:"index"`
	SlideID   string    `json:"slideId"`
	BaseImage string    `json:"baseImage"`
	Elements  []element `json:"elements"`
}

func revision(elems []element, tierCompliant bool, pageMs int64) *animation.Revision {
	// §19.4 的速度分层是按**元素面积占比**判的（≥25% 大元素 600–900、≥8% 中元素 400–600、
	// 其余 240–400），不是按元素类别。所以合规档不能写死「标题 700 / 要点 320」，
	// 必须交给档案自己算——否则同一份计划里形状比文字大得多，却被套同一档时长。
	// 默认档（520/700）是这份样片的历史取值，会落在两档之间的空档里并触发
	// duration_tier_conflict；保持不变是为了让 ADR-18 的保真基线与 5.10 的记录可复现。
	prof := design.DefaultMotion()
	tracks := make([]*animation.Track, 0, len(elems))
	for i, e := range elems {
		dur := int64(520)
		if e.Kind == "title" {
			dur = 700
		}
		if tierCompliant {
			area := float64(e.Box[2]*e.Box[3]) / float64(canvasW*canvasH)
			if d := prof.DurationFor(area, pageMs); d > 0 {
				dur = d
			}
		}
		clip := &animation.Clip{
			ID: e.ID + "-in", Type: "fadeIn",
			StartMs: int64(120 + i*180), DurationMs: dur,
			Easing: "easeOut", Trigger: "onEnter",
		}
		if e.Kind == "title" {
			clip = &animation.Clip{
				ID: e.ID + "-in", Type: "slideIn",
				StartMs: 100, DurationMs: dur, Easing: "easeOut",
				Params: animation.Params{Direction: "up", Offset: 48}, Trigger: "onEnter",
			}
		}
		tracks = append(tracks, &animation.Track{ElementID: e.ID, Kind: e.Kind, Clips: []*animation.Clip{clip}})
	}
	return &animation.Revision{
		PresetID:  "business-calm",
		Intensity: 1,
		Status:    animation.StatusApproved,
		Tracks:    tracks,
	}
}

// ---- Q7：讲解段落绑定（narrate.py 产出的 segments.json）----

type bindRef struct {
	ElementID string `json:"elementId"`
	OffsetMs  int64  `json:"offsetMs"`
}

type scriptSegment struct {
	ID         string    `json:"id"`
	Text       string    `json:"text"`
	StartMs    int64     `json:"startMs"`
	EndMs      int64     `json:"endMs"`
	DurationMs int64     `json:"durationMs"`
	Bind       []bindRef `json:"bind"`
}

type scriptPage struct {
	Index      int             `json:"index"`
	SlideID    string          `json:"slideId"`
	DurationMs int64           `json:"durationMs"`
	Segments   []scriptSegment `json:"segments"`
}

type scriptDoc struct {
	Pages []scriptPage `json:"pages"`
}

// unboundLeadMS 是「讲解没提到」的元素在页末出场的提前量：
// 留够一次淡入（600ms）落在页内，观众还能看到它出现，而不是被切在页尾。
const unboundLeadMS = 900

type boundClip struct {
	segment string
	offset  int64
}

// scriptRevision 把「元素 → 段落」的绑定编译成轨道。
//
// 段落的开口由 TTS 实测时长决定（segments.json 的 startMs），所以 clip 的
// trigger 必须写成 withSegment:<段 id>：起点跟着音频走，而不是跟着写死的毫秒。
// 写成 atTime 也能出片，但两边一旦不同步就是「画面按旧时长走、声音按新时长走」，
// 而且没有任何报错——这正是 Q7 要证伪的失败模式。
//
// 未出现在任何 bind 里的元素按「讲解没提到」处理：放在页末 unboundLeadMS 处淡入。
// 这是判据「不空转」的对照——全页元素在页首铺满，动画就退化成装饰。
func scriptRevision(elems []element, page scriptPage, tierCompliant bool) *animation.Revision {
	bound := make(map[string]boundClip, len(elems))
	for _, seg := range page.Segments {
		for _, ref := range seg.Bind {
			if prev, dup := bound[ref.ElementID]; dup {
				// 同一元素被多段绑定：只认最早的一段，重复出场会让「讲到才出现」失效。
				log.Printf("警告：%s 被多段绑定，取 %q，忽略 %q", ref.ElementID, prev.segment, seg.ID)
				continue
			}
			bound[ref.ElementID] = boundClip{segment: seg.ID, offset: ref.OffsetMs}
		}
	}

	prof := design.DefaultMotion()
	tracks := make([]*animation.Track, 0, len(elems))
	var unbound []string
	for _, e := range elems {
		dur := int64(520)
		if e.Kind == "title" {
			dur = 700
		}
		if tierCompliant {
			area := float64(e.Box[2]*e.Box[3]) / float64(canvasW*canvasH)
			if d := prof.DurationFor(area, page.DurationMs); d > 0 {
				dur = d
			}
		}
		clip := &animation.Clip{
			ID: e.ID + "-in", Type: "fadeIn", DurationMs: dur, Easing: "easeOut",
		}
		if b, ok := bound[e.ID]; ok {
			clip.Trigger = animation.TriggerSegmentPrefix + b.segment
			clip.StartMs = b.offset
		} else {
			// 页末提前量与「最后一段讲完」取较晚者：页长被最小页长撑起来时，
			// 只按页末倒推会让没讲到的元素在最后一句讲到一半时冒出来。
			lastEnd := int64(0)
			if n := len(page.Segments); n > 0 {
				lastEnd = page.Segments[n-1].EndMs
			}
			start := page.DurationMs - unboundLeadMS
			if start < lastEnd+200 {
				start = lastEnd + 200
			}
			if start+dur > page.DurationMs {
				start = page.DurationMs - dur
			}
			if start < 0 {
				start = 0
			}
			clip.Trigger = animation.TriggerAtTimePrefix + strconv.FormatInt(start, 10)
			clip.StartMs = 0
			unbound = append(unbound, e.ID)
		}
		if e.Kind == "title" {
			clip.Type = "slideIn"
			clip.Params = animation.Params{Direction: "up", Offset: 48}
		}
		tracks = append(tracks, &animation.Track{ElementID: e.ID, Kind: e.Kind, Clips: []*animation.Clip{clip}})
	}
	if len(unbound) > 0 {
		log.Printf("页 %d：讲解没提到 %v，按页末提前 %dms 淡入", page.Index, unbound, unboundLeadMS)
	}
	return &animation.Revision{
		PresetID:  "business-calm",
		Intensity: 1,
		Status:    animation.StatusApproved,
		Tracks:    tracks,
	}
}

func main() {
	in := flag.String("in", "", "含 pages/ 与 elements.json 的目录（gen_pages.py 的输出）")
	out := flag.String("out", "job", "job 目录：plan.json 与底图副本写在这里")
	duration := flag.Int64("duration", 6000, "每页时长（毫秒）；给了 -script 时由配音实测值取代")
	// 见 revision() 的注释：默认产物必须与 5.10 的保真基线一致，所以合规档只在新开关下启用。
	tierCompliant := flag.Bool("tier-compliant", false,
		"按 §19.4 速度分层取时长（小元素 320ms / 大元素 700ms）；盲评样片用")
	scriptPath := flag.String("script", "",
		"narrate.py 产出的 segments.json：用 withSegment 按讲解段落起播（页时长也取它的实测值）")
	hardCut := flag.Bool("hard-cut", false,
		"关闭页转场：转场 320ms 与页首段落的起播（220ms）重叠，像素法测不出元素是否准时")
	flag.Parse()
	if *in == "" {
		log.Fatal("-in 必填")
	}

	raw, err := os.ReadFile(filepath.Join(*in, "elements.json"))
	if err != nil {
		log.Fatal(err)
	}
	var meta []pageMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		log.Fatal(err)
	}
	// IR 的 kind 是封闭集合（validate.validKind），没有 "text"。纯文字块归到 bullet。
	for i := range meta {
		for j := range meta[i].Elements {
			if meta[i].Elements[j].Kind == "text" {
				meta[i].Elements[j].Kind = "bullet"
			}
		}
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	// 底图拷进 job 目录：合成器只认 plan 所在目录下的相对路径（ADR-14）。
	for _, m := range meta {
		data, err := os.ReadFile(filepath.Join(*in, filepath.FromSlash(m.BaseImage)))
		if err != nil {
			log.Fatal(err)
		}
		dst := filepath.Join(*out, filepath.FromSlash(m.BaseImage))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			log.Fatal(err)
		}
	}

	theme := design.Default()
	c, err := render.NewCompiler(render.Options{
		Canvas:         theme.Canvas,
		SlideWidthEMU:  12192000,
		SlideHeightEMU: 6858000,
		FPS:            30,
	})
	if err != nil {
		log.Fatal(err)
	}

	var script *scriptDoc
	if *scriptPath != "" {
		raw, err := os.ReadFile(*scriptPath)
		if err != nil {
			log.Fatal(err)
		}
		script = &scriptDoc{}
		if err := json.Unmarshal(raw, script); err != nil {
			log.Fatal(err)
		}
		if len(script.Pages) != len(meta) {
			log.Fatalf("-script 有 %d 页，elements.json 有 %d 页：两份输入不是同一份样片",
				len(script.Pages), len(meta))
		}
		for i, sp := range script.Pages {
			if sp.SlideID != meta[i].SlideID {
				log.Fatalf("页 %d 的 slideId 对不上：script=%q elements=%q",
					i, sp.SlideID, meta[i].SlideID)
			}
			seen := make(map[string]struct{}, len(sp.Segments))
			for _, seg := range sp.Segments {
				if _, dup := seen[seg.ID]; dup {
					// 段 id 是编进 trigger 的键，重复会让两条 clip 指向同一锚点。
					log.Fatalf("页 %d 的段 id 重复：%q", sp.Index, seg.ID)
				}
				seen[seg.ID] = struct{}{}
			}
		}
	}

	pages := make([]render.PageInput, 0, len(meta))
	for i, m := range meta {
		elems := make([]render.Element, 0, len(m.Elements))
		for _, e := range m.Elements {
			b := e.Box
			elems = append(elems, render.Element{
				ID: e.ID, Kind: e.Kind,
				Bounds: &render.EMUBox{
					X: int64(b[0]) * emuPerPx, Y: int64(b[1]) * emuPerPx,
					Width: int64(b[2]) * emuPerPx, Height: int64(b[3]) * emuPerPx,
				},
			})
		}
		pageMs := *duration
		rev := revision(m.Elements, *tierCompliant, pageMs)
		var offsets map[string]int64
		if script != nil {
			sp := script.Pages[i]
			pageMs = sp.DurationMs
			rev = scriptRevision(m.Elements, sp, *tierCompliant)
			offsets = make(map[string]int64, len(sp.Segments))
			for _, seg := range sp.Segments {
				offsets[seg.ID] = seg.StartMs
			}
		}
		pages = append(pages, render.PageInput{
			Index: m.Index, SlideID: m.SlideID, DurationMs: pageMs,
			BaseImage: m.BaseImage, Elements: elems, Revision: rev,
			SegmentOffsets: offsets,
		})
	}

	plan, err := c.Compile(pages)
	if err != nil {
		log.Fatal(err)
	}
	if *hardCut {
		// 转场是逐像素混合，页首 320ms 里每一帧都是「上一页 × 这一页」的插值。
		// 段落又在页首 220ms 开口，两个动作叠在一起，测出来的是混合度而不是
		// 元素的不透明度。实验样片用硬切把转场这个变量摘掉；产品默认仍带转场。
		for i := range plan.Pages {
			plan.Pages[i].Transition = render.Transition{Type: animation.TransitionNone}
		}
		log.Printf("硬切：%d 页的转场已关闭（仅为对齐实验）", len(plan.Pages))
	}
	// 必须用普通 marshal：CanonicalJSON 是给 hash 用的规范化副本，
	// 会把 baseImage 砍成 basename、清掉 plateImage/layer.image，
	// 拿它当「要渲染的计划」写盘，服务端就会去错目录找底图。
	body, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*out, "plan.json"), body, 0o644); err != nil {
		log.Fatal(err)
	}
	h, err := plan.Hash(render.HashOptions{RendererVersion: "fidelity"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("plan: pages=%d warnings=%d hash=%s\n", len(plan.Pages), len(plan.Warnings), h[:16])
	for _, p := range plan.Pages {
		fmt.Printf("  page %d layers=%d transition=%s/%dms\n",
			p.Index, len(p.Layers), p.Transition.Type, p.Transition.DurationMs)
	}
}
