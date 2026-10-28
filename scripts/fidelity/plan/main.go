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

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/design"
	"github.com/F31/ppts/internal/render"
)

const emuPerPx = 6350 // 12192000 / 1920

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

func revision(elems []element) *animation.Revision {
	tracks := make([]*animation.Track, 0, len(elems))
	for i, e := range elems {
		clip := &animation.Clip{
			ID: e.ID + "-in", Type: "fadeIn",
			StartMs: int64(120 + i*180), DurationMs: 520,
			Easing: "easeOut", Trigger: "onEnter",
		}
		if e.Kind == "title" {
			clip = &animation.Clip{
				ID: e.ID + "-in", Type: "slideIn",
				StartMs: 100, DurationMs: 700, Easing: "easeOut",
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

func main() {
	in := flag.String("in", "", "含 pages/ 与 elements.json 的目录（gen_pages.py 的输出）")
	out := flag.String("out", "job", "job 目录：plan.json 与底图副本写在这里")
	duration := flag.Int64("duration", 6000, "每页时长（毫秒）")
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

	pages := make([]render.PageInput, 0, len(meta))
	for _, m := range meta {
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
		pages = append(pages, render.PageInput{
			Index: m.Index, SlideID: m.SlideID, DurationMs: *duration,
			BaseImage: m.BaseImage, Elements: elems, Revision: revision(m.Elements),
		})
	}

	plan, err := c.Compile(pages)
	if err != nil {
		log.Fatal(err)
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
