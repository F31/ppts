package main

import (
	"testing"

	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/render"
)

// 夹具自己也要有判据：一个「造出来的东西没有动画」的夹具会让 V13 变成比两张静态图，
// 而那种门禁比没有门禁更危险——它报绿，却什么都没验。
func TestElementBoxesStayInsideAndApart(t *testing.T) {
	const w, h = 1280, 720
	boxes := elementBoxes(w, h)
	if len(boxes) < 2 {
		t.Fatalf("元素框 = %d 个，至少要两个（一个元素验不出层序）", len(boxes))
	}
	for i, b := range boxes {
		if b.x < 0 || b.y < 0 || b.x+b.w > w || b.y+b.h > h {
			t.Fatalf("box %d 越出画布：%+v（%dx%d）", i, b, w, h)
		}
		// 贴边会让底板的「外环中位数」取不到色，夹具就会退化成无分层——静默弱化判据。
		if b.x < 8 || b.y < 8 || b.x+b.w > w-8 || b.y+b.h > h-8 {
			t.Fatalf("box %d 离页边太近：%+v", i, b)
		}
		for j := i + 1; j < len(boxes); j++ {
			o := boxes[j]
			if b.x < o.x+o.w && o.x < b.x+b.w && b.y < o.y+o.h && o.y < b.y+b.h {
				t.Fatalf("box %d 与 %d 重叠：%+v / %+v", i, j, b, o)
			}
		}
	}
}

func TestCompilePlanProducesAnimatedLayers(t *testing.T) {
	const (
		w, h    = 1280, 720
		pages   = 2
		durMs   = int64(2000)
		wantPer = 2 // 每个元素一条轨道
	)
	plan, err := compilePlan(w, h, pages, durMs, animdefault.DefaultPresetID, elementBoxes(w, h))
	if err != nil {
		t.Fatalf("compilePlan: %v", err)
	}
	if err := checkPlan(plan); err != nil {
		t.Fatalf("checkPlan: %v", err)
	}
	if plan.FPS != fixtureFPS || plan.Width != w || plan.Height != h {
		t.Fatalf("计划头部 = %+v", plan)
	}
	if len(plan.Pages) != pages {
		t.Fatalf("页数 = %d，want %d", len(plan.Pages), pages)
	}
	for i, page := range plan.Pages {
		if page.DurationMs != durMs {
			t.Fatalf("page %d 时长 = %d，want %d", i, page.DurationMs, durMs)
		}
		if len(page.Layers) != wantPer {
			t.Fatalf("page %d 层数 = %d，want %d", i, len(page.Layers), wantPer)
		}
		for _, layer := range page.Layers {
			// 两条 clip（淡入 + 上移）：只有一条的话，「透明度与位移分别求值再合成」
			// 这条最容易分叉的路径在夹具里根本走不到。
			if len(layer.Clips) != 2 {
				t.Fatalf("page %d layer %s clips = %d，want 2", i, layer.ElementID, len(layer.Clips))
			}
			for _, c := range layer.Clips {
				if len(c.Props) == 0 {
					t.Fatalf("page %d layer %s clip %s 没有属性轨道", i, layer.ElementID, c.ClipID)
				}
			}
		}
		// 首镜不存在（本夹具不造切镜），所以 BaseImage 必须就是页图。
		if page.BaseImage == "" {
			t.Fatalf("page %d 没有底图", i)
		}
	}
}

func TestCheckPlanRejectsStaticPlan(t *testing.T) {
	static := &render.Timeline{Pages: []render.Page{{Index: 0, DurationMs: 1000, BaseImage: "pages/p001.png"}}}
	if err := checkPlan(static); err == nil {
		t.Fatal("没有层的计划必须被拒：否则 V13 会比两张静态图")
	}
	empty := &render.Timeline{}
	if err := checkPlan(empty); err == nil {
		t.Fatal("空计划必须被拒")
	}
}

// 时间轴与 IR 必须是同一块画布、同一条时间轴：两处各写一份时长，
// 取样点就会落在「这一刻」之外，判据分数再好也没有意义。
func TestTimelineMatchesPlanDuration(t *testing.T) {
	const pages = 3
	tl, err := buildTimeline(pages, 1500)
	if err != nil {
		t.Fatalf("buildTimeline: %v", err)
	}
	if len(tl.Slides) != pages || len(tl.Subtitles) != pages {
		t.Fatalf("slides=%d subtitles=%d，want %d", len(tl.Slides), len(tl.Subtitles), pages)
	}
	if tl.DurationUS != int64(pages)*1_500_000 {
		t.Fatalf("总时长 = %d", tl.DurationUS)
	}
	for i, slide := range tl.Slides {
		if got := slide.EndUS - slide.StartUS; got != 1_500_000 {
			t.Fatalf("slide %d 时长 = %d", i, got)
		}
		// 音频段必须比页短：分段尾部留白是分享包时间轴的既有形态，
		// 与页等长会让最后一段音频与下一页的起点重叠。
		if slide.Segments[0].EndUS >= slide.EndUS {
			t.Fatalf("slide %d 音频段没有留白", i)
		}
	}
}
