package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
)

// ScriptDraftSnapshot 是 script_draft 任务的输入快照。
// Mode 指定生成模式；G1 仅实现原文朗读（original），polish/ai_generated 走 G2。
type ScriptDraftSnapshot struct {
	ProjectID  string   `json:"projectId"`
	RevisionNo int      `json:"revisionNo"` // 0 = 当前最新已解析版本
	Language   string   `json:"language"`
	Mode       string   `json:"mode"`               // original / polish / ai_generated
	SlideIDs   []string `json:"slideIds,omitempty"` // 空 = 全部页面
}

// ScriptDraftHandler 是 script_draft 任务的 worker handler：
// 读最新解析 document.json → 逐页生成原文朗读草稿（幂等：已有讲稿不覆盖）。
type ScriptDraftHandler struct {
	scripts narration.Store
	objects objectstore.ObjectStore
}

// NewScriptDraftHandler 创建 handler。
func NewScriptDraftHandler(scripts narration.Store, objects objectstore.ObjectStore) *ScriptDraftHandler {
	return &ScriptDraftHandler{scripts: scripts, objects: objects}
}

// Handle 实现 pipeline.HandlerFunc。
func (h *ScriptDraftHandler) Handle(ctx context.Context, job *pipeline.Job) error {
	var snap ScriptDraftSnapshot
	if err := json.Unmarshal([]byte(job.InputSnapshot), &snap); err != nil {
		return fmt.Errorf("script_draft: invalid input snapshot: %w", err)
	}
	if snap.Mode != "" && snap.Mode != string(narration.ModeOriginal) {
		return fmt.Errorf("script_draft: mode %q not implemented (G2)", snap.Mode)
	}
	if snap.Language == "" {
		snap.Language = "zh-CN"
	}
	pages, err := h.loadPages(ctx, job.TenantID, snap.ProjectID, snap.RevisionNo)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, id := range snap.SlideIDs {
		want[id] = true
	}
	for _, pg := range pages {
		if len(want) > 0 && !want[pg.SlideID] {
			continue
		}
		if err := h.ensureDraft(ctx, job.TenantID, snap.ProjectID, snap.Language, pg); err != nil {
			return err
		}
	}
	return nil
}

// parsedPage 是 document.json 页面的最小读取视图。
type parsedPage struct {
	Index     int    `json:"index"`
	SlideID   string `json:"slideId"`
	NotesText string `json:"notesText"`
	Shapes    []struct {
		Text string `json:"text"`
	} `json:"shapes"`
}

func (h *ScriptDraftHandler) loadPages(ctx context.Context, tenantID, projectID string, revisionNo int) ([]parsedPage, error) {
	if projectID == "" {
		return nil, errors.New("script_draft: project_id is required")
	}
	if revisionNo == 0 {
		revisionNo = 1 // G1：脚本生成跟随首个完成的解析版本；后续版本接入时改为查当前版本。
	}
	key := objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: projectID,
		Revision: srcRevString(revisionNo), AssetType: "document", AssetID: "extracted", Ext: "json",
	}
	rc, _, err := h.objects.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("script_draft: read parsed document: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Pages []parsedPage `json:"pages"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("script_draft: invalid parsed document: %w", err)
	}
	return doc.Pages, nil
}

// ensureDraft 为单个页面生成原文草稿。已存在讲稿（含占位或用户已编辑）则跳过，不覆盖。
func (h *ScriptDraftHandler) ensureDraft(ctx context.Context, tenantID, projectID, language string, pg parsedPage) error {
	slideID := pg.SlideID
	if slideID == "" {
		return nil
	}
	if _, err := h.scripts.Get(ctx, tenantID, projectID, slideID, language); err == nil {
		return nil // 已存在，不覆盖（含占位）。
	} else if !errors.Is(err, narration.ErrNotFound) {
		return err
	}

	text := pgText(pg)
	if text == "" {
		return nil // 无正文/备注，不生成空讲稿。
	}
	rev, err := h.scripts.EnsureExists(ctx, tenantID, projectID, slideID, language, narration.ModeOriginal)
	if err != nil {
		return fmt.Errorf("script_draft: ensure script: %w", err)
	}
	segment := &narration.Segment{
		SegmentID:   fmt.Sprintf("seg-%02d", pg.Index+1),
		DisplayText: text,
		SpokenText:  text,
		SourceRefs:  []string{slideID},
		Status:      narration.StatusDraft,
	}
	if _, err := h.scripts.Update(ctx, tenantID, projectID, slideID, language, rev.Revision, []*narration.Segment{segment}); err != nil {
		return fmt.Errorf("script_draft: write draft: %w", err)
	}
	return nil
}

// pgText 拼接页面形状文本；为空时回退到备注。
func pgText(pg parsedPage) string {
	var parts []string
	for _, sh := range pg.Shapes {
		if t := strings.TrimSpace(sh.Text); t != "" {
			parts = append(parts, t)
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		text = strings.TrimSpace(pg.NotesText)
	}
	return text
}
