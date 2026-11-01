package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/project"
)

// 本文件是「形状投影 → 绑定器 → 默认规格」三段接起来的端到端验收
// （changes/animation-narration-binding tasks 3.x）。
//
// 前两段各自有测试（bind_elements_test.go 盯输入保真，bind 包盯核心算法与金标），
// 但**接起来**才会暴露第三类问题：绑上的元素在默认生成里根本没有动效模板
// （预设不管这类元素），于是「绑了 18 条、只出 14 条轨」。这不是 bug，是预设的能力边界；
// 但它必须被数字钉住，否则将来会有人把「绑定数」当成「数量对得上」的证据。

const prefillDeckPath = "../animation/animdefault/testdata/sample-customer-deck.pptx"
const prefillScriptPath = "../animation/bind/testdata/q8-golden.json"

// q8Script 只取本用例需要的字段：分段文本 + 夹具元素 id 到动画锚点的对应关系。
type q8Script struct {
	Pages []struct {
		Segments []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"segments"`
	} `json:"pages"`
}

// 夹具里的五页对应 deck 的 1/2/4/8/10 页（0 基 index 0/1/3/7/9）。
var prefillDeckPages = []int{0, 1, 3, 7, 9}

// 基线数字，来自 Q7 实验（53 条绑定）。出轨道的是 45 条：另外 8 条指向容器形状，
// 而 business-calm 不给「无文字形状」挂动效——绑定成立，但没有模板可用。
var wantPrefillTracks = map[int]int{0: 3, 1: 11, 3: 3, 7: 14, 9: 14}

func TestPrefillPipelineOnRealDeck(t *testing.T) {
	raw, err := os.ReadFile(prefillDeckPath)
	if err != nil {
		t.Fatalf("读样本: %v", err)
	}
	doc, err := project.NewGoPPTXReader(project.Limits{}).Inspect(context.Background(), bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("解析样本: %v", err)
	}
	sraw, err := os.ReadFile(prefillScriptPath)
	if err != nil {
		t.Fatalf("读讲稿夹具: %v", err)
	}
	var script q8Script
	if err := json.Unmarshal(sraw, &script); err != nil {
		t.Fatalf("解析讲稿夹具: %v", err)
	}
	if len(script.Pages) != len(prefillDeckPages) {
		t.Fatalf("夹具页数 = %d，期望 %d", len(script.Pages), len(prefillDeckPages))
	}

	totalBind, totalTrack := 0, 0
	for i, pageIdx := range prefillDeckPages {
		segs := make([]bind.Segment, 0, len(script.Pages[i].Segments))
		ids := make([]string, 0, len(script.Pages[i].Segments))
		for _, s := range script.Pages[i].Segments {
			segs = append(segs, bind.Segment{ID: s.ID, Text: s.Text})
			ids = append(ids, s.ID)
		}
		page := doc.Pages[pageIdx]

		res := bind.Run(PageBindElements(page, 0, 0), segs, bind.Options{})
		if res.Band != bind.BandStructured {
			t.Errorf("第 %d 页分档 = %s，期望 structured", pageIdx+1, res.Band)
		}

		rev, report, err := animdefault.DefaultSpecReport(page, animdefault.DefaultOptions{
			SlideID: page.SlideID, Language: "zh-CN", DurationMS: 8000, FPS: 30,
			SegmentIDs: ids, Bindings: res.Bindings,
		})
		if err != nil {
			t.Fatalf("第 %d 页生成失败: %v", pageIdx+1, err)
		}

		// 非空跑：真实页上预填必须真的绑上东西。
		if report.Bound == 0 {
			t.Errorf("第 %d 页一条都没绑上", pageIdx+1)
		}
		if want := wantPrefillTracks[pageIdx]; report.Bound != want {
			t.Errorf("第 %d 页轨道数 = %d，基线是 %d（改了绑定器或预设就要重定这条基线并说明原因）",
				pageIdx+1, report.Bound, want)
		}

		// 每条绑定只出现一次；指向非可动画元素的绑定被丢弃，可动画的一条都不许丢。
		animatable := map[string]bool{}
		for _, a := range animdefault.Anchors(page) {
			animatable[a.ID] = true
		}
		wantBound := 0
		for _, b := range res.Bindings {
			if animatable[b.ElementID] {
				wantBound++
			}
		}
		if wantBound != report.Bound {
			t.Errorf("第 %d 页：可动画元素上的绑定 %d 条，轨道只有 %d 条（丢绑定了）", pageIdx+1, wantBound, report.Bound)
		}

		for _, tr := range rev.Tracks {
			for _, c := range tr.Clips {
				if !strings.HasPrefix(c.Trigger, animation.TriggerSegmentPrefix) {
					t.Errorf("第 %d 页 clip %s 触发器 = %q，预填只许写 withSegment", pageIdx+1, c.ID, c.Trigger)
					continue
				}
				seg := strings.TrimPrefix(c.Trigger, animation.TriggerSegmentPrefix)
				if !containsStr(ids, seg) {
					t.Errorf("第 %d 页 clip %s 指向不存在的分段 %q", pageIdx+1, c.ID, seg)
				}
				if c.TriggerSource == "" {
					t.Errorf("第 %d 页 clip %s 没有来源标注", pageIdx+1, c.ID)
				}
			}
		}
		totalBind += len(res.Bindings)
		totalTrack += report.Bound
	}
	t.Logf("五页合计：绑定 %d 条，产出轨道 %d 条（差值来自无动效模板的容器）", totalBind, totalTrack)
	if totalBind != 53 || totalTrack != 45 {
		t.Errorf("合计绑定 %d / 轨道 %d，基线 53 / 45", totalBind, totalTrack)
	}
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
