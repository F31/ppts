package render

import (
	"fmt"
	"strings"

	"github.com/F31/ppts/internal/animation"
)

// 同一分段上多条入场若挤在前几秒（预填/错开 140ms），视为「没按讲解铺开」。
// 超过这个跨度就认为作者已经手调过，不再改。
const staggerClusterMs int64 = 5000

// revisionWithSpreadStarts 复制规格，把挤在同一分段开头的入场铺到该段时长上。
//
// 时间轴经常一页只有一段音频（整页一条 TTS）。预填会把所有要点绑到这一段，
// 再用 stagger 写成 0/140/280ms。编译后动效在页首两秒播完，后面几十秒讲解对着静图。
// 这里按段长均分起点，讲到后半页时对应要点才入场。不改库里的规格。
//
// onlyEntrance 为真时，收集与写回都跳过非入场 clip：spotlight 旁路轨不占 n，
// 也不被改 StartMs。有效 BeatSheet 的注视页走这条，孔的时钟来自拍表。
func revisionWithSpreadStarts(rev *animation.Revision, offsets map[string]int64, pageMs int64, onlyEntrance bool) *animation.Revision {
	if rev == nil || pageMs <= 0 {
		return rev
	}
	out := *rev
	out.Tracks = make([]*animation.Track, len(rev.Tracks))
	type ref struct {
		ti, ci int
		seg    string
		start  int64
	}
	var items []ref
	seenTrackSeg := map[string]struct{}{}
	for ti, tr := range rev.Tracks {
		if tr == nil {
			continue
		}
		cp := *tr
		cp.Clips = make([]*animation.Clip, len(tr.Clips))
		for ci, cl := range tr.Clips {
			if cl == nil {
				continue
			}
			cpc := *cl
			cp.Clips[ci] = &cpc
			if onlyEntrance && !animation.IsEntranceEffect(cpc.Type) {
				continue
			}
			if !strings.HasPrefix(cpc.Trigger, animation.TriggerSegmentPrefix) {
				continue
			}
			seg := strings.TrimPrefix(cpc.Trigger, animation.TriggerSegmentPrefix)
			if seg == "" {
				continue
			}
			key := fmt.Sprintf("%d\x00%s", ti, seg)
			if _, dup := seenTrackSeg[key]; dup {
				continue
			}
			seenTrackSeg[key] = struct{}{}
			items = append(items, ref{ti, ci, seg, cpc.StartMs})
		}
		out.Tracks[ti] = &cp
	}
	groups := map[string][]int{}
	for i, it := range items {
		groups[it.seg] = append(groups[it.seg], i)
	}
	for seg, idxs := range groups {
		if len(idxs) < 2 {
			continue
		}
		var lo, hi int64 = items[idxs[0]].start, items[idxs[0]].start
		for _, i := range idxs[1:] {
			if items[i].start < lo {
				lo = items[i].start
			}
			if items[i].start > hi {
				hi = items[i].start
			}
		}
		if hi-lo > staggerClusterMs {
			continue
		}
		off, ok := offsets[seg]
		if !ok {
			off = 0
		}
		end := pageMs
		for other, o := range offsets {
			if other == seg {
				continue
			}
			if o > off && o < end {
				end = o
			}
		}
		span := end - off
		if span < staggerClusterMs*2 {
			continue
		}
		n := int64(len(idxs))
		for k, i := range idxs {
			it := items[i]
			start := int64(k) * span / n
			segPrefix := animation.TriggerSegmentPrefix + it.seg
			for _, cl := range out.Tracks[it.ti].Clips {
				if cl == nil || cl.Trigger != segPrefix {
					continue
				}
				if onlyEntrance && !animation.IsEntranceEffect(cl.Type) {
					continue
				}
				cl.StartMs = start
			}
		}
	}
	return &out
}
