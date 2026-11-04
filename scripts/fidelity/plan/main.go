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
	"sort"
	"strconv"
	"strings"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/design"
	"github.com/F31/ppts/internal/project"
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
	// Text 是元素的文字内容。plan 编译用不到它，但讲稿绑定预填靠它与分段文本算相似度——
	// 缺了它，预填在样片上只能靠结构规则，验出来的是「结构规则能对齐」，不是「预填能对齐」。
	Text string `json:"text,omitempty"`
	// Enter 覆盖按 kind 选的默认入场方式。目前只用于柱子：grow 让柱高从 0 长到终值，
	// 锚点由编译期从 scaleY 推出来（见 render.originFor），这里不必声明。
	Enter string `json:"enter,omitempty"`
	// Value 是图表的数值（秒）。plan 编译用不到它，但 check_chart 要用它
	// 核对「柱高与标注值成比例」——把数值留在元素清单里，判据才不必去解析图上的字。
	Value *int `json:"value,omitempty"`
	// Chart 是图表形状的附加信息（extract_pptx_elements.py 产出）。
	// plan 只关心 Bars：讲稿里写「讲 174 那根」时，焦点要落成一块矩形，
	// 而矩形只能来自这里——图表是一张位图，柱子不是图层。
	Chart *chartInfo `json:"chart,omitempty"`
}

// chartInfo 只取 plan 用得上的部分，其余字段（类别、系列值）留给判据脚本。
type chartInfo struct {
	ChartType string `json:"chartType"`
	// DataLabels 决定焦点矩形要不要向上扩：柱子顶上的数字画在柱体包围盒**之外**。
	DataLabels bool       `json:"dataLabels"`
	Bars       []chartBar `json:"bars"`
}

// chartBar 是 split_chart_bars.py 从页面图切出来的一根柱：坐标与元素同一套（计划像素）。
type chartBar struct {
	ID  string `json:"id"`
	Box [4]int `json:"box"`
	// LabelPad 是数据标签带相对柱体的方向与距离，由切柱脚本在页面图上量出来。
	// 垂直柱在柱顶上方（up），水平条在条的右端（right）。缺省表示没量到
	// （或清单来自还没做这一步的旧版本），此时退回下面的实测常数。
	LabelPad *barLabelPad `json:"labelPad,omitempty"`
}

type barLabelPad struct {
	Dir string `json:"dir"` // "up" | "right"
	Px  int    `json:"px"`
}

// focusLabelPadPx 是**量不到标签带时**的兜底高度（垂直柱）。
//
// 柱顶的数据标签画在柱体包围盒之外：实测文字顶边在柱顶上方 41px（标签带高 22px），
// 只框柱体时「讲的是这根」对了，但这根自己的数字被压在纱下面——图上最该清楚的数
// 反而灰了。45px 是量出来的，不是估的（scripts/fidelity/mask_preview.py 的
// --label-pad 默认值与它同源），现在切柱脚本也按同一口径逐根量（42px + 3px 余量），
// 这个常数只留给「清单里还没有 labelPad 字段」的老产物。
const focusLabelPadPx = 45

// barRects 把「柱 id → 矩形」摊平成一页一张表。
//
// 不在这里校验矩形是否落在画布内：那是 Validate 的活，且它同时守着
// 「服务端收到的 plan 也不能越界」。这里只负责把数据送到焦点上——但**扩标签时
// 要自己夹住**：向右扩过头会越出画布右缘，而 Validate 只报「原点为负」，
// 右侧越界它不查。
func barRects(elems []element, canvasW int) map[string]render.Box {
	out := make(map[string]render.Box)
	for _, e := range elems {
		if e.Chart == nil {
			continue
		}
		for _, b := range e.Chart.Bars {
			if b.Box[2] <= 0 || b.Box[3] <= 0 {
				// 切柱脚本已经保证不写无效框；真出现了就跳过并留痕，
				// 免得一个 0×0 的矩形被当成「已指定」而让焦点静默无效。
				log.Printf("警告：%s 的柱子 %q 包围盒无效 %v，忽略", e.ID, b.ID, b.Box)
				continue
			}
			r := render.Box{X: b.Box[0], Y: b.Box[1], W: b.Box[2], H: b.Box[3]}
			if e.Chart.DataLabels {
				// 只有带标签的图才需要扩：没有标签时扩出一块空白，
				// 等于在柱子旁边留一条没被压暗的带，反而更显眼。
				if b.LabelPad != nil && b.LabelPad.Dir == "right" && b.LabelPad.Px > 0 {
					// 水平条：标签在右端，宽度随位数变（"174" 与 "96" 差一倍），
					// 一个常数盖不住，所以按根量、按根扩。
					pad := b.LabelPad.Px
					if room := canvasW - (r.X + r.W); room < pad {
						pad = room
					}
					if pad > 0 {
						r.W += pad
					}
				} else {
					pad := focusLabelPadPx
					if b.LabelPad != nil && b.LabelPad.Dir == "up" && b.LabelPad.Px > 0 {
						pad = b.LabelPad.Px
					}
					if r.Y < pad {
						pad = r.Y // 贴着画布顶边时能扩多少扩多少
					}
					r.Y -= pad
					r.H += pad
				}
			}
			out[b.ID] = r
		}
	}
	return out
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

// focusAtRef 是「句内」的焦点切换：这一句讲到第 AtMs 毫秒时，注意力转到 ElementID。
//
// 为什么需要它：一句 3 秒的话里念两个数字，而段级 focus 只能给出一个切换点，
// 于是这 3 秒画面必然不动（V8 量的就是它）。把切换点落到句内，等于承认
// 「一句话里可以有两次强调」——这也是唯一不需要给渲染器新增效果的做法。
type focusAtRef struct {
	ElementID string `json:"elementId"`
	AtMs      int64  `json:"atMs"`
}

type scriptSegment struct {
	ID         string    `json:"id"`
	Text       string    `json:"text"`
	StartMs    int64     `json:"startMs"`
	EndMs      int64     `json:"endMs"`
	DurationMs int64     `json:"durationMs"`
	Bind       []bindRef `json:"bind"`
	// Focus 是「这一段讲的是哪个元素」。它与 Bind 是两件事：
	// Bind 决定元素何时**出场**，Focus 决定讲它时谁**让位**。
	// 混用会产生两种病：绑得太早（元素齐活，后面全静）或绑得太晚（讲到才出现，前面全空）。
	Focus string `json:"focus,omitempty"`
	// FocusAt 是句内焦点切换点，时间相对本段开口。
	FocusAt []focusAtRef `json:"focusAt,omitempty"`
}

type scriptPage struct {
	Index      int             `json:"index"`
	SlideID    string          `json:"slideId"`
	DurationMs int64           `json:"durationMs"`
	Segments   []scriptSegment `json:"segments"`
	// Static 是「页首就在、不参与入场」的元素：容器底板、分隔线这类东西。
	// 不给它们建轨道，它们就留在底板里（stagePages 只擦有轨道的层），
	// 于是页一开就完整，既没有空框悬挂，也不会在别人的包围盒里制造灰度变化
	// （后者会把子元素的实测 onset 全部拉平到容器淡入的那一刻）。
	Static []string `json:"static,omitempty"`
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
	static := make(map[string]struct{}, len(page.Static))
	for _, id := range page.Static {
		static[id] = struct{}{}
	}
	var unbound []string
	for _, e := range elems {
		if _, ok := static[e.ID]; ok {
			// 常驻元素不建轨道：留在底板里，页首就完整。绑了也按常驻处理——两者语义冲突，
			// 常驻更保守（少一次不该有的淡入），日志留痕。
			if prev, bound := bound[e.ID]; bound {
				log.Printf("警告：%s 同时是常驻元素和段 %q 的绑定，按常驻处理（不建轨道）", e.ID, prev.segment)
			}
			continue
		}
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
		switch {
		case e.Enter == "grow":
			// 生长要看得见「长」这件事：520ms 的淡入里看不出高度在变。
			// catalog 给 grow 的范围是 400–2000，而速度分层对小元素只给到 400——
			// 取 400 是两者唯一不冲突的取值（取 700 会被判 duration_tier_conflict）。
			clip.Type = "grow"
			clip.DurationMs = 400
			// 必须 easeOut：easeInOut 的起步段几乎没有位移，柱子前 100ms 在画面上
			// 等于没动，实测被记成「开口后 120ms 才动」（V5.2 判红），错落也跟着错位。
			clip.Easing = "easeOut"
		case e.Enter != "":
			clip.Type = e.Enter
		case e.Kind == "title":
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

// focusReleaseGapMS 是「这一句讲完」到「下一句开口讲下一个对象」之间，允许直接
// 交棒的最大空档。小于它就把压暗保持到下一段开始——下一句紧接着讲下一个对象时
// 放手，画面会闪一下全亮，比一直压着更难听。大于它就必须放手：那段时间旁白在
// 说别的（页 2 的「两核跑五分钟，」「两条并发，」），压着上一个对象等于指错人。
const focusReleaseGapMS = 500

// focusSpans 把「这一段讲的是谁」编译成页级焦点序列。
//
// 只有带 Focus 的段参与：段的顺序就是注意力转移的顺序，段的开口时刻就是切换时刻。
// 少于两个不同的焦点元素时不启用——一个元素从头亮到尾，没有「让位」可言，
// 生成出来的序列只会让求值器每帧多查一次表。
//
// rects 是「柱 id → 矩形」，来自元素清单里图表形状的 bars。命中就把它挂到焦点上：
// 那一刻的让位交给遮罩（整页白纱、矩形留孔），而不是图层压暗——柱子没有图层。
//
// 注意这里**不看** Bind：出场与焦点已经解耦。把两者绑在一起，就会出现
// 「元素一起出场后再无变化」的静默页（V8 量的正是它）。
func focusSpans(page scriptPage, rects map[string]render.Box) []render.FocusSpan {
	static := make(map[string]struct{}, len(page.Static))
	for _, id := range page.Static {
		static[id] = struct{}{}
	}

	// 两路来源合成一条时间线：段级 Focus 落在段开口，FocusAt 落在句内偏移。
	// segEnd 一并带上：焦点的「该讲完了」就是它所属那一段的结束时刻。
	type at struct {
		ms     int64
		segEnd int64
		id     string
		from   string
	}
	var all []at
	for _, seg := range page.Segments {
		end := seg.EndMs
		if end == 0 {
			end = seg.StartMs + seg.DurationMs
		}
		if seg.Focus != "" {
			all = append(all, at{seg.StartMs, end, seg.Focus, seg.ID})
		}
		for _, fa := range seg.FocusAt {
			all = append(all, at{seg.StartMs + fa.AtMs, end, fa.ElementID,
				fmt.Sprintf("%s@%dms", seg.ID, fa.AtMs)})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].ms < all[j].ms })

	var spans []render.FocusSpan
	var owns []int64 // 与 spans 一一对应：该焦点所属段的结束时刻
	seen := make(map[string]struct{}, len(all))
	for _, a := range all {
		if _, ok := static[a.id]; ok {
			// 常驻元素不建轨道，不能被让位：它不在图层里，压暗也无从谈起。
			log.Printf("警告：页 %d 焦点 %q（来自 %q）是常驻元素，忽略", page.Index, a.id, a.from)
			continue
		}
		if _, dup := seen[a.id]; dup {
			// 同一元素二次成为焦点会与「唯一焦点」的假设冲突（压暗量按元素查表）。
			continue
		}
		// 焦点序列要求起点严格递增：同一毫秒出现两个焦点，后一个会被校验拒掉。
		// 句内偏移与前一段的段级焦点撞车时，先到先得。
		if n := len(spans); n > 0 && a.ms <= spans[n-1].StartMs {
			log.Printf("警告：页 %d 焦点 %q 与上一个同一时刻（%dms），跳过", page.Index, a.id, a.ms)
			continue
		}
		seen[a.id] = struct{}{}
		span := render.FocusSpan{StartMs: a.ms, ElementID: a.id}
		if r, ok := rects[a.id]; ok {
			span.Rect = &r
			log.Printf("页 %d：焦点 %q 是图表柱子（来自 %q），矩形 (%d,%d,%d,%d)",
				page.Index, a.id, a.from, r.X, r.Y, r.W, r.H)
		}
		spans = append(spans, span)
		owns = append(owns, a.segEnd)
	}
	// 少于两个不同的焦点元素时不启用：一个元素从头亮到尾，没有「让位」可言，
	// 生成出来的序列只会让求值器每帧多查一次表。
	if len(spans) < 2 {
		return nil
	}
	// 给每个跨度一个终点：讲它的那一段结束、而下一段又隔了足够久，就放手。
	// 不写 EndMs 等于「保持到下一段开始」，那正是「讲 B 时还压着 A」的来源。
	for i := range spans {
		next := page.DurationMs
		if i+1 < len(spans) {
			next = spans[i+1].StartMs
		}
		if g := next - owns[i]; g >= focusReleaseGapMS {
			spans[i].EndMs = owns[i]
			log.Printf("页 %d：焦点 %s 在 %dms 放手（空档 %dms）",
				page.Index, spans[i].ElementID, spans[i].EndMs, g)
		}
	}
	return spans
}

// prefillRevision 用**产品侧**的两段代码生成轨道：animation/bind 算绑定，
// animdefault 把绑定翻成轨道。本文件自己不再拼一套「差不多的」编排。
//
// 为什么非要用产品代码：手写 bind 的样片只能证明「按段起播能对上开口」，
// 那件事 Q7 已经证过了。这一版要证的是「**预填算出来的绑定**能对上开口」——
// 中间多了一次规则推断和一次预设翻译，任何一处偏一点，元素就会早到或迟到。
func prefillRevision(elems []element, sp scriptPage, pageMs int64) *animation.Revision {
	bel := make([]bind.Element, 0, len(elems))
	ael := make([]animdefault.Element, 0, len(elems))
	for _, e := range elems {
		bel = append(bel, bind.Element{
			ID: e.ID, Kind: e.Kind, Text: e.Text,
			Box: bind.Box{X: float64(e.Box[0]), Y: float64(e.Box[1]), W: float64(e.Box[2]), H: float64(e.Box[3])},
		})
		ael = append(ael, animdefault.Element{
			ID: e.ID, Kind: e.Kind,
			Box: project.Box{
				X: int64(e.Box[0]) * emuPerPx, Y: int64(e.Box[1]) * emuPerPx,
				Width: int64(e.Box[2]) * emuPerPx, Height: int64(e.Box[3]) * emuPerPx,
			},
		})
	}
	segs := make([]bind.Segment, 0, len(sp.Segments))
	ids := make([]string, 0, len(sp.Segments))
	for _, s := range sp.Segments {
		segs = append(segs, bind.Segment{ID: s.ID, Text: s.Text})
		ids = append(ids, s.ID)
	}
	res := bind.Run(bel, segs, bind.Options{})
	rev, report, err := animdefault.SpecFromElements(ael, animdefault.DefaultOptions{
		SlideID: sp.SlideID, Language: "zh-CN", DurationMS: pageMs, FPS: 30,
		SegmentIDs: ids, Bindings: res.Bindings,
	})
	if err != nil {
		log.Fatalf("页 %d：预填生成的规格不合法：%v", sp.Index, err)
	}
	log.Printf("页 %d：预填 %d 条绑定 → 轨道 %d 条、静态 %d 条（分档 %s %v）",
		sp.Index, len(res.Bindings), report.Bound, report.Static, res.Band, res.Signals)
	return rev
}

// prefillScriptPage 把实测绑定写回段落的 bind：像素测量的参照物必须与被测画面同源。
//
// check_alignment 是按 seg.bind 找「哪个元素属于这一段」的。若它读的仍是手写绑定，
// 量到的就是「另一份编排在画面上表现如何」，与预填无关。
func prefillScriptPage(sp scriptPage, rev *animation.Revision) scriptPage {
	bySeg := make(map[string][]bindRef, len(sp.Segments))
	for _, tr := range rev.Tracks {
		for _, c := range tr.Clips {
			if !strings.HasPrefix(c.Trigger, animation.TriggerSegmentPrefix) {
				continue // 整页轨道与 atTime 不是预填产物
			}
			seg := strings.TrimPrefix(c.Trigger, animation.TriggerSegmentPrefix)
			bySeg[seg] = append(bySeg[seg], bindRef{ElementID: tr.ElementID, OffsetMs: c.StartMs})
			break // 同一元素的多条 clip 共用同一 trigger，只记一次
		}
	}
	out := sp
	out.Segments = make([]scriptSegment, len(sp.Segments))
	for i, s := range sp.Segments {
		// 空切片而不是 nil：null 会让读取方在 for ... range 上崩掉，
		// 而「这一段没有绑定」是正常状态（讲解没提到任何元素）。
		s.Bind = append([]bindRef{}, bySeg[s.ID]...)
		out.Segments[i] = s
	}
	return out
}

func main() {
	in := flag.String("in", "", "含 pages/ 与 elements.json 的目录（gen_pages.py 的输出）")
	out := flag.String("out", "job", "job 目录：plan.json 与底图副本写在这里")
	// 元素清单与底图可覆盖：图表实验链是「extract_pptx_elements.py → split_chart_bars.py」，
	// 产出的清单不在 gen_pages.py 的目录里，baseImage 也没人填。给两个开关就能直接编，
	// 不必为了跑一次编译去伪造一份 pages/ 目录。
	elementsPath := flag.String("elements", "",
		"元素清单路径，缺省 <in>/elements.json；图表实验用 split_chart_bars.py 的产物")
	baseImage := flag.String("base-image", "",
		"覆盖清单里的 baseImage（相对 <in>）；图表实验只有一张整页图时用")
	duration := flag.Int64("duration", 6000, "每页时长（毫秒）；给了 -script 时由配音实测值取代")
	// 见 revision() 的注释：默认产物必须与 5.10 的保真基线一致，所以合规档只在新开关下启用。
	tierCompliant := flag.Bool("tier-compliant", false,
		"按 §19.4 速度分层取时长（小元素 320ms / 大元素 700ms）；盲评样片用")
	scriptPath := flag.String("script", "",
		"narrate.py 产出的 segments.json：用 withSegment 按讲解段落起播（页时长也取它的实测值）")
	hardCut := flag.Bool("hard-cut", false,
		"关闭页转场：转场 320ms 与页首段落的起播（220ms）重叠，像素法测不出元素是否准时")
	prefill := flag.Bool("prefill", false,
		"轨道改由产品侧的讲稿绑定预填生成（animation/bind + animdefault），"+
			"而不是读 segments.json 里手写的 bind；需要同时给 -script（用它取分段文本与开口时刻）")
	flag.Parse()
	if *in == "" {
		log.Fatal("-in 必填")
	}

	raw, err := os.ReadFile(filepath.Join(*in, "elements.json"))
	if *elementsPath != "" {
		raw, err = os.ReadFile(*elementsPath)
	}
	if err != nil {
		log.Fatal(err)
	}
	var meta []pageMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		log.Fatal(err)
	}
	if *baseImage != "" {
		// 逗号分隔可以逐页给：一份 deck 里两页各有一张图表时，两页的整页图不同，
		// 而 -base-image 原来只能给一个值、会把两页都指到同一张图上——那两页的
		// 焦点矩形就会落在另一页的画面上，偏偏看起来「有图、有框」。
		names := strings.Split(*baseImage, ",")
		for i := range names {
			names[i] = strings.TrimSpace(names[i])
		}
		if len(names) != 1 && len(names) != len(meta) {
			log.Fatalf("-base-image 给了 %d 个名字，但清单有 %d 页", len(names), len(meta))
		}
		for i := range meta {
			if len(names) == 1 {
				meta[i].BaseImage = names[0]
			} else {
				meta[i].BaseImage = names[i]
			}
		}
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
		if m.BaseImage == "" {
			// 空 baseImage 会让 filepath.Join 退化成目录本身，ReadFile 报
			// 「Incorrect function」（Windows）/「is a directory」（Unix）——
			// 那句话对着「我的清单明明有图」完全对不上号。
			log.Fatalf("页 %d 缺 baseImage：清单是提取器产的，没填底图；用 -base-image 指定整页图",
				m.Index)
		}
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
		var focus []render.FocusSpan
		if script != nil {
			sp := script.Pages[i]
			pageMs = sp.DurationMs
			if *prefill {
				// 验收要的是**产品产出的轨道**，不是这份实验脚本自己拼的轨道：
				// 手写 bind 只能证明「按段起播能对齐」，证明不了「预填算出的绑定能对齐」。
				rev = prefillRevision(m.Elements, sp, pageMs)
				// 像素测量的输入换成实测绑定。check_alignment 读的就是 seg.bind，
				// 让它继续读手写 bind 等于拿另一份编排去解释这段画面。
				sp = prefillScriptPage(sp, rev)
				script.Pages[i] = sp
			} else {
				rev = scriptRevision(m.Elements, sp, *tierCompliant)
			}
			rects := barRects(m.Elements, theme.Canvas.Width)
			focus = focusSpans(sp, rects)
			// 焦点既不是图层、也不是切出来的柱子时，编译会在 Validate 里报
			// 「不是本页图层」——那句话对着「我明明写了柱子的 id」很难对上号。
			// 在这一层先说清楚缺了哪一步：柱子要先切出来才有矩形。
			layerID := make(map[string]struct{}, len(m.Elements))
			for _, e := range m.Elements {
				layerID[e.ID] = struct{}{}
			}
			for _, f := range focus {
				if _, ok := layerID[f.ElementID]; ok {
					continue
				}
				if _, ok := rects[f.ElementID]; !ok {
					log.Fatalf("页 %d 的焦点 %q 既不是本页图层，也没有切出对应的柱子："+
						"先跑 split_chart_bars.py 生成 chart.bars，或用 -elements 指向它的产物",
						sp.Index, f.ElementID)
				}
			}
			offsets = make(map[string]int64, len(sp.Segments))
			for _, seg := range sp.Segments {
				offsets[seg.ID] = seg.StartMs
			}
			if len(focus) > 0 {
				log.Printf("页 %d：焦点序列 %v", sp.Index, focus[:min(len(focus), 8)])
			}
		}
		pages = append(pages, render.PageInput{
			Index: m.Index, SlideID: m.SlideID, DurationMs: pageMs,
			BaseImage: m.BaseImage, Elements: elems, Revision: rev,
			SegmentOffsets: offsets, Focus: focus,
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
	if *prefill && script != nil {
		// 测量脚本读的是这份：bind 是**实测**绑定（产品预填的产物），不是手写编排。
		sb, err := json.MarshalIndent(script, "", "  ")
		if err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(*out, "segments-prefill.json"), sb, 0o644); err != nil {
			log.Fatal(err)
		}
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
