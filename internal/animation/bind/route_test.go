package bind

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// 一页「版面有结构（三个编号块）、讲稿口语直讲」的页面：分档应为 script，
// 这正是唯一该走模型的档。
func scriptBandPage() ([]Element, []Segment) {
	elements := []Element{
		{ID: "t1", Kind: "title", Box: Box{X: 100, Y: 30, W: 900, H: 60}, Text: "方案结构"},
		{ID: "m1", Kind: "marker", Box: Box{X: 100, Y: 140, W: 50, H: 50}, Text: "01"},
		{ID: "m2", Kind: "marker", Box: Box{X: 100, Y: 320, W: 50, H: 50}, Text: "02"},
		{ID: "m3", Kind: "marker", Box: Box{X: 100, Y: 500, W: 50, H: 50}, Text: "03"},
		{ID: "b1", Kind: "bullet", Box: Box{X: 180, Y: 140, W: 600, H: 60}, Text: "背景数据说明"},
		{ID: "b2", Kind: "bullet", Box: Box{X: 180, Y: 320, W: 600, H: 60}, Text: "做法共三步"},
		{ID: "b3", Kind: "bullet", Box: Box{X: 180, Y: 500, W: 600, H: 60}, Text: "收益测算"},
		// 无字卡片底板：模型看不见它，规则的结构推断应该补上。
		{ID: "card1", Kind: "shape", Box: Box{X: 160, Y: 120, W: 640, H: 100}},
	}
	segments := []Segment{
		{ID: "s1", Text: "先说说背景"},
		{ID: "s2", Text: "再讲做法"},
		{ID: "s3", Text: "最后看收益"},
	}
	return elements, segments
}

type fixedCompleter struct {
	calls int
	fn    func(ctx context.Context) (ModelCompletion, error)
}

func (f *fixedCompleter) Complete(ctx context.Context, _ ModelPrompt) (ModelCompletion, error) {
	f.calls++
	return f.fn(ctx)
}

type mapCache struct {
	data   map[string]string
	gets   int
	puts   int
	putErr error
}

func newMapCache() *mapCache { return &mapCache{data: map[string]string{}} }

func (m *mapCache) Get(_ context.Context, key string) (string, bool, error) {
	m.gets++
	v, ok := m.data[key]
	return v, ok, nil
}

func (m *mapCache) Put(_ context.Context, key, text string) error {
	m.puts++
	if m.putErr != nil {
		return m.putErr
	}
	m.data[key] = text
	return nil
}

func bindingMap(bindings []Binding) map[string]Binding {
	out := make(map[string]Binding, len(bindings))
	for _, b := range bindings {
		out[b.ElementID] = b
	}
	return out
}

func TestRouteOfOnlyScriptUsesModel(t *testing.T) {
	cases := []struct {
		band   Band
		reason string
		want   Route
	}{
		{BandStructured, "", RouteRule},
		{BandLowConfidence, BandReasonScript, RouteModel},
		{BandLowConfidence, BandReasonLayout, RouteRule},
		{BandLowConfidence, BandReasonBoth, RouteRule},
		{BandLowConfidence, BandReasonNoScript, RouteRule},
	}
	for _, c := range cases {
		if got := RouteOf(c.band, c.reason); got != c.want {
			t.Errorf("band=%q reason=%q → %q，期望 %q", c.band, c.reason, got, c.want)
		}
	}
}

func TestRoutedScriptBandUsesModelAndFillsStructure(t *testing.T) {
	elements, segments := scriptBandPage()
	// 先确认分档确实是 script，否则这条用例测的不是模型通道。
	if base := Run(elements, segments, Options{}); base.Band != BandLowConfidence || base.BandReason != BandReasonScript {
		t.Fatalf("夹具分档 = %q/%q，期望 low-confidence/script", base.Band, base.BandReason)
	}

	fake := &fixedCompleter{fn: func(context.Context) (ModelCompletion, error) {
		return ModelCompletion{Text: `{"bindings":[
			{"elementId":"b1","segmentId":"s1","confidence":0.9},
			{"elementId":"b2","segmentId":"s2","confidence":0.8},
			{"elementId":"b3","segmentId":"s3","confidence":0.7}]}`}, nil
	}}
	out := RunRouted(context.Background(), "slide-1", elements, segments, RoutedOptions{Model: fake})

	if out.Route != RouteModel || !out.Model.Used {
		t.Fatalf("route=%q used=%v fallback=%q，期望走模型", out.Route, out.Model.Used, out.Model.Fallback)
	}
	if fake.calls != 1 {
		t.Fatalf("模型调用 %d 次，期望 1", fake.calls)
	}
	if out.Model.Accepted != 3 || out.Model.Prior != 1 || out.Model.Structural != 1 {
		t.Fatalf("accepted=%d prior=%d structural=%d，期望 3/1/1",
			out.Model.Accepted, out.Model.Prior, out.Model.Structural)
	}

	got := bindingMap(out.Bindings)
	for _, id := range []string{"b1", "b2", "b3"} {
		b, ok := got[id]
		if !ok {
			t.Fatalf("%s 没有绑上：%v", id, out.Bindings)
		}
		if b.Source != "model" {
			t.Errorf("%s 来源 = %q，期望 model", id, b.Source)
		}
	}
	if got["b1"].Confidence != 0.9 {
		t.Errorf("b1 置信度 = %v，期望 0.9（模型自报，不是字面相似度）", got["b1"].Confidence)
	}
	if got["t1"].Source != "prior" {
		t.Errorf("标题应被标题先验补上，实际 %q", got["t1"].Source)
	}
	if got["card1"].Source != "structural" {
		t.Errorf("卡片底板应被结构推断补上，实际 %q", got["card1"].Source)
	}
	if len(out.BySource["model"]) != 3 || len(out.BySource["structural"]) != 1 {
		t.Errorf("BySource 没按来源分开：%v", out.BySource)
	}
	if out.Bindings[0].ElementID == "" {
		t.Fatal("绑定顺序没有按元素排")
	}
}

// 路由到模型时整页换掉规则结果，不做逐元素合并。
// 实测模型会无视「规则已绑」这条约束（越界 49/30 条），合并只会带来看不见的错绑。
func TestRoutedModelReplacesRuleBindings(t *testing.T) {
	elements, segments := scriptBandPage()
	base := Run(elements, segments, Options{})
	if _, ok := bindingMap(base.Bindings)["b2"]; !ok {
		t.Skip("这版规则没有绑上 b2，用例前提不成立")
	}

	fake := &fixedCompleter{fn: func(context.Context) (ModelCompletion, error) {
		return ModelCompletion{Text: `{"bindings":[{"elementId":"b1","segmentId":"s1","confidence":0.9}]}`}, nil
	}}
	out := RunRouted(context.Background(), "slide-1", elements, segments, RoutedOptions{Model: fake})
	got := bindingMap(out.Bindings)
	if _, ok := got["b2"]; ok {
		t.Fatalf("模型没绑 b2，结果里却还有它（规则结果被合并进来了）：%v", out.Bindings)
	}
	if _, ok := got["b1"]; !ok {
		t.Fatalf("模型绑的 b1 丢了：%v", out.Bindings)
	}
}

func TestRoutedStructuredBandNeverCallsModel(t *testing.T) {
	elements, segments := scriptBandPage()
	// 同一页，把讲稿换成逐条讲——分档变结构化，规则更准，不该再问模型。
	segments = []Segment{
		{ID: "s1", Text: "第一步讲背景数据"},
		{ID: "s2", Text: "第二步讲做法"},
		{ID: "s3", Text: "第三步看收益"},
	}
	if base := Run(elements, segments, Options{}); base.Band != BandStructured {
		t.Fatalf("夹具分档 = %q，期望 structured", base.Band)
	}
	fake := &fixedCompleter{fn: func(context.Context) (ModelCompletion, error) {
		t.Fatal("结构化页不该调用模型")
		return ModelCompletion{}, nil
	}}
	out := RunRouted(context.Background(), "slide-1", elements, segments, RoutedOptions{Model: fake})
	if out.Route != RouteRule || fake.calls != 0 {
		t.Fatalf("route=%q calls=%d，期望 rule/0", out.Route, fake.calls)
	}
}

func TestRoutedLayoutBandFallsBackToRules(t *testing.T) {
	// 版面本身没有结构：三个要点散排，没有编号也没有卡片。
	elements := []Element{
		{ID: "b1", Kind: "bullet", Box: Box{X: 100, Y: 200, W: 400, H: 60}, Text: "背景数据说明"},
		{ID: "b2", Kind: "bullet", Box: Box{X: 700, Y: 500, W: 400, H: 60}, Text: "做法共三步"},
	}
	segments := []Segment{{ID: "s1", Text: "第一步讲背景"}, {ID: "s2", Text: "第二步讲做法"}}
	if base := Run(elements, segments, Options{}); base.BandReason != BandReasonLayout {
		t.Fatalf("夹具分档原因 = %q，期望 layout", base.BandReason)
	}
	fake := &fixedCompleter{fn: func(context.Context) (ModelCompletion, error) {
		t.Fatal("layout 档不该调用模型：0.353 换 0.533，还多花十几秒")
		return ModelCompletion{}, nil
	}}
	out := RunRouted(context.Background(), "slide-2", elements, segments, RoutedOptions{Model: fake})
	if out.Route != RouteRule || fake.calls != 0 {
		t.Fatalf("route=%q calls=%d，期望 rule/0", out.Route, fake.calls)
	}
}

func TestRoutedFallsBackWhenModelReturnsGarbage(t *testing.T) {
	elements, segments := scriptBandPage()
	base := Run(elements, segments, Options{})

	cases := []struct {
		name string
		text string
		want string
	}{
		{"不是 JSON", "好的，我来帮你绑定：", ModelFallbackInvalidJSON},
		{"JSON 但一条都不该收", `{"bindings":[{"elementId":"nope","segmentId":"s1"}]}`, ModelFallbackRejected},
		{"空绑定", `{"bindings":[]}`, ModelFallbackRejected},
	}
	for _, c := range cases {
		fake := &fixedCompleter{fn: func(context.Context) (ModelCompletion, error) {
			return ModelCompletion{Text: c.text}, nil
		}}
		out := RunRouted(context.Background(), "slide-1", elements, segments, RoutedOptions{Model: fake})
		if out.Model.Fallback != c.want {
			t.Errorf("%s：fallback = %q，期望 %q", c.name, out.Model.Fallback, c.want)
		}
		if out.Route != RouteRule {
			t.Errorf("%s：route = %q，期望 rule（退回规则）", c.name, out.Route)
		}
		if len(out.Bindings) != len(base.Bindings) {
			t.Errorf("%s：绑定数 = %d，期望退回规则的 %d",
				c.name, len(out.Bindings), len(base.Bindings))
		}
	}
}

func TestRoutedFallsBackOnTimeoutAndError(t *testing.T) {
	elements, segments := scriptBandPage()

	t.Run("超时", func(t *testing.T) {
		fake := &fixedCompleter{fn: func(ctx context.Context) (ModelCompletion, error) {
			<-ctx.Done()
			return ModelCompletion{}, ctx.Err()
		}}
		out := RunRouted(context.Background(), "slide-1", elements, segments, RoutedOptions{
			Model:   fake,
			Timeout: 10 * time.Millisecond,
		})
		if out.Model.Fallback != ModelFallbackTimeout {
			t.Fatalf("fallback = %q，期望 timeout", out.Model.Fallback)
		}
		if out.Route != RouteRule {
			t.Fatalf("route = %q，期望 rule", out.Route)
		}
	})

	t.Run("网关错误", func(t *testing.T) {
		fake := &fixedCompleter{fn: func(context.Context) (ModelCompletion, error) {
			return ModelCompletion{}, errors.New("502 bad gateway")
		}}
		out := RunRouted(context.Background(), "slide-1", elements, segments, RoutedOptions{Model: fake})
		if out.Model.Fallback != ModelFallbackError {
			t.Fatalf("fallback = %q，期望 error", out.Model.Fallback)
		}
	})
}

func TestRoutedWithoutModelKeepsRules(t *testing.T) {
	elements, segments := scriptBandPage()
	base := Run(elements, segments, Options{})
	out := RunRouted(context.Background(), "slide-1", elements, segments, RoutedOptions{})
	if out.Model.Fallback != ModelFallbackNoModel {
		t.Fatalf("fallback = %q，期望 no_model", out.Model.Fallback)
	}
	if len(out.Bindings) != len(base.Bindings) {
		t.Fatalf("没配模型时结果应与纯规则一致：%d vs %d", len(out.Bindings), len(base.Bindings))
	}
}

// 缓存是确定性的一部分：同一输入第二次必须回放同一段文本，且不再发请求。
func TestRoutedCacheAvoidsSecondCall(t *testing.T) {
	elements, segments := scriptBandPage()
	fake := &fixedCompleter{fn: func(context.Context) (ModelCompletion, error) {
		return ModelCompletion{Text: `{"bindings":[{"elementId":"b1","segmentId":"s1","confidence":0.9}]}`}, nil
	}}
	cache := newMapCache()
	opt := RoutedOptions{Model: fake, Cache: cache, ModelID: "fake-1"}

	first := RunRouted(context.Background(), "slide-1", elements, segments, opt)
	if first.Model.CacheHit {
		t.Fatal("第一次不该命中缓存")
	}
	if cache.puts != 1 {
		t.Fatalf("第一次应写一次缓存，实际 %d", cache.puts)
	}

	second := RunRouted(context.Background(), "slide-1", elements, segments, opt)
	if fake.calls != 1 {
		t.Fatalf("第二次又发了请求（调用 %d 次）", fake.calls)
	}
	if !second.Model.CacheHit {
		t.Fatal("第二次应命中缓存")
	}
	if second.Model.LatencyMS != 0 {
		t.Fatalf("命中缓存时延迟 = %dms，期望 0：延迟必须反映真实调用", second.Model.LatencyMS)
	}
	if len(second.Bindings) != len(first.Bindings) {
		t.Fatalf("回放结果与首次不一致：%d vs %d", len(second.Bindings), len(first.Bindings))
	}
}

// 缓存写失败不影响这一次的结果：它只让下一次重跑不稳定，不该把页面变成错误。
func TestRoutedCacheWriteFailureIsNotFatal(t *testing.T) {
	elements, segments := scriptBandPage()
	fake := &fixedCompleter{fn: func(context.Context) (ModelCompletion, error) {
		return ModelCompletion{Text: `{"bindings":[{"elementId":"b1","segmentId":"s1"}]}`}, nil
	}}
	cache := newMapCache()
	cache.putErr = errors.New("disk full")
	out := RunRouted(context.Background(), "slide-1", elements, segments, RoutedOptions{
		Model: fake, Cache: cache, ModelID: "fake-1",
	})
	if !out.Model.Used {
		t.Fatalf("缓存写失败把整页退回了规则：%q", out.Model.Fallback)
	}
}

// 没有 ModelID 时不读也不写缓存：空模型名会让换模型之后仍命中旧缓存。
func TestRoutedNoModelIDSkipsCache(t *testing.T) {
	elements, segments := scriptBandPage()
	fake := &fixedCompleter{fn: func(context.Context) (ModelCompletion, error) {
		return ModelCompletion{Text: `{"bindings":[{"elementId":"b1","segmentId":"s1"}]}`}, nil
	}}
	cache := newMapCache()
	opt := RoutedOptions{Model: fake, Cache: cache}
	RunRouted(context.Background(), "slide-1", elements, segments, opt)
	RunRouted(context.Background(), "slide-1", elements, segments, opt)
	if cache.gets != 0 || cache.puts != 0 {
		t.Fatalf("没有 ModelID 却动了缓存（get=%d put=%d）", cache.gets, cache.puts)
	}
	if fake.calls != 2 {
		t.Fatalf("调用 %d 次，期望 2（没有缓存可命中）", fake.calls)
	}
}

// 与离线脚本的结构收尾对账：同一份页、同一份「完美模型输出」（金标里有文字的元素），
// Python bind_llm.fill_structure 得到 prior=0 / structural=4 / 共 18 条；
// Go 侧必须一模一样。两边各写一份补容器的逻辑，差一条都会让「模型召回 0.944/0.972」
// 这些数字失去意义。
func TestModelStructFillMatchesPythonHarness(t *testing.T) {
	doc := loadFixture(t)
	page := doc.Pages[4]

	elements := make([]Element, len(page.Elements))
	for i, e := range page.Elements {
		elements[i] = Element{
			ID: e.ID, Kind: e.Kind, Text: e.Text,
			Box: Box{X: e.Box[0], Y: e.Box[1], W: e.Box[2], H: e.Box[3]},
		}
	}
	segments := make([]Segment, len(page.Segments))
	for i, s := range page.Segments {
		segments[i] = Segment{ID: s.ID, Text: s.Text}
	}

	type item struct {
		ElementID string  `json:"elementId"`
		SegmentID string  `json:"segmentId"`
		Conf      float64 `json:"confidence"`
	}
	var items []item
	for _, g := range page.Golden {
		for _, e := range elements {
			if e.ID == g.ElementID && strings.TrimSpace(e.Text) != "" {
				items = append(items, item{g.ElementID, g.SegmentID, 0.8})
			}
		}
	}
	raw, err := json.Marshal(map[string]any{"bindings": items})
	if err != nil {
		t.Fatal(err)
	}

	out := finishModelRoute(RoutedResult{Result: Run(elements, segments, Options{})},
		string(raw), readingOrder(elements), segments, RoutedOptions{})

	if out.Model.Accepted != 14 {
		t.Fatalf("接受 %d 条，Python 侧是 14 条", out.Model.Accepted)
	}
	if out.Model.Prior != 0 || out.Model.Structural != 4 {
		t.Fatalf("收尾 prior=%d structural=%d，Python 侧是 0 / 4", out.Model.Prior, out.Model.Structural)
	}
	if len(out.Bindings) != 18 {
		t.Fatalf("最终绑定 %d 条，Python 侧是 18 条", len(out.Bindings))
	}
	filled := map[string]bool{}
	for _, id := range out.BySource["structural"] {
		filled[id] = true
	}
	for _, want := range []string{"p5-s5", "p5-s10", "p5-s15", "p5-s20"} {
		if !filled[want] {
			t.Errorf("结构收尾没补上 %s（Python 侧补了这四个底板）：%v", want, out.BySource["structural"])
		}
	}
}
