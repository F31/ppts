package pipeline

import (
	"strconv"
	"strings"
	"testing"
)

// 快照 → 范围 的推导用例（原在 internal/api，B4-M6b 把实现下沉到 pipeline 后一并搬过来）。
// 这是纯函数，因此无需数据库即可在默认测试套件中运行。
func TestScopeOf(t *testing.T) {
	cases := []struct {
		name         string
		kind         JobKind
		snapshot     string
		wantKind     string
		wantPages    int
		wantRevision int64
		wantPageIDs  []string
		wantFormat   string
	}{
		{
			name: "narration 按页生成", kind: KindNarration,
			snapshot:     `{"slides":[{"slideId":"s1","scriptRevision":3},{"slideId":"s2","scriptRevision":4}],"segmentIds":[],"language":"zh"}`,
			wantKind:     "pages",
			wantPages:    2,
			wantRevision: 4,
			wantPageIDs:  []string{"s1", "s2"},
		},
		{
			name: "narration 局部重生成视为 segments", kind: KindNarration,
			snapshot:     `{"slides":[{"slideId":"s1","scriptRevision":2}],"segmentIds":["seg-1"]}`,
			wantKind:     "segments",
			wantPages:    1,
			wantRevision: 2,
			wantPageIDs:  []string{"s1"},
		},
		{
			// segmentIds 只决定 Kind，**不**进入页列表（migrations/0026 的回填 SQL 与此一致）。
			name: "narration 的页列表只看 slides，不含 segmentIds", kind: KindNarration,
			snapshot:    `{"slides":[{"slideId":"s1"},{"slideId":"s2"}],"segmentIds":["seg-1","seg-2","seg-3"]}`,
			wantKind:    "segments",
			wantPages:   2,
			wantPageIDs: []string{"s1", "s2"},
		},
		{
			name: "narration 重复与空页 ID 去重", kind: KindNarration,
			snapshot:    `{"slides":[{"slideId":"s1"},{"slideId":" s1 "},{"slideId":""}],"segmentIds":[]}`,
			wantKind:    "pages",
			wantPages:   1,
			wantPageIDs: []string{"s1"},
		},
		{
			name: "script_draft 空 slideIds 表示全篇", kind: KindScriptDraft,
			snapshot:     `{"projectId":"p1","revisionNo":7,"mode":"original"}`,
			wantKind:     "project",
			wantPages:    0,
			wantRevision: 7,
			wantPageIDs:  []string{},
		},
		{
			name: "script_draft 指定页", kind: KindScriptDraft,
			snapshot:     `{"slideIds":["s9"],"revisionNo":2}`,
			wantKind:     "pages",
			wantPages:    1,
			wantRevision: 2,
			wantPageIDs:  []string{"s9"},
		},
		{
			name: "parse 面向全篇并带输入版本", kind: KindParse,
			snapshot:     `{"sourceRevisionId":"rev-1","revisionNo":5,"parserVersion":"v1"}`,
			wantKind:     "project",
			wantPages:    0,
			wantRevision: 5,
			wantPageIDs:  []string{},
		},
		{
			name: "export 只计数不暴露对象键", kind: KindExport,
			snapshot:    `{"format":"mp4","pagePngKeys":["t/s1.png","t/s2.png","t/s3.png"]}`,
			wantKind:    "export",
			wantPages:   3,
			wantPageIDs: []string{},
			wantFormat:  "mp4",
		},
		{
			// render 的 handler 未写步骤/快照页列表，故不猜页数（保持 unknown）。
			name: "render 无页列表", kind: KindRender,
			snapshot: `{"slideId":"s1"}`,
			wantKind: "unknown", wantPageIDs: []string{},
		},
		{
			name: "空快照", kind: KindNarration, snapshot: "",
			wantKind: "unknown", wantPageIDs: []string{},
		},
		{
			name: "只有空白字符的快照", kind: KindNarration, snapshot: "   \n ",
			wantKind: "unknown", wantPageIDs: []string{},
		},
		{
			name: "非法 JSON", kind: KindNarration, snapshot: `{"slides":`,
			wantKind: "unknown", wantPageIDs: []string{},
		},
		{
			// slides 类型不符会让整体 Unmarshal 失败 → unknown：宁可不识别，也不猜页数。
			// （migrations/0026 的回填用 jsonb_typeof 守卫，此处落空数组，与之一致。）
			name: "slides 不是数组时退回 unknown", kind: KindNarration, snapshot: `{"slides":"oops"}`,
			wantKind: "unknown", wantPageIDs: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScopeOf(tc.kind, tc.snapshot)
			if got.Kind != tc.wantKind {
				t.Fatalf("kind = %q want %q", got.Kind, tc.wantKind)
			}
			if got.PageCount != tc.wantPages {
				t.Fatalf("pageCount = %d want %d", got.PageCount, tc.wantPages)
			}
			if got.InputRevision != tc.wantRevision {
				t.Fatalf("inputRevision = %d want %d", got.InputRevision, tc.wantRevision)
			}
			if got.Format != tc.wantFormat {
				t.Fatalf("format = %q want %q", got.Format, tc.wantFormat)
			}
			// AffectedPages 必须始终非 nil，否则 JSON 会序列化成 null 而不是 []。
			if got.AffectedPages == nil {
				t.Fatalf("affectedPages must not be nil")
			}
			if len(got.AffectedPages) != len(tc.wantPageIDs) {
				t.Fatalf("affectedPages = %v want %v", got.AffectedPages, tc.wantPageIDs)
			}
			for i, id := range tc.wantPageIDs {
				if got.AffectedPages[i] != id {
					t.Fatalf("affectedPages[%d] = %q want %q", i, got.AffectedPages[i], id)
				}
			}
		})
	}
}

// ScopeOf 不设条数上限（上限属传输层），因此超长页表必须完整返回——否则落库的
// jobs.affected_pages 会被截断，页数排序随之失真。
func TestScopeOfKeepsAllPagesWithoutTransportCap(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"slides":[`)
	const n = 250
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"slideId":"s`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`"}`)
	}
	b.WriteString(`],"segmentIds":[]}`)
	got := ScopeOf(KindNarration, b.String())
	if got.PageCount != n {
		t.Fatalf("pageCount = %d want %d", got.PageCount, n)
	}
	if len(got.AffectedPages) != n {
		t.Fatalf("affectedPages len = %d want %d (ScopeOf 不应做传输层截断)", len(got.AffectedPages), n)
	}
}

// AffectedPagesOf 是落库用的便捷函数，必须与 ScopeOf 完全一致（否则列表排序与「范围」列会矛盾）。
func TestAffectedPagesOfMatchesScopeOf(t *testing.T) {
	snapshots := []struct {
		kind JobKind
		snap string
	}{
		{KindNarration, `{"slides":[{"slideId":"s1"},{"slideId":"s2"}],"segmentIds":["x"]}`},
		{KindScriptDraft, `{"slideIds":["s3"],"revisionNo":1}`},
		{KindScriptDraft, `{"revisionNo":1}`},
		{KindParse, `{"revisionNo":1}`},
		{KindExport, `{"format":"mp4","pagePngKeys":["a"]}`},
		{KindRender, `{"slideId":"s1"}`},
		{KindNarration, ``},
	}
	for _, s := range snapshots {
		want := ScopeOf(s.kind, s.snap).AffectedPages
		got := AffectedPagesOf(s.kind, s.snap)
		if len(got) != len(want) {
			t.Fatalf("kind=%s len = %d want %d", s.kind, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("kind=%s [%d] = %q want %q", s.kind, i, got[i], want[i])
			}
		}
	}
}

// 游标 round-trip：排序键值里可能含 ':'、'.'、'+' 等字符（RFC3339Nano），必须能原样还原。
func TestJobCursorRoundTrip(t *testing.T) {
	cases := [][2]string{
		{"2026-09-16T01:02:03.123456789+08:00", "6f1b0e9a-0000-4000-8000-000000000001"},
		{"tts_segment", "abc-def"},
		{"0", "x"},
		{"中文阶段", "id-1"},
	}
	for _, c := range cases {
		cur := encodeJobCursor(c[0], c[1])
		gotVal, gotID, err := decodeJobCursor(cur)
		if err != nil {
			t.Fatalf("decode(%q) error: %v", cur, err)
		}
		if gotVal != c[0] || gotID != c[1] {
			t.Fatalf("round trip = (%q,%q) want (%q,%q)", gotVal, gotID, c[0], c[1])
		}
	}
}

func TestDecodeJobCursorRejectsGarbage(t *testing.T) {
	// base64 非法 / 缺少分隔符 / 任一侧为空，都应返回 ErrBadJobCursor（api 映射为 400）。
	bad := []string{"!!!not-base64!!!", encodeJobCursor("only-one-part", ""), encodeJobCursor("", "id"), "aGVsbG8"}
	for _, cur := range bad {
		if _, _, err := decodeJobCursor(cur); err == nil {
			t.Fatalf("decode(%q) should fail", cur)
		}
	}
}

func TestJobSortSpec(t *testing.T) {
	cases := map[string][2]string{
		"created": {"created_at", "timestamptz"},
		"updated": {"updated_at", "timestamptz"},
		"phase":   {"phase", "text"},
		"pages":   {"jsonb_array_length(affected_pages)", "int"},
		// 未知值必须退回 created（api 侧另有白名单校验并返回 400）。
		"":           {"created_at", "timestamptz"},
		"DROP TABLE": {"created_at", "timestamptz"},
	}
	for in, want := range cases {
		expr, cast := jobSortSpec(in)
		if expr != want[0] || cast != want[1] {
			t.Fatalf("jobSortSpec(%q) = (%q,%q) want (%q,%q)", in, expr, cast, want[0], want[1])
		}
	}
}

// 排序键的 SQL 表达式绝不能来自用户输入（注入面）：白名单之外一律落到 created_at。
func TestJobSortSpecNeverEchoesInput(t *testing.T) {
	expr, cast := jobSortSpec("id) DROP TABLE jobs; --")
	if strings.Contains(expr, "DROP") || strings.Contains(cast, "DROP") {
		t.Fatalf("sort expression must not contain user input: %q / %q", expr, cast)
	}
	if expr != "created_at" {
		t.Fatalf("expr = %q want created_at", expr)
	}
}
