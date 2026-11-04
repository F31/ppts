package app

import (
	"fmt"
	"strings"

	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/project"
)

// 本文件回答「这一段建议指谁」，答案是**只读**的。
//
// 与 Q7 的动画预填分工：那边把规则绑定结果直接落进 animation revision（带来源标注、
// 可批量撤销）；焦点这边不落库。原因有三条，都是产品口径而不是技术限制：
//
//  1. FocusTarget 是单值且本页焦点互斥，而 bind.Run 是「多个元素 → 同一段」的多对一。
//     一段命中多个元素时取哪一个没有依据，替用户猜就是「静默绑错」。
//  2. FocusTarget 没有 source / confidence 字段。Q7 的「来源标注 + 撤销后落库为 manual」
//     在讲稿侧没有落点；而 Store.Update 的三态是「focus 缺席 = 保留」，自动写进去的
//     错焦点会被当成既成事实，用户改个错别字也不会把它抹掉。
//  3. 规则绑定器只产出 ShapeID，产不出 BarID；图表切开后候选清单里只有柱子，
//     没有「整块图表」这一项。把 shape id 硬凑成一根柱子，等于让机器随便挑一根数。
//
// 所以这个函数只产出建议，由编辑器展示、用户显式采纳后走**既有**的讲稿保存通道。
// 它不写任何存储、不读任何存储，同一输入必然同一输出。

// FocusSuggestion 是一段的建议。Candidates 为空表示「不给建议」，Reason 说明为什么。
//
// 给的是**一组**候选而不是一个：真实编号卡片页上一段话会命中 4 个元素
// （编号 + 标题 + 描述 + 容器），而焦点是单值。从 4 个里替用户挑 1 个就是
// 「静默绑错」；全不给又等于功能恒空。列出所有真的可选的目标，由人挑。
//
// 每段都有一条记录（而不是只返回有候选的段）：界面要能区分「这一段没建议」
// 与「这一段根本没参与计算」，前者要给人一个说法，否则看起来像功能坏了。
type SuggestedFocus struct {
	Focus  narration.FocusTarget
	Source string // bind.Binding 的规则来源，供界面标注「规则建议」
}

type FocusSuggestion struct {
	SegmentID  string
	Candidates []SuggestedFocus
	Reason     string
}

// PageFocusSuggestions 是一页的建议结果。
type PageFocusSuggestions struct {
	Suggestions []FocusSuggestion
	// Band 是 bind 的版面分档（structured / low-confidence），透出来让界面说明可信度。
	Band string
	// Note 是整页说明。低置信页不预填的原因写在这里，而不是塞进每段的 Reason。
	Note string
}

// SuggestFocus 用 Q7 的规则绑定器给每页每段算一个焦点建议。
//
// 三道闸门，缺一道就宁可不建议：
//   - 版面/讲稿分档必须是 structured（低置信页规则召回只有 0.53–0.67）；
//   - 这一段只能唯一命中一个元素（焦点是单值）；
//   - 命中的元素必须真的出现在 FocusTargetsForPage 的候选里
//     （「编辑器里选得到的，就是成片里指得到的」，见 focustargets.go）。
func SuggestFocus(page *project.Page, segments []*narration.Segment) PageFocusSuggestions {
	out := PageFocusSuggestions{Suggestions: make([]FocusSuggestion, 0, len(segments))}
	if page == nil {
		out.Note = "这一页还没有解析产物，无从建议，请逐段手选。"
		return out
	}
	if len(segments) == 0 {
		out.Note = "这一页还没有讲稿，先写稿再来谈焦点。"
		return out
	}

	elems := PageBindElements(page, 0, 0)
	bindSegs := make([]bind.Segment, 0, len(segments))
	for _, s := range segments {
		if s == nil {
			continue
		}
		text := strings.TrimSpace(s.DisplayText)
		if text == "" {
			text = strings.TrimSpace(s.SpokenText)
		}
		bindSegs = append(bindSegs, bind.Segment{ID: s.SegmentID, Text: text})
	}
	res := bind.Run(elems, bindSegs, bind.Options{})
	out.Band = string(res.Band)

	// 闸门一：低置信页不自动建议。Q7 的动画预填会「照出但标低置信」，那是因为它
	// 带来源标注且可批量撤销；焦点没有这两样，照出就等于写死。
	if res.Band != bind.BandStructured {
		out.Note = fmt.Sprintf(
			"版面/讲稿缺少结构信号（%s），规则在低置信页上的召回只有 0.53–0.67；不自动建议，请逐段手选。",
			res.BandReason)
		for _, s := range segments {
			if s == nil {
				continue
			}
			out.Suggestions = append(out.Suggestions, FocusSuggestion{
				SegmentID: s.SegmentID, Reason: "低置信页不自动建议，请手选",
			})
		}
		return out
	}

	// 候选清单是「成片里真正指得到」的那份权威集合。只收 BarID 为空的整形状：
	// 绑定器给的是 shape id，图表切开后候选里只有柱子，没有整块图表——
	// 这种情况下不给建议，而不是随便挑一根柱子。
	//
	// 顺便用候选清单的遍历序当输出顺序：它就是界面上的阅读顺序，也是唯一
	// 稳定的排序依据（Binding.Score 对非字面规则恒为 0，拿它排会随实现漂）。
	candidateOrder := make([]string, 0)
	for _, t := range FocusTargetsForPage(page).Targets {
		if t.BarID == "" {
			candidateOrder = append(candidateOrder, t.ShapeID)
		}
	}

	bySegment := map[string][]bind.Binding{}
	for _, b := range res.Bindings {
		bySegment[b.SegmentID] = append(bySegment[b.SegmentID], b)
	}

	for _, s := range segments {
		if s == nil {
			continue
		}
		sug := FocusSuggestion{SegmentID: s.SegmentID}
		hits := bySegment[s.SegmentID]
		hitSource := map[string]string{}
		for _, b := range hits {
			if _, seen := hitSource[b.ElementID]; !seen {
				hitSource[b.ElementID] = b.Source
			}
		}
		// 闸门：只收既被绑定器命中、又在可选清单里的目标。装饰底板、容器、
		// 以及「整块图表」（候选里只有柱子）都会在这一步自然落掉。
		for _, id := range candidateOrder {
			if src, ok := hitSource[id]; ok {
				sug.Candidates = append(sug.Candidates, SuggestedFocus{
					Focus: narration.FocusTarget{ShapeID: id}, Source: src,
				})
			}
		}
		switch {
		case len(hits) == 0:
			sug.Reason = "这一段没有可绑定的元素"
		case len(sug.Candidates) == 0:
			sug.Reason = "命中的元素都不是可选焦点（装饰形状、容器或整块图表），不硬凑"
		case len(sug.Candidates) == 1:
			sug.Reason = "规则唯一命中，且该目标在可选清单里"
		default:
			sug.Reason = fmt.Sprintf("这一段命中 %d 个可选焦点，请挑一个", len(sug.Candidates))
		}
		out.Suggestions = append(out.Suggestions, sug)
	}
	return out
}
