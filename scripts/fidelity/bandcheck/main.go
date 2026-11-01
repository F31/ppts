// bandcheck 打印每页的分档判定，供离线对照脚本做「按页路由」。
//
// 为什么单独一个程序：分档逻辑只有一份实现（internal/animation/bind.classifyBand）。
// 离线脚本要按分档路由，就必须问产品代码，不能在 Python 侧再写一遍——两套尺子量同一批页，
// 测出来的东西没法解释。这里只做胶水：读 elements.json + 讲稿 → 组 bind 输入 → 输出判定。
//
//	go run ./scripts/fidelity/bandcheck \
//	    -elements tmp/q8/fid/elements.json \
//	    -segments scripts/fidelity/q8-script.json \
//	    -out tmp/q8/band.json
//
// 输出（供 bind_llm.py --route 读）：
//
//	{"model":"...","pages":[{"index":0,"band":"structured","bandReason":"",
//	                         "signals":["markers","clusters"],"bound":2}]}
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/F31/ppts/internal/animation/bind"
)

type element struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Box  [4]int `json:"box"`
	Text string `json:"text,omitempty"`
}

type page struct {
	Index    int       `json:"index"`
	SlideID  string    `json:"slideId"`
	Elements []element `json:"elements"`
}

type segment struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type scriptPage struct {
	Slide    int       `json:"slide"`
	Segments []segment `json:"segments"`
}

type script struct {
	Pages []scriptPage `json:"pages"`
}

type verdict struct {
	Index      int      `json:"index"`
	SlideID    string   `json:"slideId,omitempty"`
	Band       string   `json:"band"`
	BandReason string   `json:"bandReason,omitempty"`
	Signals    []string `json:"signals,omitempty"`
	Bound      int      `json:"bound"`
}

func main() {
	elemPath := flag.String("elements", "", "elements.json（数组，每项一页）")
	segPath := flag.String("segments", "", "讲稿 JSON（pages[].segments[]）")
	out := flag.String("out", "", "输出路径；留空打印到 stdout")
	flag.Parse()

	if *elemPath == "" || *segPath == "" {
		log.Fatal("需要 -elements 与 -segments")
	}

	pages, segs := load(*elemPath, *segPath)
	if len(pages) != len(segs) {
		log.Fatalf("页数对不上：elements %d 页、讲稿 %d 页。按顺序配对，不能错位",
			len(pages), len(segs))
	}

	outPages := make([]verdict, 0, len(pages))
	for i, p := range pages {
		bel := make([]bind.Element, 0, len(p.Elements))
		for _, e := range p.Elements {
			bel = append(bel, bind.Element{
				ID:   e.ID,
				Kind: e.Kind,
				Box:  bind.Box{X: float64(e.Box[0]), Y: float64(e.Box[1]), W: float64(e.Box[2]), H: float64(e.Box[3])},
				Text: e.Text,
			})
		}
		bsegs := make([]bind.Segment, 0, len(segs[i].Segments))
		for _, s := range segs[i].Segments {
			bsegs = append(bsegs, bind.Segment{ID: s.ID, Text: s.Text})
		}

		res := bind.Run(bel, bsegs, bind.Options{})
		outPages = append(outPages, verdict{
			Index:      p.Index,
			SlideID:    p.SlideID,
			Band:       string(res.Band),
			BandReason: res.BandReason,
			Signals:    res.Signals,
			Bound:      len(res.Bindings),
		})
	}

	doc := map[string]any{"pages": outPages}
	if *out == "" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", " ")
		if err := enc.Encode(doc); err != nil {
			log.Fatal(err)
		}
		return
	}

	buf, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, append(buf, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "写到 %s（%d 页）\n", *out, len(outPages))
}

func load(elemPath, segPath string) ([]page, []scriptPage) {
	var pages []page
	readJSON(elemPath, &pages)
	var s script
	readJSON(segPath, &s)
	return pages, s.Pages
}

func readJSON(path string, v any) {
	buf, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("读 %s：%v", path, err)
	}
	if err := json.Unmarshal(buf, v); err != nil {
		log.Fatalf("解析 %s：%v", path, err)
	}
}
