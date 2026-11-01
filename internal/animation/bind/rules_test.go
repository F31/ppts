package bind

import (
	"reflect"
	"testing"
)

func elem(id, kind string, x, y, w, h float64, text string) Element {
	return Element{ID: id, Kind: kind, Box: Box{X: x, Y: y, W: w, H: h}, Text: text}
}

func bindingsByElement(r Result) map[string]Binding {
	out := make(map[string]Binding, len(r.Bindings))
	for _, b := range r.Bindings {
		out[b.ElementID] = b
	}
	return out
}

func TestNormalizeAndCn2Num(t *testing.T) {
	nums := []struct{ in, want string }{
		{"一百七十四", "174"}, // 两位以上带单位 → 改写
		{"三十", "30"},     // 同上
		{"一致", "一致"},     // 单字不动
		{"一部分", "一部分"},   // 单字不动
		{"三点五", "3.5"},   // 小数
	}
	for _, c := range nums {
		if got := cn2num(c.in); got != c.want {
			t.Errorf("cn2num(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
	norms := []struct{ in, want string }{
		{"ＡＢＣ　12", "abc12"}, // 全角转半角 + 去空白
		{"174s", "174s"},    // 数字与字母保留
	}
	for _, c := range norms {
		if got := normalize(c.in); got != c.want {
			t.Errorf("normalize(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestShortTextLCSUsesExactEquality(t *testing.T) {
	// 「01」对「02」：段里出现任意一个 0，编号就"像"了。极短文本必须只认完全相等。
	if _, d := similarity("编号 01", "02", "marker"); d.LCS != 0 {
		t.Fatalf("极短文本 LCS 覆盖率 = %v，期望 0（不能靠单个字顶上去）", d.LCS)
	}
	if _, d := similarity("01", "01", "marker"); d.LCS != 1 {
		t.Fatalf("完全相等时 LCS 覆盖率 = %v，期望 1", d.LCS)
	}
}

func TestTitleBoost(t *testing.T) {
	seg := "今天我们讲架构要点"
	plain, _ := similarity(seg, "架构要点", "bullet")
	title, _ := similarity(seg, "架构要点", "title")
	if title <= plain {
		t.Fatalf("title 加成失效：title=%v bullet=%v", title, plain)
	}
}

func TestQuantitySkipsOrdinal(t *testing.T) {
	// 「第二条」是序数，不是「两条」。不排除的话，逐条讲的页面会在每条上各触发一次
	// 计数规则，把整页编号块反复绑到最后一段。
	if n, ok := detectCount("第二条，每个关键事实都留痕"); ok {
		t.Fatalf("「第二条」被当成数量概括 N=%d", n)
	}
	if n, ok := detectCount("今天分五个部分，从市场讲到合规"); !ok || n != 5 {
		t.Fatalf("「五个部分」没有解析成 5（got %d ok=%v）", n, ok)
	}
	if _, ok := detectCount("只有一个想法"); ok {
		t.Fatal("N=1 不应算列表概括")
	}
}

func TestOrdinalBindsNthBlock(t *testing.T) {
	elements := []Element{
		elem("m1", "marker", 50, 100, 80, 80, "01"),
		elem("b1", "bullet", 150, 100, 300, 60, "双通道理解"),
		elem("m2", "marker", 50, 300, 80, 80, "02"),
		elem("b2", "bullet", 150, 300, 300, 60, "事实块留痕"),
		elem("m3", "marker", 50, 500, 80, 80, "03"),
		elem("b3", "bullet", 150, 500, 300, 60, "确定性校验"),
	}
	// 段文本刻意与块内文字无字面重合：序数规则的用途正是「字面为 0 时靠序号对应」。
	segments := []Segment{{ID: "s0", Text: "第二条，主要看它能不能追到源头。"}}
	res := Run(elements, segments, Options{})
	got := bindingsByElement(res)
	for _, id := range []string{"m2", "b2"} {
		b, ok := got[id]
		if !ok {
			t.Fatalf("第 2 块的 %s 没被绑上：%+v", id, res.Bindings)
		}
		if b.SegmentID != "s0" || b.Source != "ordinal" {
			t.Fatalf("%s 绑定来源/段落不对：%+v", id, b)
		}
	}
	for _, id := range []string{"m1", "b1", "m3", "b3"} {
		if _, ok := got[id]; ok {
			t.Fatalf("%s 不该被「第二条」带出", id)
		}
	}
}

func TestCountBindsMarkerGroup(t *testing.T) {
	var elements []Element
	labels := []string{"市场趋势", "产品定位", "核心功能", "技术架构", "合规路线"}
	for i, label := range labels {
		y := 120.0 + float64(i)*140
		elements = append(elements,
			elem("m"+itoa(i), "marker", 100, y, 80, 80, "0"+itoa(i+1)),
			elem("b"+itoa(i), "bullet", 200, y, 600, 60, label),
		)
	}
	// 段文本与五个标签保持字面无关：计数规则的用途是「概括 + 编号组」的对应，
	// 若字面先命中，测试就变成在验字面匹配了。
	segments := []Segment{{ID: "s0", Text: "今天分五个部分，我按顺序过一遍。"}}
	res := Run(elements, segments, Options{})
	if len(res.Bindings) != 10 {
		t.Fatalf("绑了 %d 件，期望 10（5 组 × 2 件）：%+v", len(res.Bindings), res.Bindings)
	}
	for _, b := range res.Bindings {
		if b.Source != "count" {
			t.Fatalf("%s 来源 = %q，期望 count", b.ElementID, b.Source)
		}
	}
}

func TestSpatialPicksSingleCandidate(t *testing.T) {
	elements := []Element{
		elem("img-left", "image", 100, 300, 400, 400, ""),
		elem("img-right", "image", 1200, 300, 400, 400, ""),
		elem("cap", "bullet", 900, 100, 300, 50, "部署形态说明"),
	}
	segments := []Segment{{ID: "s0", Text: "右边那张图展示了部署形态。"}}
	got := bindingsByElement(Run(elements, segments, Options{}))
	if _, ok := got["img-right"]; !ok {
		t.Fatalf("右边那张图没绑上：%+v", got)
	}
	if _, ok := got["img-left"]; ok {
		t.Fatal("左边那张图被误绑")
	}
}

func TestSpatialGivesUpOnAmbiguity(t *testing.T) {
	// 两个都在右边就放弃——宁可漏，不可错。
	elements := []Element{
		elem("a", "image", 1100, 100, 300, 300, ""),
		elem("b", "image", 1100, 500, 300, 300, ""),
	}
	got := bindingsByElement(Run(elements, []Segment{{ID: "s0", Text: "右边的图"}}, Options{}))
	if len(got) != 0 {
		t.Fatalf("候选不唯一时仍绑了：%+v", got)
	}
}

func TestAuxIsNeverBound(t *testing.T) {
	elements := []Element{
		elem("body", "bullet", 100, 100, 600, 60, "四步流程说明"),
		elem("cap", "note", 100, 800, 600, 40, "图：四步流程"),
		elem("pn", "page_number", 1800, 1015, 86, 43, "02"),
	}
	got := bindingsByElement(Run(elements, []Segment{{ID: "s0", Text: "四步流程"}}, Options{}))
	if _, ok := got["cap"]; ok {
		t.Fatal("图注被绑上（错绑，用户看不出来）")
	}
	if _, ok := got["pn"]; ok {
		t.Fatal("页码被绑上")
	}
	if _, ok := got["body"]; !ok {
		t.Fatal("正文没绑上")
	}
}

func TestSiblingCarriesCardContent(t *testing.T) {
	// 四步流程里的典型一列：卡片底 / 图标 / 标题 / 注释。字面只够绑标题，
	// 其余三件靠「同块带出」跟进来，否则这一列会只亮一半。
	// 注释那一件离标题太远（gap > 60），按「上下紧邻」的判据本来就不该被带出——
	// 这里只断言真正同块的三件，不把「漏」当「错」。
	elements := []Element{
		elem("title", "bullet", 0, 0, 600, 60, "上传文件"),
		elem("card", "shape", 0, 70, 300, 300, ""),
		elem("icon", "image", 10, 80, 80, 80, ""),
		elem("caption", "bullet", 0, 390, 300, 60, "自动识别页面、备注与异常提示"),
	}
	got := bindingsByElement(Run(elements, []Segment{{ID: "s0", Text: "第一步，把文件拖进来。"}}, Options{}))
	for _, id := range []string{"title", "card", "icon"} {
		if _, ok := got[id]; !ok {
			t.Fatalf("%s 没跟着进（同块带出失效）：%+v", id, got)
		}
	}
	if b := got["title"]; b.Source != "text" {
		t.Fatalf("标题应来自字面匹配，实际 %q", b.Source)
	}
}

func TestTitlePriorWhenTextMisses(t *testing.T) {
	// 标题的措辞经常是概括而不是重复元素文字，纯字面匹配一定漏。
	elements := []Element{
		elem("t", "title", 100, 100, 600, 60, "架构要点"),
		elem("b", "bullet", 100, 200, 600, 60, ""),
	}
	segments := []Segment{{ID: "s0", Text: "五段流水线，每段都有独立的重试"}}
	got := bindingsByElement(Run(elements, segments, Options{}))
	b, ok := got["t"]
	if !ok {
		t.Fatalf("标题没被先验兜住：%+v", got)
	}
	if b.SegmentID != "s0" || b.Source != "prior" {
		t.Fatalf("标题先验落点不对：%+v", b)
	}
}

func TestBand(t *testing.T) {
	markerPage := []Element{
		elem("m1", "marker", 100, 120, 80, 80, "01"),
		elem("m2", "marker", 100, 260, 80, 80, "02"),
		elem("m3", "marker", 100, 400, 80, 80, "03"),
		elem("b1", "bullet", 200, 120, 600, 60, "市场趋势"),
	}
	// 讲稿把结构念了出来：结构化需要「版面有结构」+「讲稿讲了结构」两件。
	scripted := []Segment{{ID: "s0", Text: "今天分三个部分，第一部分讲市场"}}
	v := classifyBand(markerPage, scripted)
	if v.Band != BandStructured {
		t.Fatalf("有 3 个编号标记且讲稿含序数，却判成 %q（原因 %q）", v.Band, v.Reason)
	}
	if v.Reason != "" {
		t.Fatalf("结构化不该带低置信原因，实际 %q", v.Reason)
	}
	if !hasSignal(v.Signals, SignalMarkers) || !hasSignal(v.Signals, SignalScriptStructure) {
		t.Fatalf("信号不对：%v", v.Signals)
	}

	// 版面有结构、讲稿口语直讲：实测召回 0.667（结构化是 0.972）。
	// 这一格以前判 structured，低置信提示恰好在真实的高风险组合上沉默。
	flat := []Segment{{ID: "s0", Text: "这套东西就是把一份幻灯片讲成能播的片子"}}
	v = classifyBand(markerPage, flat)
	if v.Band != BandLowConfidence || v.Reason != BandReasonScript {
		t.Fatalf("版面有结构但讲稿没讲出来，应 low-confidence/script，实际 %q/%q", v.Band, v.Reason)
	}
	if !hasSignal(v.Signals, SignalScriptFlat) {
		t.Fatalf("讲稿无结构词时应出 script-flat：%v", v.Signals)
	}

	// 单个居中文本框：没有编号、没有卡片簇。方向覆盖仍然是 2（左 + 上），
	// 但它不编码讲解顺序，不能凭它判结构化——否则分档等于没有。
	plainPage := []Element{elem("p", "bullet", 100, 100, 600, 60, "一段话")}
	v = classifyBand(plainPage, scripted)
	if v.Band != BandLowConfidence {
		t.Fatalf("纯文字页被判成 %q（方向覆盖 %d，信号 %v）", v.Band, v.Directions, v.Signals)
	}
	if v.Reason != BandReasonLayout {
		t.Fatalf("版面缺信号、讲稿有结构词，原因应为 layout，实际 %q", v.Reason)
	}

	// 两侧都缺：原因必须合并成 both，不能报成 layout 或 script 之一。
	v = classifyBand(plainPage, flat)
	if v.Band != BandLowConfidence || v.Reason != BandReasonBoth {
		t.Fatalf("两侧都缺应 low-confidence/both，实际 %q/%q", v.Band, v.Reason)
	}
}

// 没有讲稿与「讲稿没讲出结构」是两件事：一个要写稿，一个要改稿。
// 真实客户 deck 上这一格是常态（33 页 Token 方案有 27 页没有备注），
// 混成 script-flat 会让编辑器对着空讲稿说「先去改讲解词」。
func TestBandNoScript(t *testing.T) {
	markerPage := []Element{
		elem("m1", "marker", 100, 120, 80, 80, "01"),
		elem("m2", "marker", 100, 260, 80, 80, "02"),
		elem("m3", "marker", 100, 400, 80, 80, "03"),
		elem("b1", "bullet", 200, 120, 600, 60, "市场趋势"),
	}

	// 版面有编号，但一个分段都没有：不能判结构化，也不能说「讲稿没念出结构」。
	v := classifyBand(markerPage, nil)
	if v.Band != BandLowConfidence || v.Reason != BandReasonNoScript {
		t.Fatalf("无讲稿应 low-confidence/no-script，实际 %q/%q", v.Band, v.Reason)
	}
	if !hasSignal(v.Signals, SignalScriptAbsent) {
		t.Fatalf("无讲稿应出 script-absent：%v", v.Signals)
	}
	if hasSignal(v.Signals, SignalScriptFlat) {
		t.Fatalf("无讲稿不该同时出 script-flat（两个信号互斥）：%v", v.Signals)
	}

	// 分段存在但全是空白：同样算「没有可念的文字」。空段落不是讲稿。
	blank := []Segment{{ID: "s0", Text: "  "}, {ID: "s1", Text: "\n"}}
	v = classifyBand(markerPage, blank)
	if v.Reason != BandReasonNoScript {
		t.Fatalf("全空段落也应按 no-script，实际 %q", v.Reason)
	}

	// 只要有一句可念的字，就回到 script-flat：能绑不能绑是绑定器的事，
	// 分档只管「有没有文字」。
	v = classifyBand(markerPage, []Segment{{ID: "s0", Text: "谢谢"}})
	if v.Reason != BandReasonScript {
		t.Fatalf("有一句讲稿应回到 script，实际 %q", v.Reason)
	}
}

func TestScriptStructureKinds(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"序数", "第二部分讲落地", []string{ScriptOrdinal}},
		{"精确数量", "今天分五个部分", []string{ScriptCount}},
		{"空间指代", "右边那张图是关键", []string{ScriptSpatial}},
		{"图表指代", "这张表把几家摆在一起", []string{ScriptSpatial}},
		{"中间", "中间的流程是主线", []string{ScriptSpatial}},
		// 序数形态不能被当成「两条」：`第` 开头的那一处结构只记一次。
		{"第二条只算序数", "第二条是合规", []string{ScriptOrdinal}},
		// 模糊数量不算信号：与规则同口径（消融里「几个」与整句不说同分）。
		{"几个不算", "今天分几个部分", nil},
		// 「一个X」不编码结构（版面不会有 1 项的列表），与 detectCount 的 2..12 同口径。
		{"一个点不算", "毛利率比上季度高一个点", nil},
		{"无关文本", "这套东西就是把幻灯片讲成片子", nil},
	}
	for _, c := range cases {
		got := scriptStructureKinds([]Segment{{ID: "s", Text: c.text}})
		if len(got) != len(c.want) {
			t.Fatalf("%s: 命中 %v，期望 %v", c.name, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s: 命中 %v，期望 %v", c.name, got, c.want)
			}
		}
	}
}

func TestDeterministic(t *testing.T) {
	doc := loadFixture(t)
	for _, page := range doc.Pages {
		elements := make([]Element, len(page.Elements))
		for i, e := range page.Elements {
			elements[i] = Element{ID: e.ID, Kind: e.Kind, Text: e.Text,
				Box: Box{X: e.Box[0], Y: e.Box[1], W: e.Box[2], H: e.Box[3]}}
		}
		segments := make([]Segment, len(page.Segments))
		for i, s := range page.Segments {
			segments[i] = Segment{ID: s.ID, Text: s.Text}
		}
		first := Run(elements, segments, Options{})
		for i := 0; i < 5; i++ {
			if next := Run(elements, segments, Options{}); !reflect.DeepEqual(first, next) {
				t.Fatalf("page %d 第 %d 次重跑结果不同（预填必须是纯函数）", page.Index, i+2)
			}
		}
	}
}

func hasSignal(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
