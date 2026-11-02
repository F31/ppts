package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/project"
)

// scriptCompareSegment 是对照面板里"当前讲稿"的一句一段落视图。
type scriptCompareSegment struct {
	SegmentID string `json:"segmentId"`
	Text      string `json:"text"`
}

// scriptComparePayload 是 GET /projects/{pid}/slides/{sid}/script/compare 的应答。
//
// 为什么由服务端给出 originalText 而不是前端自己拼：
// 「原文」的取法（显式来源 > 形状文字 > 备注回退）与成稿时的输入是**同一套规则**
// （app.PageSourceText）。前端若只拿 GetSlides 的 preview（截断 120 字）或自己
// 拼形状文字，对照面板显示的就未必是模型当时吃的素材。
type scriptComparePayload struct {
	SlideID    string `json:"slideId"`
	RevisionNo int    `json:"revisionNo"`
	Language   string `json:"language"`
	// Mode 是当前讲稿的生成模式（original/polish/ai_generated）：同一段文本由不同模式
	// 产出，"这是润色稿还是原文稿"直接决定对照有没有意义。
	Mode           string                 `json:"mode"`
	ScriptRevision int64                  `json:"scriptRevision"`
	Source         string                 `json:"source"` // page|notes|custom|none
	OriginalText   string                 `json:"originalText"`
	CurrentText    string                 `json:"currentText"`
	Segments       []scriptCompareSegment `json:"segments"`
}

// editorScriptCompare 返回单页「原文 vs 当前讲稿」的对照数据（G2-2）。
//
// 只读、不改任何状态：用户接受部分句子在前端完成，落库仍走既有的讲稿保存通道
// （ScriptService.Update / 编辑器自动保存），因此这里没有"接受"端点——
// 多一条写路径就多一份与乐观并发（expected_revision）不一致的风险。
func editorScriptCompare(w http.ResponseWriter, r *http.Request, objects objectstore.ObjectStore, scripts narration.Store, srcStore app.ScriptSourceStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	proj, ok := requireProject(w, r, projects, members, recorder)
	if !ok {
		return
	}
	slideID := strings.TrimSpace(r.PathValue("sid"))
	if slideID == "" {
		http.Error(w, "slide_id required", http.StatusBadRequest)
		return
	}
	revNo := requestSourceRevision(r.Header)
	if revNo <= 0 {
		revNo, _ = strconv.Atoi(r.URL.Query().Get("revision_no"))
	}
	if revNo <= 0 {
		revNo = int(proj.CurrentRevision)
	}
	if revNo <= 0 {
		revNo = 1
	}
	language := requestLanguage(r.Header)

	rev, err := scripts.Get(r.Context(), principal.TenantID, proj.ID, revNo, slideID, language)
	if errors.Is(err, narration.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "script_not_found", "message": "该页还没有讲稿"})
		return
	}
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}

	pg, err := loadComparePage(r.Context(), objects, principal.TenantID, proj.ID, revNo, slideID)
	switch {
	case errors.Is(err, errProjectHasNoDocument):
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "no_parsed_document", "message": "该项目还没有解析完成的版本"})
		return
	case errors.Is(err, errSlideNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "slide_not_found", "message": "源版本中找不到该页"})
		return
	case err != nil:
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}

	source, custom := "", ""
	if srcStore != nil {
		// 来源选择按源版本隔离；读取失败不阻塞对照（退化为默认版面来源），
		// 但要显式记录——否则"对照的原文不对"会被误当成解析问题。
		choices, lerr := srcStore.List(r.Context(), principal.TenantID, proj.ID, revNo)
		if lerr == nil {
			if choice, ok := choices[slideID]; ok {
				source = string(choice.Kind)
				custom = choice.CustomText
			}
		}
	}
	kind, original := app.PageOriginalText(pg, source, custom)
	if strings.TrimSpace(original) == "" {
		kind = "none"
	}

	segments := make([]scriptCompareSegment, 0, len(rev.Segments))
	parts := make([]string, 0, len(rev.Segments))
	for _, seg := range rev.Segments {
		if seg == nil {
			continue
		}
		segments = append(segments, scriptCompareSegment{SegmentID: seg.SegmentID, Text: seg.DisplayText})
		if t := strings.TrimSpace(seg.DisplayText); t != "" {
			parts = append(parts, t)
		}
	}

	writeJSON(w, http.StatusOK, scriptComparePayload{
		SlideID:        slideID,
		RevisionNo:     revNo,
		Language:       language,
		Mode:           string(rev.Mode),
		ScriptRevision: rev.Revision,
		Source:         kind,
		OriginalText:   original,
		CurrentText:    strings.Join(parts, "\n"),
		Segments:       segments,
	})
}

// loadComparePage 读取源版本解析文档中的单页，并合并用户编辑过的备注。
//
// 备注合并必须与成稿路径一致：成稿 worker 读的是"用户备注优先"的版本
// （internal/app/scriptdraft.go 的 WithSlideNotes 段），对照若只读解析原件，
// 用户改过备注的页会出现"原文是旧的、讲稿是按新备注写的"。
func loadComparePage(ctx context.Context, objects objectstore.ObjectStore, tenantID, projectID string, revisionNo int, slideID string) (*project.Page, error) {
	key := objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: projectID,
		Revision: srcRevString(revisionNo), AssetType: "document", AssetID: "extracted", Ext: "json",
	}
	rc, _, err := objects.Get(ctx, key)
	if err != nil {
		if errors.Is(err, objectstore.ErrObjectNotFound) {
			return nil, errProjectHasNoDocument
		}
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	var doc project.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	notes := project.NewPgSlideNotesStore(objects)
	if stored, nerr := notes.Get(ctx, tenantID, projectID, revisionNo); nerr == nil {
		for i := range doc.Pages {
			if doc.Pages[i] == nil {
				continue
			}
			if n, ok := stored[doc.Pages[i].SlideID]; ok {
				doc.Pages[i].NotesText = n
			}
		}
	}
	for _, pg := range doc.Pages {
		if pg != nil && pg.SlideID == slideID {
			return pg, nil
		}
	}
	return nil, errSlideNotFound
}

var (
	errProjectHasNoDocument = errors.New("project has no parsed document yet")
	errSlideNotFound        = errors.New("slide not found in parsed document")
)
