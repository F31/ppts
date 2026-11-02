package main

import (
	"math"
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
	plan, err := compilePlan(w, h, pages, durMs, animdefault.DefaultPresetID, elementBoxes(w, h), 2)
	if err != nil {
		t.Fatalf("compilePlan: %v", err)
	}
	if err := checkPlan(plan, 2); err != nil {
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
		// 首镜必须指向页级图（此时还没有底板，编译期给的是 baseImage）；
		// 特写镜头的图是 staging/打包器从这张页图裁出来的，路径由编译器给。
		if page.BaseImage == "" {
			t.Fatalf("page %d 没有底图", i)
		}
		if len(page.Shots) != 2 {
			t.Fatalf("page %d 镜头数 = %d，want 2", i, len(page.Shots))
		}
		if page.Shots[0].Image != page.BaseImage {
			t.Fatalf("page %d 首镜图 = %q，want 页图 %q", i, page.Shots[0].Image, page.BaseImage)
		}
		if page.Shots[1].Crop == nil {
			t.Fatalf("page %d 特写镜头没有裁切框", i)
		}
	}
}

// 切镜必须真的覆盖整页：少 1ms 页尾就停帧，多 1ms 会把下一镜的图提前亮出来，
// 两种都不报错。IR 校验会拒，但夹具应该在编译后自己再确认一次。
func TestPageShotInputsCoverPageAndStayInside(t *testing.T) {
	const (
		w, h  = 1280, 720
		durMs = int64(2000)
	)
	emuPerPx := float64(slideWidthEMU) / float64(w)
	shots := pageShotInputs(durMs, 2, elementBoxes(w, h), w, h, emuPerPx)
	if len(shots) != 2 {
		t.Fatalf("镜头数 = %d，want 2", len(shots))
	}
	var cursor int64
	for k, s := range shots {
		if s.StartMs != cursor {
			t.Fatalf("shot %d 起点 = %d，want %d（有空隙或重叠）", k, s.StartMs, cursor)
		}
		if s.DurationMs <= 0 {
			t.Fatalf("shot %d 时长非正", k)
		}
		if int64(s.CrossFadeMs) >= s.DurationMs {
			t.Fatalf("shot %d 叠化 %d 不短于时长 %d", k, s.CrossFadeMs, s.DurationMs)
		}
		if k == 0 && s.Crop != nil {
			t.Fatalf("首镜是整页，不该带裁切框")
		}
		if k > 0 {
			if s.Crop == nil {
				t.Fatalf("shot %d 是特写，必须有裁切框", k)
			}
			// 裁切框在页内：越界会让裁出来的图右边带黑边（两端都带，SSIM 反而不敏感）。
			scale := float64(w) / float64(slideWidthEMU)
			x := int64(math.Round(float64(s.Crop.X) * scale))
			y := int64(math.Round(float64(s.Crop.Y) * scale))
			cw := int64(math.Round(float64(s.Crop.Width) * scale))
			ch := int64(math.Round(float64(s.Crop.Height) * scale))
			if x < 0 || y < 0 || x+cw > w || y+ch > h {
				t.Fatalf("shot %d 裁切框 (%d,%d %dx%d) 越出 %dx%d", k, x, y, cw, ch, w, h)
			}
			// 与画布同比例：两端按 fill 拉伸，比例不同就会变形——两边一起变形时
			// SSIM 反而更好看，判据失去意义。
			if math.Abs(float64(cw)/float64(ch)-float64(w)/float64(h)) > 0.02 {
				t.Fatalf("shot %d 裁切比 %.3f 与画布比 %.3f 差太多", k, float64(cw)/float64(ch), float64(w)/float64(h))
			}
		}
		cursor += s.DurationMs
	}
	if cursor != durMs {
		t.Fatalf("镜头总长 = %d，want %d", cursor, durMs)
	}
}

// 短页 + 多镜：叠化按镜头长度收窄，不能因写死 120ms 而被 IR 校验拒掉。
func TestPageShotInputsShrinkFadeForShortPages(t *testing.T) {
	const (
		w, h  = 640, 360
		durMs = int64(500)
	)
	shots := pageShotInputs(durMs, 4, elementBoxes(w, h), w, h, float64(slideWidthEMU)/float64(w))
	if len(shots) != 4 {
		t.Fatalf("镜头数 = %d，want 4", len(shots))
	}
	var cursor int64
	for k, s := range shots {
		if int64(s.CrossFadeMs) >= s.DurationMs {
			t.Fatalf("shot %d 叠化 %d 不短于时长 %d", k, s.CrossFadeMs, s.DurationMs)
		}
		cursor += s.DurationMs
	}
	if cursor != durMs {
		t.Fatalf("镜头总长 = %d，want %d", cursor, durMs)
	}
}

func TestCheckPlanRejectsStaticPlan(t *testing.T) {
	static := &render.Timeline{Pages: []render.Page{{Index: 0, DurationMs: 1000, BaseImage: "pages/p001.png"}}}
	if err := checkPlan(static, 1); err == nil {
		t.Fatal("没有层的计划必须被拒：否则 V13 会比两张静态图")
	}
	empty := &render.Timeline{}
	if err := checkPlan(empty, 1); err == nil {
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
