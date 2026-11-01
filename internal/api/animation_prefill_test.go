package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/F31/ppts/internal/animation"
	"github.com/F31/ppts/internal/animation/animdefault"
	"github.com/F31/ppts/internal/animation/animstore"
	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/project"
)

// 本文件是「预填入口」的 HTTP 侧验收（changes/animation-narration-binding tasks 3.1–3.3）：
// 显式开关、人工结果不被覆盖、没有分段就说明原因而不是猜。

func prefillPage() *project.Page {
	box := func(y int64) *project.Box {
		return &project.Box{X: 800000, Y: y, Width: 8000000, Height: 700000}
	}
	return &project.Page{
		Index:   2,
		SlideID: anTestSlide,
		Shapes: []*project.Shape{
			{ID: "shape-title", Kind: "text", Text: "第三季度回顾", Bounds: &project.Box{X: 800000, Y: 600000, Width: 8000000, Height: 900000}},
			{ID: "shape-b1", Kind: "text", Text: "收入增长 18%", Bounds: box(2400000)},
			{ID: "shape-b2", Kind: "text", Text: "毛利率 42%", Bounds: box(3200000)},
		},
	}
}

// prefillSegments 是两段带明确字面重合的讲稿：预填应当把 b1 绑到 seg-1、b2 绑到 seg-2。
func prefillSegments() []animation.ScriptSegment {
	return []animation.ScriptSegment{
		{ID: "seg-1", Text: "先看整体：收入增长 18%，这是本季度的主旋律。"},
		{ID: "seg-2", Text: "毛利率 42%，比上季度高一个点，说明定价站得住。"},
	}
}

func newPrefillServer(t *testing.T, pc animation.PageContext) (*httptest.Server, animstore.Store, *project.Page) {
	t.Helper()
	store := newAnimationTestStore(t)
	page := prefillPage()
	pc.Elements = animdefault.PageAnchors(page)
	resolver := &fakeAnimationResolver{page: page, pc: pc}
	mux := http.NewServeMux()
	auth := func(h http.Handler) http.Handler {
		return AuthMiddlewareWithOptions(h, AuthOptions{
			LocalPrincipal: &Principal{TenantID: db.LocalTenantID, UserID: db.LocalUserID},
		})
	}
	NewAnimationHandler(store, resolver, true).Register(mux, auth)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, store, page
}

func prefillPC(segs ...animation.ScriptSegment) animation.PageContext {
	pc := animation.PageContext{DurationMS: 6000, FPS: 30, Segments: segs}
	for _, s := range segs {
		pc.SegmentIDs = append(pc.SegmentIDs, s.ID)
	}
	return pc
}

// triggersOf 把响应里的 tracks 摊平成 elementID → trigger。
func triggersOf(t *testing.T, payload map[string]any) map[string]string {
	t.Helper()
	out := map[string]string{}
	tracks, _ := payload["tracks"].([]any)
	for _, raw := range tracks {
		tr, _ := raw.(map[string]any)
		id, _ := tr["elementId"].(string)
		clips, _ := tr["clips"].([]any)
		if len(clips) == 0 {
			continue
		}
		c, _ := clips[0].(map[string]any)
		trig, _ := c["trigger"].(string)
		out[id] = trig
		if src, _ := c["triggerSource"].(string); src != "" {
			out[id+"#source"] = src
		}
	}
	return out
}

func TestDefaultSpecPrefillBindsSegments(t *testing.T) {
	ts, _, _ := newPrefillServer(t, prefillPC(prefillSegments()...))

	status, payload := doJSON(t, http.MethodGet, ts.URL+animationURL()+"?default=true&prefill=true", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, payload)
	}

	info, _ := payload["prefill"].(map[string]any)
	if info == nil || info["applied"] != true {
		t.Fatalf("prefill = %v，期望 applied=true", payload["prefill"])
	}
	if bound := info["bound"].(float64); bound == 0 {
		t.Errorf("bound = %v，期望至少绑上一条（预填没生效）", info["bound"])
	}
	if band, _ := info["band"].(string); band == "" {
		t.Error("band 为空：分档是低置信标注的前提，不能省略")
	}

	got := triggersOf(t, payload)
	if trig := got["shape-b1"]; trig != "withSegment:seg-1" {
		t.Errorf("shape-b1 触发器 = %q，期望 withSegment:seg-1（字面与第一段重合）", trig)
	}
	if trig := got["shape-b2"]; trig != "withSegment:seg-2" {
		t.Errorf("shape-b2 触发器 = %q，期望 withSegment:seg-2（字面与第二段重合）", trig)
	}
	// tasks 3.2：预填只写相对锚点。
	for tr := range got {
		if strings.HasPrefix(tr, "atTime:") {
			t.Errorf("出现绝对触发 %q", tr)
		}
	}
	// 预填的 clip 必须带来源，否则编辑器无法按来源复核（tasks 4.1 的前提）。
	if src := got["shape-b1#source"]; src == "" {
		t.Error("shape-b1 的 clip 没有 triggerSource")
	}
}

// 分档要读讲稿：同一份版面，讲稿把结构念出来才判结构化。
//
// 这一格以前只看版面，结果「版面有编号/卡片、讲稿口语直讲」被判 structured 且不给低置信
// 提示——而实测召回只有 0.667（结构化是 0.972）。见 changes/animation-prefill-script-signal。
func TestPrefillBandReadsNarration(t *testing.T) {
	cases := []struct {
		name       string
		segs       []animation.ScriptSegment
		wantBand   string
		wantReason string
		wantSignal string
	}{
		{
			name: "讲稿讲了结构",
			segs: []animation.ScriptSegment{
				{ID: "seg-1", Text: "第一部分看整体：收入增长 18%。"},
				{ID: "seg-2", Text: "第二部分看毛利率，42%，比上季度高一个点。"},
			},
			wantBand:   "structured",
			wantReason: "",
			wantSignal: "script-structure",
		},
		{
			name:       "讲稿口语直讲，没把结构念出来",
			segs:       prefillSegments(),
			wantBand:   "low-confidence",
			wantReason: "script",
			wantSignal: "script-flat",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts, _, _ := newPrefillServer(t, prefillPC(c.segs...))
			status, payload := doJSON(t, http.MethodGet, ts.URL+animationURL()+"?default=true&prefill=true", nil)
			if status != http.StatusOK {
				t.Fatalf("status = %d (%v)", status, payload)
			}
			info, _ := payload["prefill"].(map[string]any)
			if info == nil {
				t.Fatalf("没有 prefill 说明：%v", payload)
			}
			if got, _ := info["band"].(string); got != c.wantBand {
				t.Fatalf("band = %q，期望 %q", got, c.wantBand)
			}
			if got, _ := info["bandReason"].(string); got != c.wantReason {
				t.Fatalf("bandReason = %q，期望 %q（低置信必须说清缺的是哪一侧）", got, c.wantReason)
			}
			signals, _ := info["signals"].([]any)
			found := false
			for _, s := range signals {
				if s == c.wantSignal {
					found = true
				}
			}
			if !found {
				t.Fatalf("信号里没有 %q：%v", c.wantSignal, signals)
			}
		})
	}
}

// 没有分段时跳过预填，并明确说明原因——而不是悄悄按 onEnter 猜一遍。
func TestDefaultSpecPrefillSkippedWithoutSegments(t *testing.T) {
	ts, _, _ := newPrefillServer(t, animation.PageContext{DurationMS: 6000, FPS: 30})

	status, payload := doJSON(t, http.MethodGet, ts.URL+animationURL()+"?default=true&prefill=true", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, payload)
	}
	info, _ := payload["prefill"].(map[string]any)
	if info == nil {
		t.Fatalf("没有 prefill 说明：%v", payload)
	}
	if info["applied"] != false || info["reason"] != "no_segments" {
		t.Fatalf("prefill = %v，期望 applied=false reason=no_segments", info)
	}
	// 退回既有默认规格（零配置也要能出一版），且**没有**任何分段触发：
	// 没有分段就不该出现 withSegment，否则等于凭空造了一个锚点。
	for id, trig := range triggersOf(t, payload) {
		if strings.HasSuffix(id, "#source") {
			continue
		}
		if strings.HasPrefix(trig, animation.TriggerSegmentPrefix) {
			t.Errorf("元素 %s 出现分段触发 %q，但没有分段", id, trig)
		}
	}
}

// 不加 prefill 参数时响应里不该出现 prefill 字段：旧客户端解析的是同一份 JSON。
func TestDefaultSpecWithoutPrefillFlagIsUnchanged(t *testing.T) {
	ts, _, _ := newPrefillServer(t, prefillPC(prefillSegments()...))

	_, payload := doJSON(t, http.MethodGet, ts.URL+animationURL()+"?default=true", nil)
	if _, ok := payload["prefill"]; ok {
		t.Fatalf("未请求预填却返回了 prefill 字段：%v", payload["prefill"])
	}
	// 未请求预填时保持既有顺序分配：第 1 条要点 → 第 1 段。
	if trig := triggersOf(t, payload)["shape-b1"]; trig != "withSegment:seg-1" {
		t.Errorf("shape-b1 = %q，既有顺序分配应把第 1 条要点绑到第 1 段", trig)
	}

	// 拼错的取值一律当"没开"：宁可什么都不做，也不要因为写了 yes 就换一种形态。
	_, payload = doJSON(t, http.MethodGet, ts.URL+animationURL()+"?default=true&prefill=yes", nil)
	if _, ok := payload["prefill"]; ok {
		t.Fatal("prefill=yes 不应被当成开启")
	}
}

// tasks 3.1：已有规格时默认生成根本没被调用，所以人工结果（哪怕是一条空轨道）
// 不会被预填覆盖；用户清空后也不会被自动填回。
func TestPrefillNeverOverridesSavedSpec(t *testing.T) {
	ts, _, _ := newPrefillServer(t, prefillPC(prefillSegments()...))

	body := validPutBody(0)
	body["tracks"] = []map[string]any{{
		"elementId": "shape-b1",
		"kind":      "bullet",
		"clips": []map[string]any{{
			"id": "c1", "type": "fadeIn", "startMs": 0, "durationMs": 400,
			"easing": "easeOut", "trigger": "withSegment:seg-2", "triggerSource": animation.TriggerSourceManual,
		}},
	}}
	status, payload := doJSON(t, http.MethodPut, ts.URL+animationURL(), body)
	if status != http.StatusOK {
		t.Fatalf("put status = %d (%v)", status, payload)
	}

	status, payload = doJSON(t, http.MethodGet, ts.URL+animationURL()+"?default=true&prefill=true", nil)
	if status != http.StatusOK {
		t.Fatalf("get status = %d (%v)", status, payload)
	}
	if rev := payload["revision"].(float64); rev != 1 {
		t.Fatalf("revision = %v，期望返回已保存的 1（预填不该新建一份）", rev)
	}
	if trig := triggersOf(t, payload)["shape-b1"]; trig != "withSegment:seg-2" {
		t.Errorf("shape-b1 = %q，人工绑定被覆盖了", trig)
	}

	// 用户清空轨道后，预填不得回填。
	empty := validPutBody(1)
	empty["tracks"] = []map[string]any{}
	if status, payload := doJSON(t, http.MethodPut, ts.URL+animationURL(), empty); status != http.StatusOK {
		t.Fatalf("put empty status = %d (%v)", status, payload)
	}
	status, payload = doJSON(t, http.MethodGet, ts.URL+animationURL()+"?default=true&prefill=true", nil)
	if status != http.StatusOK {
		t.Fatalf("get after clear status = %d (%v)", status, payload)
	}
	if tracks, _ := payload["tracks"].([]any); len(tracks) != 0 {
		t.Fatalf("清空后被回填了 %d 条轨道：%v", len(tracks), tracks)
	}
}
