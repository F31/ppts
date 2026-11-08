package render

import (
	"fmt"

	"github.com/F31/ppts/internal/animation"
)

// 本文件是「跨页延续」的整组入口：把一页里演不完的 clip 拆出余量段注入后续页。
// 四个函数必须一起读——injectContinuations 逐页推进 offset，continuationSegFor 算余量段，
// injectCarry 补页缝处的静态盒跳变，indexOfLayerByElementID 找承接层。
// 任何一处单独改动都会破坏「页缝两侧进度逐位相同」这条不变量，而它没有别的判据能兜住。

// injectCarry 给次页的续播层补一段「静态盒补偿」，把视觉矩形从上一页的盒线性搬回本页的盒。
//
// 为什么需要它：延续段的负起点只让**属性进度**在页缝处连续。元素在相邻页的静态盒（位置 +
// 尺寸）若不同，页缝两侧的绝对几何就有一个跳变——成片里表现为元素「跳一格」，而所有既有
// 判据（求值连续性、末帧比对）都看不见它，因为 clip 状态确实是连续的。
//
// 两个盒完全相同时不产生任何 clip（CarryProps 返回 nil），所以作者按老写法把元素放在同一
// 位置时，清单与引入本能力前逐字节一致。
func (c *Compiler) injectCarry(prevPage, page *Page, li int, elementID string, seg *Motion) {
	pi := indexOfLayerByElementID(prevPage.Layers, elementID)
	if pi < 0 {
		// 源段只可能在「有同 elementId 层」的页上产出（compileClip 逐层展开），走到这里
		// 说明计划本身不自洽。不加补偿好过加一段来路不明的补偿——几何不连续会由 V19 暴露。
		return
	}
	props := CarryProps(prevPage.Layers[pi].Box, page.Layers[li].Box, page.Layers[li].Origin)
	if len(props) == 0 {
		return
	}
	ms := CarryDuration(seg.DurationMs-seg.ContinuationOffsetMs, page.DurationMs)
	if ms < 1 {
		return
	}
	page.Layers[li].Clips = append(page.Layers[li].Clips, Motion{
		ClipID:     seg.ClipID + "@carry",
		Effect:     "carry",
		StartMs:    0,
		DurationMs: ms,
		Easing:     seg.Easing,
		Props:      props,
		Carry:      true,
		ElementID:  elementID,
	})
}

// injectContinuations 遍历每页的 continues clip，把溢出余量段写入后续页的对应层。
// 单跳与链式同一条路径：每注入一页，若该段在本页内仍未演完，就把 offset 加上本页页长、
// 重新以负起点平移注入下一页（G8 边界①）。次页缺失或后续页无同 elementId 层时记
// continuation_target_missing 诊断并停止注入（已有段仍有效，不崩、不改变非延续语义）。
func (c *Compiler) injectContinuations(pages []PageInput, out *Timeline) {
	for i := range out.Pages {
		in := pages[i]
		if in.Revision == nil {
			continue
		}
		intensity := in.Revision.Intensity
		if intensity <= 0 {
			intensity = 1
		}
		for ti := range in.Revision.Tracks {
			track := in.Revision.Tracks[ti]
			if track.ElementID == "" {
				continue // 整页轨道无跨页身份，不参与延续
			}
			for ci := range track.Clips {
				clip := track.Clips[ci]
				if !clip.Continues {
					continue
				}
				seg, diag := c.continuationSegFor(in, track, clip, intensity)
				if diag != nil {
					out.Warnings = append(out.Warnings, *diag)
					continue
				}
				if seg == nil {
					continue
				}
				if i+1 >= len(out.Pages) {
					out.Warnings = append(out.Warnings, Diagnostic{
						Code: DiagContinuationTargetMissing, Page: in.Index,
						ElementID: track.ElementID, ClipID: clip.ID,
						Message: "这是最后一页，无次页承接延续 clip，已退化为本页截断",
					})
					continue
				}

				// 逐页注入，直到 clip 演完或没有下一页：G8 边界① 链式多页。
				// 每一跳的 offset 是「上一跳 offset + 上一页页长」——它等于该段在页尾处的
				// clip 时间；负起点（StartMs=-offset）因此始终把求值平移回同一条原 clip
				// 时间轴，链上任意页缝两侧的进度都逐位相同（与单跳同一数学，只是重复 N 次）。
				frame := frameMS(c.opt.FPS)
				cur := *seg
				for pageIdx := i + 1; ; pageIdx++ {
					if pageIdx >= len(out.Pages) {
						// 片长到此结束：余量被视频长度裁掉，不崩。留痕便于作者判断是否需要
						// 补页——这与单跳「这是最后一页」是同一类信息，只是发生在链尾。
						out.Warnings = append(out.Warnings, Diagnostic{
							Code: DiagContinuationTargetMissing, Page: in.Index,
							ElementID: track.ElementID, ClipID: clip.ID,
							Message: fmt.Sprintf("链式延续到达末页仍有 %dms 余量，已被片长裁掉",
								cur.DurationMs-cur.ContinuationOffsetMs),
						})
						break
					}
					page := &out.Pages[pageIdx]
					li := indexOfLayerByElementID(page.Layers, track.ElementID)
					if li < 0 {
						msg := "次页无同 elementId 层承接延续 clip，已退化为本页截断"
						if pageIdx > i+1 {
							msg = fmt.Sprintf("第 %d 页无同 elementId 层承接延续 clip，链式延续在此中断（前面的段仍有效）",
								pageIdx+1)
						}
						out.Warnings = append(out.Warnings, Diagnostic{
							Code: DiagContinuationTargetMissing, Page: in.Index,
							ElementID: track.ElementID, ClipID: clip.ID, Message: msg,
						})
						break
					}
					// 静态盒补偿（G8 边界③）：动效连续只保证「进度」连续，不保证「位置」连续。
					// 作者在次页把同一元素挪了位置或改了尺寸时，页缝处会跳一格——那是静态几何
					// 的差，不是动效的差。给续播层补一条 carry clip，把视觉矩形从上一页的盒搬回
					// 本页的盒。上一页的盒取 pageIdx-1（链式时是上一跳所在页，不是源页）。
					c.injectCarry(&out.Pages[pageIdx-1], page, li, track.ElementID, &cur)
					page.Layers[li].Clips = append(page.Layers[li].Clips, cur)

					// 本页内（含 1 帧容差）能演完就收手；否则把剩余部分再平移到下一页。
					if cur.ContinuationOffsetMs+page.DurationMs+frame >= cur.DurationMs {
						break
					}
					nextOffset := cur.ContinuationOffsetMs + page.DurationMs
					cur.StartMs = -nextOffset
					cur.ContinuationOffsetMs = nextOffset
				}
			}
		}
	}
}

// continuationSegFor 计算一个 continues clip 溢出到次页的余量 Motion（不修改入参）。
// 返回的 Motion 携带 IsContinuation=true / ContinuationOffsetMs / ElementID，可直接注入次页同元素层。
// 返回 (nil, nil) 表示该 clip 无需延续（完全落在页内、起点已超页尾、或余量≤0）。
// 若起点已超本页尾等无法推断次页起点的情况，返回 (nil, diag) 供调用方降级处理。
//
// 余量段沿用完整 from→to 属性，并用负起点（StartMs = -offset）把本段「平移」回原 clip 时间轴，
// 其中 offset = 本页时长 - 本页段起点。在页 N+1 的 localMs 处，evaluateLayer 求得的进度是
// (localMs + offset)/D，与原 clip 在「页缝 + localMs」处的进度完全一致——cubic-bezier 缓动在页缝处
// 天然连续，避免了「两段预拆分 Props 再各自施加缓动」导致的二次缓动跳变（设计 §2）。
func (c *Compiler) continuationSegFor(in PageInput, track *animation.Track, clip *animation.Clip,
	intensity float64) (*Motion, *Diagnostic) {
	anchor, ok, _ := resolveTrigger(clip.Trigger, in.SegmentOffsets, in.Index, clip.ID)
	if !ok {
		return nil, nil
	}
	start := anchor + clip.StartMs
	if start < 0 {
		start = 0
	}
	if start >= in.DurationMs {
		// MVP 不支持「起点落在次页」的延续：次页段起点固定为 0，无法对齐。
		return nil, &Diagnostic{Code: DiagContinuationTargetMissing, Page: in.Index,
			ElementID: track.ElementID, ClipID: clip.ID,
			Message: "continues clip 起点超出本页尾，无本页段且无法推断次页起点，已跳过"}
	}
	residue := start + clip.DurationMs - in.DurationMs
	if residue <= 0 {
		return nil, nil
	}
	offset := in.DurationMs - start
	if clip.DurationMs <= 0 {
		return nil, &Diagnostic{Code: DiagContinuationTargetMissing, Page: in.Index,
			ElementID: track.ElementID, ClipID: clip.ID,
			Message: "continues clip 时长为 0，无法跨页拆分，已退化为本页截断"}
	}

	plan, err := animation.PlanMotion(clip.Type, clip.Params, clip.Easing, intensity)
	if err != nil {
		return nil, &Diagnostic{Code: "plan_motion_failed", Page: in.Index,
			ElementID: track.ElementID, ClipID: clip.ID, Message: err.Error()}
	}
	// 次页段沿用完整 from→to 属性，并用负起点把本段「平移」回原 clip 时间轴：
	// 在页 N+1 的 localMs 处，evaluateLayer 求得的进度是 (localMs + offset)/D，
	// 与原 clip 在「页缝 + localMs」处的进度完全一致——cubic-bezier 缓动在页缝处
	// 连续，无二次施加导致的跳变（design §2）。offset 仅作审计/对齐用。
	m := Motion{
		ClipID:               clip.ID,
		Effect:               clip.Type,
		StartMs:              -offset,
		DurationMs:           clip.DurationMs,
		Easing:               plan.Curve,
		Props:                c.motionProps(plan),
		IsContinuation:       true,
		ContinuationOffsetMs: offset,
		ElementID:            track.ElementID,
	}
	return &m, nil
}

// indexOfLayerByElementID 返回同一 elementId 的层下标，找不到返回 -1。
func indexOfLayerByElementID(layers []Layer, elementID string) int {
	for i, l := range layers {
		if l.ElementID == elementID {
			return i
		}
	}
	return -1
}
