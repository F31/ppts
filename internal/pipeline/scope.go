package pipeline

import (
	"encoding/json"
	"strings"
)

// Scope 描述一个任务的作用范围（由 jobs.input_snapshot 推导）。
//
// 为什么把这段逻辑放在 pipeline 而不是 api：B4-M6b 需要把「受影响页」落成 jobs.affected_pages
// （供列表侧排序/计数），而界面上的「范围」列仍由快照推导。若两处各写一份解析逻辑，
// 同一任务的范围列与页数排序就可能互相矛盾——因此快照解析**只有这一处**实现。
type Scope struct {
	// Kind：project=全篇 | pages=指定页 | segments=指定分段 | export=导出 | unknown=未识别
	Kind          string   `json:"kind"`
	PageCount     int      `json:"pageCount"`
	AffectedPages []string `json:"affectedPages"`
	// InputRevision 是快照记录的输入版本（讲稿 RevisionNo / 每页 scriptRevision），0 表示快照未记录。
	InputRevision int64  `json:"inputRevision"`
	Format        string `json:"format,omitempty"` // Kind=export 时的产物格式
}

// ScopeOf 按任务种类解析输入快照，推导范围 / 受影响页 / 输入版本。
//
// 解析失败或种类未知时返回 Kind="unknown"（**不报错**）：范围不可识别不应让整个详情接口失败，
// 界面据此显示「—」而不是伪造范围。
//
// 返回的 AffectedPages 始终非 nil（便于序列化成 []），且**不受条数上限约束**——
// 上限属传输层关注点，由调用方（api 的 maxScopePages）自行截断；PageCount 始终为完整计数，
// 故 PageCount > len(AffectedPages) 即表示数组已被截断。
func ScopeOf(kind JobKind, snapshot string) Scope {
	out := Scope{Kind: "unknown", AffectedPages: []string{}}
	if strings.TrimSpace(snapshot) == "" {
		return out
	}
	tally := newPageTally()
	switch kind {
	case KindNarration:
		var snap struct {
			Slides []struct {
				SlideID        string `json:"slideId"`
				ScriptRevision int64  `json:"scriptRevision"`
			} `json:"slides"`
			SegmentIDs []string `json:"segmentIds"`
		}
		if err := json.Unmarshal([]byte(snapshot), &snap); err != nil {
			return out
		}
		// 局部重生成（segmentIds 非空）与按页生成是两种范围。
		// 注意：受影响页始终取 slides[].slideId；segmentIds 只决定 Kind，**不**进入页列表。
		// migrations/0026 的回填 SQL 必须与此一致。
		out.Kind = "pages"
		if len(snap.SegmentIDs) > 0 {
			out.Kind = "segments"
		}
		for _, s := range snap.Slides {
			tally.add(s.SlideID)
			if s.ScriptRevision > out.InputRevision {
				out.InputRevision = s.ScriptRevision
			}
		}
	case KindScriptDraft:
		var snap struct {
			SlideIDs   []string `json:"slideIds"`
			RevisionNo int64    `json:"revisionNo"`
		}
		if err := json.Unmarshal([]byte(snapshot), &snap); err != nil {
			return out
		}
		out.InputRevision = snap.RevisionNo
		if len(snap.SlideIDs) == 0 {
			// 空 = 全部页面（app.ScriptDraftSnapshot.SlideIDs 注释）。
			out.Kind = "project"
			return out
		}
		out.Kind = "pages"
		for _, id := range snap.SlideIDs {
			tally.add(id)
		}
	case KindParse:
		var snap struct {
			RevisionNo int64 `json:"revisionNo"`
		}
		if err := json.Unmarshal([]byte(snapshot), &snap); err != nil {
			return out
		}
		// 解析面向整份源文件（全篇）。
		out.Kind = "project"
		out.InputRevision = snap.RevisionNo
	case KindExport:
		var snap struct {
			Format      string   `json:"format"`
			PagePNGKeys []string `json:"pagePngKeys"`
		}
		if err := json.Unmarshal([]byte(snapshot), &snap); err != nil {
			return out
		}
		out.Kind = "export"
		out.Format = snap.Format
		// pagePngKeys 是对象键（内部存储键），只用于计数，不作为页面 ID 透出。
		out.PageCount = len(snap.PagePNGKeys)
		return out
	default:
		return out
	}
	out.PageCount = tally.count
	out.AffectedPages = tally.pages
	return out
}

// AffectedPagesOf 只取受影响页/段 ID 列表（供 jobs.affected_pages 落库与页数排序）。
// 取不到时返回空切片（不猜测）。
func AffectedPagesOf(kind JobKind, snapshot string) []string {
	return ScopeOf(kind, snapshot).AffectedPages
}

// pageTally 统计去重后的页面数并保留出现顺序。
type pageTally struct {
	seen  map[string]bool
	pages []string
	count int
}

func newPageTally() *pageTally {
	return &pageTally{seen: make(map[string]bool), pages: []string{}}
}

func (t *pageTally) add(id string) {
	id = strings.TrimSpace(id)
	if id == "" || t.seen[id] {
		return
	}
	t.seen[id] = true
	t.count++
	t.pages = append(t.pages, id)
}
