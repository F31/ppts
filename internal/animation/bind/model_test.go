package bind

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 提示词必须与 scripts/fidelity/bind_llm.py 里的 SYSTEM_PROMPT 逐字一致。
//
// 这不是洁癖：离线实验量的是那条提示词的效果，产品里换一句就等于换了一个被测对象，
// 而 README 上的 0.833 / 0.972 还是旧数字。两边分叉时这条用例会直接红，
// 而不是等到下一次实测发现「指标对不上」再去翻提示词。
func TestSystemPromptMatchesPythonHarness(t *testing.T) {
	const path = "../../../scripts/fidelity/bind_llm.py"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("离线脚本不在（%v）：只在完整仓库里做这道对账", err)
	}
	m := regexp.MustCompile(`(?s)SYSTEM_PROMPT = """(.*?)"""`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("在 bind_llm.py 里找不到 SYSTEM_PROMPT")
	}
	// Python 以 universal newlines 读源码，Go 读到的是原始字节；仓库里这个文件是 CRLF，
	// 不归一化会比出一个纯换行差异。
	py := strings.ReplaceAll(string(m[1]), "\r\n", "\n")
	if py != modelSystemPrompt {
		t.Fatalf("系统提示词与离线脚本不一致（Go %d 字节 / Python %d 字节）", len(modelSystemPrompt), len(py))
	}
}

// 行式输入的形状与截断口径要和 _clip / build_page_input 一致。
func TestModelPromptShape(t *testing.T) {
	long := strings.Repeat("长", maxElemChars+20)
	elements := []Element{
		{ID: "e1", Kind: "bullet", Box: Box{X: 100, Y: 100, W: 400, H: 60}, Text: "第一行\n第二行"},
		{ID: "e2", Kind: "shape", Box: Box{X: 100, Y: 200, W: 400, H: 60}},
		{ID: "e3", Kind: "bullet", Box: Box{X: 100, Y: 300, W: 400, H: 60}, Text: long},
	}
	segments := []Segment{{ID: "s1", Text: "讲讲背景"}}
	p := modelPromptFor("slide-7", elements, segments)

	if p.System != modelSystemPrompt {
		t.Fatal("System 提示词没有带上")
	}
	lines := strings.Split(p.User, "\n")
	if lines[0] != "SLIDE slide-7" {
		t.Fatalf("首行 = %q，期望 SLIDE slide-7", lines[0])
	}
	if lines[1] != "E e1 | bullet | 第一行 / 第二行" {
		t.Fatalf("换行没有压成 ' / '：%q", lines[1])
	}
	if lines[2] != "E e2 | shape | (no text)" {
		t.Fatalf("无文字元素应写 (no text)：%q", lines[2])
	}
	if n := len([]rune(strings.TrimPrefix(lines[3], "E e3 | bullet | "))); n != maxElemChars {
		t.Fatalf("截断后长度 = %d，期望 %d", n, maxElemChars)
	}
	if !strings.HasSuffix(lines[3], "…") {
		t.Fatalf("截断后应带省略号：%q", lines[3])
	}
	if lines[4] != "SEG" || lines[5] != "S s1 | 讲讲背景" {
		t.Fatalf("段落段格式不对：%q / %q", lines[4], lines[5])
	}
}

func TestValidateRejectsEachKind(t *testing.T) {
	elements := []Element{
		{ID: "b1", Kind: "bullet", Text: "背景数据说明"},
		{ID: "b2", Kind: "bullet", Text: "做法共三步"},
		{ID: "footer1", Kind: "footer", Text: "© 2026 某公司"},
		{ID: "card", Kind: "shape", Text: ""},
	}
	segments := []Segment{{ID: "s1", Text: "讲背景"}, {ID: "s2", Text: "讲做法"}}

	raw := `{"bindings":[
		{"elementId":"nope","segmentId":"s1","confidence":0.9},
		{"elementId":"b1","segmentId":"nope","confidence":0.9},
		{"elementId":"b1","segmentId":"s1","confidence":0.9},
		{"elementId":"b1","segmentId":"s2","confidence":0.8},
		{"elementId":"footer1","segmentId":"s1","confidence":0.9},
		{"elementId":"card","segmentId":"s1","confidence":0.9},
		{"elementId":"b2","segmentId":"s2","confidence":7},
		{"elementId":"b2","segmentId":"s2","confidence":0.7},
		"这不是对象"
	]}`
	v := validateModelOutput(raw, elements, segments, nil)

	want := map[string]int{
		RejectNotObject:    1,
		RejectBadElementID: 1,
		RejectBadSegmentID: 1,
		RejectDuplicate:    1,
		RejectBadConf:      1,
		RejectRuleLocked:   0,
		RejectAuxElement:   1,
		RejectNoText:       1,
	}
	for k, n := range want {
		if v.Rejected[k] != n {
			t.Errorf("%s = %d，期望 %d（全部统计：%v）", k, v.Rejected[k], n, v.Rejected)
		}
	}
	if !v.ParseOK {
		t.Fatal("输出是一个合法对象，ParseOK 应为 true")
	}
	if len(v.Pairs) != 2 {
		t.Fatalf("被接受的绑定 = %d 条，期望 2（b1→s1、b2→s2）：%v", len(v.Pairs), v.Pairs)
	}
	if v.Pairs[0].elem != 0 || v.Pairs[0].seg != 0 || v.Pairs[0].conf != 0.9 {
		t.Fatalf("第一条 = %+v，期望 b1→s1 且置信度 0.9", v.Pairs[0])
	}
	if v.Pairs[1].elem != 1 || v.Pairs[1].seg != 1 || v.Pairs[1].conf != 0.7 {
		t.Fatalf("第二条 = %+v，期望 b2→s2 且置信度 0.7", v.Pairs[1])
	}
	// 条目数取输出里的 9 条（含那条非对象），不是接受数：拒绝率的分母必须是它。
	if v.Items != 9 {
		t.Fatalf("分母 = %d，期望 9", v.Items)
	}
}

func TestValidateRejectsFabricatedIDsAsFormatError(t *testing.T) {
	elements := []Element{{ID: "b1", Kind: "bullet", Text: "背景"}}
	segments := []Segment{{ID: "s1", Text: "讲背景"}}
	v := validateModelOutput(`{"bindings":[{"elementId":"b9","segmentId":"s9"}]}`, elements, segments, nil)
	if v.Rejected[RejectBadElementID] != 1 {
		t.Fatalf("编造 id 应记 bad_element_id：%v", v.Rejected)
	}
	if len(v.Pairs) != 0 {
		t.Fatalf("编造 id 不能进结果：%v", v.Pairs)
	}
}

func TestValidateRejectsNonJSON(t *testing.T) {
	elements := []Element{{ID: "b1", Kind: "bullet", Text: "背景"}}
	segments := []Segment{{ID: "s1", Text: "讲背景"}}
	for _, raw := range []string{
		"",
		"好的，我来帮你绑定：",
		`[{"elementId":"b1","segmentId":"s1"}]`,
		`{"bindings":[{"elementId":"b1"`,
		`{"binding":[]}`,
	} {
		v := validateModelOutput(raw, elements, segments, nil)
		if v.ParseOK {
			t.Fatalf("%q 被当成了合法输出", raw)
		}
		if v.Rejected[RejectNotObject] != 1 {
			t.Fatalf("%q：not_object = %d，期望 1", raw, v.Rejected[RejectNotObject])
		}
		if v.Items != 1 {
			t.Fatalf("%q：分母 = %d，期望 1（非 JSON 也要有分母）", raw, v.Items)
		}
	}
}

// 代码块是模型最常见的「包装」，剥一层即可；剥完还不是整段 JSON 就不收。
func TestValidateStripsOneCodeFence(t *testing.T) {
	elements := []Element{{ID: "b1", Kind: "bullet", Text: "背景"}}
	segments := []Segment{{ID: "s1", Text: "讲背景"}}
	v := validateModelOutput("```json\n{\"bindings\":[{\"elementId\":\"b1\",\"segmentId\":\"s1\",\"confidence\":0.6}]}\n```", elements, segments, nil)
	if len(v.Pairs) != 1 {
		t.Fatalf("代码块里的合法输出没有被接受：%v", v.Rejected)
	}
}

func TestValidateRespectsAllowIDs(t *testing.T) {
	elements := []Element{
		{ID: "b1", Kind: "bullet", Text: "背景"},
		{ID: "b2", Kind: "bullet", Text: "做法"},
	}
	segments := []Segment{{ID: "s1", Text: "讲背景"}, {ID: "s2", Text: "讲做法"}}
	raw := `{"bindings":[{"elementId":"b1","segmentId":"s1"},{"elementId":"b2","segmentId":"s2"}]}`
	v := validateModelOutput(raw, elements, segments, map[string]struct{}{"b2": {}})
	if v.Rejected[RejectRuleLocked] != 1 {
		t.Fatalf("规则已绑的元素应记 rule_locked：%v", v.Rejected)
	}
	if len(v.Pairs) != 1 || v.Pairs[0].elem != 1 {
		t.Fatalf("只该留下 b2：%v", v.Pairs)
	}
}

// 缓存键必须把模型、提示词版本、采样序号与提示词全文都算进去。
//
// 采样序号是最容易漏的一个：temperature=0 在实测里并不确定（同一提示词三次
// 6/11/7 条），漏掉序号会让多轮实验全部命中第一轮的结果，看起来「方差为零」。
func TestModelCacheKeyCoversEveryInput(t *testing.T) {
	base := modelCacheKey("m1", "bind-v1", 0, "sys", "user")
	if len(base) != 32 {
		t.Fatalf("键长 = %d，期望 32 位十六进制", len(base))
	}
	if strings.ToLower(base) != base {
		t.Fatalf("键里有大写：%s", base)
	}
	variants := []struct{ name, key string }{
		{"换模型", modelCacheKey("m2", "bind-v1", 0, "sys", "user")},
		{"换提示词版本", modelCacheKey("m1", "bind-v2", 0, "sys", "user")},
		{"换采样序号", modelCacheKey("m1", "bind-v1", 1, "sys", "user")},
		{"换系统提示词", modelCacheKey("m1", "bind-v1", 0, "sys2", "user")},
		{"换用户输入", modelCacheKey("m1", "bind-v1", 0, "sys", "user2")},
	}
	for _, v := range variants {
		if v.key == base {
			t.Errorf("%s 后键没变（%s）：这项输入没有进缓存键", v.name, base)
		}
	}
	if again := modelCacheKey("m1", "bind-v1", 0, "sys", "user"); again != base {
		t.Errorf("同一输入两次算出不同的键：%s vs %s", base, again)
	}
}
