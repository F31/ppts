package animation

import (
	"encoding/json"
	"strings"
	"testing"
)

// 1.2：无来源标注的既有规格，序列化字节必须与引入本字段前逐字节相同。
//
// 这不是「差不多」的要求：规格会落库、会进 planHash、会进快照测试。多出一个
// `"triggerSource":""` 会让每一份历史规格的哈希都变一次，缓存全失效、快照全红，
// 而用户什么都没改。
func TestClipJSONUnchangedWithoutTriggerSource(t *testing.T) {
	c := Clip{ID: "c1", Type: "fadeIn", StartMs: 0, DurationMs: 400, Easing: "easeOut", Trigger: "onEnter"}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"c1","type":"fadeIn","startMs":0,"durationMs":400,"easing":"easeOut","params":{},"trigger":"onEnter"}`
	if string(b) != want {
		t.Fatalf("序列化字节变了\n got: %s\nwant: %s", b, want)
	}
}

func TestClipJSONCarriesTriggerSource(t *testing.T) {
	c := Clip{ID: "c1", Type: "fadeIn", DurationMs: 400, Easing: "easeOut",
		Trigger: "withSegment:seg-1", TriggerSource: TriggerSourceSibling}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"triggerSource":"sibling"`) {
		t.Fatalf("来源标注没有进序列化: %s", b)
	}
}

func TestTriggerSourceWhitelist(t *testing.T) {
	sources := TriggerSources()
	if len(sources) != 8 {
		t.Fatalf("白名单有 %d 个值，期望 8：%v", len(sources), sources)
	}
	for _, s := range sources {
		if !ValidTriggerSource(s) {
			t.Fatalf("%q 在 TriggerSources() 里却通不过校验", s)
		}
	}
	if !ValidTriggerSource("") {
		t.Fatal("空串应视为「无标注」而合法")
	}
}

func TestUnknownTriggerSourceRejected(t *testing.T) {
	rev := validRevision()
	rev.Tracks[0].Clips[0].TriggerSource = "siblingBind" // 前端口误写
	err := rev.Validate(validPageContext())
	if err == nil {
		t.Fatal("未知来源标注被接受了（编辑器按来源筛选时这一条会从所有分类里消失）")
	}
	var ve *ValidationError
	if !asValidationError(err, &ve) {
		t.Fatalf("错误类型 = %T，期望 *ValidationError", err)
	}
	if ve.Status != 400 {
		t.Fatalf("状态码 = %d，期望 400", ve.Status)
	}
	if !strings.Contains(ve.Field, "triggerSource") {
		t.Fatalf("错误字段 = %q，未指向 triggerSource", ve.Field)
	}
	for _, s := range TriggerSources() {
		if !strings.Contains(ve.Hint, s) {
			t.Fatalf("Hint 没列出 %q：%s", s, ve.Hint)
		}
	}
}

func TestTriggerSourcesReturnsCopy(t *testing.T) {
	s := TriggerSources()
	s[0] = "hacked"
	if TriggerSources()[0] == "hacked" {
		t.Fatal("TriggerSources 返回了内部切片，调用方改一下就把白名单改了")
	}
}

// 置信度同样要保证「缺省不进字节」：上面那条 TestClipJSONUnchangedWithoutTriggerSource
// 钉的就是同一串字节，这里再显式断一次 confidence，免得将来把 omitempty 删了却没人发现。
func TestClipJSONOmitsZeroConfidence(t *testing.T) {
	c := Clip{ID: "c1", Type: "fadeIn", DurationMs: 400, Easing: "easeOut", Trigger: "onEnter"}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "confidence") {
		t.Fatalf("0 置信度进了序列化: %s", b)
	}

	c.Confidence = 0.31
	b, _ = json.Marshal(c)
	if !strings.Contains(string(b), `"confidence":0.31`) {
		t.Fatalf("置信度没有进序列化: %s", b)
	}
}

func TestConfidenceOutOfRangeRejected(t *testing.T) {
	for _, v := range []float64{-0.01, 1.01, 42} {
		rev := validRevision()
		rev.Tracks[0].Clips[0].Confidence = v
		err := rev.Validate(validPageContext())
		if err == nil {
			t.Fatalf("置信度 %v 被接受了", v)
		}
		var ve *ValidationError
		if !asValidationError(err, &ve) {
			t.Fatalf("错误类型 = %T，期望 *ValidationError", err)
		}
		if ve.Status != 400 || !strings.Contains(ve.Field, "confidence") {
			t.Fatalf("v=%v：状态 %d、字段 %q，期望 400 + confidence", v, ve.Status, ve.Field)
		}
	}
}

func asValidationError(err error, target **ValidationError) bool {
	ve, ok := err.(*ValidationError)
	if ok {
		*target = ve
	}
	return ok
}
