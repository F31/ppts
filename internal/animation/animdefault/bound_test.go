package animdefault

import (
	"strings"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/bind"
	"github.com/F31/ppts/internal/project"
)

// 本文件是「预填入口」的验收（changes/animation-narration-binding tasks 3.1–3.3）：
// 绑定说了算、没绑的保持静态、只写相对锚点、分段对不上就报错而不是猜。

func bindPage() *project.Page {
	box := func(y int64) *project.Box {
		return &project.Box{X: 800000, Y: y, Width: 8000000, Height: 700000}
	}
	return &project.Page{
		Index:   1,
		SlideID: "slide-2",
		Shapes: []*project.Shape{
			{ID: "b-title", Kind: "text", Text: "三条结论", Bounds: box(600000)},
			{ID: "b-1", Kind: "text", Text: "收入增长 18%", Bounds: box(2000000)},
			{ID: "b-2", Kind: "text", Text: "毛利率 42%", Bounds: box(2800000)},
			{ID: "b-3", Kind: "text", Text: "新增客户 36 家", Bounds: box(3600000)},
		},
	}
}

func bindOptions(bindings []bind.Binding) DefaultOptions {
	opt := defaultOptions(DefaultPresetID)
	opt.SlideID = "slide-2"
	opt.Bindings = bindings
	return opt
}

// triggerOf 返回某个元素轨道上第一条 clip 的触发器（没有该轨道时返回空串）。
func triggerOf(rev *animation.Revision, elementID string) (string, bool) {
	for _, tr := range rev.Tracks {
		if tr.ElementID == elementID && len(tr.Clips) > 0 {
			return tr.Clips[0].Trigger, true
		}
	}
	return "", false
}

func TestBoundTracksUseSegmentTriggers(t *testing.T) {
	rev, report, err := DefaultSpecReport(bindPage(), bindOptions([]bind.Binding{
		{ElementID: "b-title", SegmentID: "seg-2", Source: animation.TriggerSourcePrior},
		{ElementID: "b-1", SegmentID: "seg-1", Source: animation.TriggerSourceText, StaggerMS: 0},
		{ElementID: "b-2", SegmentID: "seg-1", Source: animation.TriggerSourceText, StaggerMS: 140},
	}))
	if err != nil {
		t.Fatalf("DefaultSpecReport: %v", err)
	}

	want := map[string]string{
		"b-title": "withSegment:seg-2",
		"b-1":     "withSegment:seg-1",
		"b-2":     "withSegment:seg-1",
	}
	for id, trig := range want {
		got, ok := triggerOf(rev, id)
		if !ok {
			t.Fatalf("元素 %s 没有轨道（绑定被丢了）", id)
		}
		if got != trig {
			t.Errorf("元素 %s 触发器 = %q，期望 %q", id, got, trig)
		}
	}
	// 没被绑定的元素必须保持静态：出轨道就等于替用户猜了一个出场时机。
	if _, ok := triggerOf(rev, "b-3"); ok {
		t.Error("未绑定的 b-3 不应该有轨道")
	}
	if report.Bound != 3 || report.Static != 1 {
		t.Errorf("report = %+v，期望 bound=3 static=1", report)
	}

	// tasks 3.2：只写相对锚点。绝对毫秒在改稿后即失效，一条都不许有。
	for _, tr := range rev.Tracks {
		for _, c := range tr.Clips {
			if strings.HasPrefix(c.Trigger, "atTime:") {
				t.Errorf("clip %s 用了绝对触发 %q", c.ID, c.Trigger)
			}
		}
	}
}

func TestBoundTracksCarrySourceAndStagger(t *testing.T) {
	rev, _, err := DefaultSpecReport(bindPage(), bindOptions([]bind.Binding{
		{ElementID: "b-1", SegmentID: "seg-1", Source: animation.TriggerSourceText, Score: 0.42, StaggerMS: 280},
	}))
	if err != nil {
		t.Fatalf("DefaultSpecReport: %v", err)
	}
	for _, tr := range rev.Tracks {
		if tr.ElementID != "b-1" {
			continue
		}
		c := tr.Clips[0]
		if c.TriggerSource != animation.TriggerSourceText {
			t.Errorf("triggerSource = %q，期望 text（来源必须透传，否则编辑器无法按来源复核）", c.TriggerSource)
		}
		if c.Confidence != 0.42 {
			t.Errorf("confidence = %v，期望 0.42（置信度是「先看最可疑」的唯一依据）", c.Confidence)
		}
		if c.StartMs != 280 {
			t.Errorf("startMs = %d，期望 280（同段错落按绑定器给的值）", c.StartMs)
		}
	}
}

// nil 与空切片的分野：nil = 没做预填（走既有顺序分配），非 nil 空 = 做了预填但一条没绑上。
func TestNilBindingsKeepSequencedBehaviour(t *testing.T) {
	seq, err := DefaultSpec(bindPage(), bindOptions(nil))
	if err != nil {
		t.Fatalf("DefaultSpec(nil bindings): %v", err)
	}
	// 既有行为：3 条要点按「第 N 条 → 第 N 段」，第 3 条退化为页首。
	if trig, _ := triggerOf(seq, "b-3"); trig != animation.TriggerEnter {
		t.Errorf("b-3 触发器 = %q，nil 绑定时应保持既有顺序分配（onEnter）", trig)
	}
	if len(seq.Tracks) != 4 {
		t.Errorf("tracks = %d，nil 绑定时应覆盖全部 4 个文字元素", len(seq.Tracks))
	}

	empty, report, err := DefaultSpecReport(bindPage(), bindOptions([]bind.Binding{}))
	if err != nil {
		t.Fatalf("DefaultSpecReport(empty bindings): %v", err)
	}
	if len(empty.Tracks) != 0 {
		t.Errorf("tracks = %d，空绑定应产出零轨道（不拿顺序分配或整页淡入兜底）", len(empty.Tracks))
	}
	if report.Bound != 0 || report.Static != 4 {
		t.Errorf("report = %+v，期望 bound=0 static=4", report)
	}
}

func TestBindingToUnknownSegmentFails(t *testing.T) {
	_, _, err := DefaultSpecReport(bindPage(), bindOptions([]bind.Binding{
		{ElementID: "b-1", SegmentID: "seg-9", Source: animation.TriggerSourceText},
	}))
	if err == nil {
		t.Fatal("分段不在清单里时应报错，而不是静默降级")
	}
	if !strings.Contains(err.Error(), "seg-9") {
		t.Errorf("错误信息没带上分段 id：%v", err)
	}
}

// 绑定指向的元素不参与动画（无包围盒/音视频）时必须被安静忽略：那是"这个元素不该动"，
// 不是"绑定错了"。
func TestBindingToNonAnimatableElementIgnored(t *testing.T) {
	page := bindPage()
	page.Shapes = append(page.Shapes, &project.Shape{ID: "b-audio", Kind: "audio", Bounds: &project.Box{Width: 100, Height: 100}})
	rev, report, err := DefaultSpecReport(page, bindOptions([]bind.Binding{
		{ElementID: "b-1", SegmentID: "seg-1", Source: animation.TriggerSourceText},
		{ElementID: "b-audio", SegmentID: "seg-1", Source: animation.TriggerSourceSpatial},
	}))
	if err != nil {
		t.Fatalf("DefaultSpecReport: %v", err)
	}
	if report.Bound != 1 {
		t.Errorf("bound = %d，期望 1（音视频不该被算成绑定成功）", report.Bound)
	}
	for _, tr := range rev.Tracks {
		if tr.ElementID == "b-audio" {
			t.Error("音视频元素不该出现在轨道里")
		}
	}
}

func TestBoundTracksAreDeterministic(t *testing.T) {
	bindings := []bind.Binding{
		{ElementID: "b-title", SegmentID: "seg-2", Source: animation.TriggerSourcePrior},
		{ElementID: "b-1", SegmentID: "seg-1", Source: animation.TriggerSourceText},
	}
	a, _, err := DefaultSpecReport(bindPage(), bindOptions(bindings))
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	b, _, err := DefaultSpecReport(bindPage(), bindOptions(bindings))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if specHash(t, a) != specHash(t, b) {
		t.Fatal("同一输入两次生成不一致")
	}
}

// 绑定模式产生的规格同样要能过一遍产品侧校验（V1–V12）：预填没有豁免权。
func TestBoundSpecPassesValidation(t *testing.T) {
	rev, _, err := DefaultSpecReport(bindPage(), bindOptions([]bind.Binding{
		{ElementID: "b-title", SegmentID: "seg-2", Source: animation.TriggerSourcePrior},
		{ElementID: "b-1", SegmentID: "seg-1", Source: animation.TriggerSourceText},
	}))
	if err != nil {
		t.Fatalf("DefaultSpecReport: %v", err)
	}
	pc := animation.PageContext{
		DurationMS: 6000, FPS: 30,
		Elements:   anchorsOf(pageElements(bindPage())),
		SegmentIDs: []string{"seg-1", "seg-2"},
	}
	if err := rev.Validate(pc); err != nil {
		t.Fatalf("绑定模式的规格不合法: %v", err)
	}
}
