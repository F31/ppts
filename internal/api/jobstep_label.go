package api

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
)

// maxJobDocumentBytes 限制任务详情为了取页标题而读入的解析文档大小。
// 读失败或超限时步骤仍返回，只是没有标题和概述，不能让详情接口因此失败。
const maxJobDocumentBytes = 32 << 20

// jobSlideLabel 是一页在任务详情里的可读身份：文稿位置、PPT 标题、概述。
// Page 从 1 起，与编辑器左侧页码一致。
type jobSlideLabel struct {
	SlideID string
	Page    int
	Title   string
	Summary string
}

// jobSlideSource 按源版本读取解析文档里的页标题。未配置或读取失败时返回 nil。
type jobSlideSource interface {
	SlideLabels(ctx context.Context, tenantID, userID, projectID string, revision int) map[string]jobSlideLabel
}

// stepSubject 是一步执行记录上要展示的页身份。
// Segment 只在分段配音上有意义，表示该页内的第几段（从 1 起）。
type stepSubject struct {
	Page    int
	Title   string
	Summary string
	Segment int
}

type revisionLookup interface {
	GetProject(ctx context.Context, tenantID, userID, id string) (*project.Project, error)
}

type documentGetter interface {
	Get(ctx context.Context, key objectstore.ObjectKey) (io.ReadCloser, objectstore.ObjectMeta, error)
}

// jobDocumentLabels 从解析文档取页标题。projects/objects 任一为空时直接放弃。
type jobDocumentLabels struct {
	projects revisionLookup
	objects  documentGetter
}

func (l jobDocumentLabels) SlideLabels(ctx context.Context, tenantID, userID, projectID string, revision int) map[string]jobSlideLabel {
	if l.objects == nil || strings.TrimSpace(projectID) == "" {
		return nil
	}
	if revision <= 0 && l.projects != nil {
		proj, err := l.projects.GetProject(ctx, tenantID, userID, projectID)
		if err != nil || proj == nil || proj.CurrentRevision <= 0 {
			return nil
		}
		revision = proj.CurrentRevision
	}
	if revision <= 0 {
		return nil
	}
	rc, _, err := l.objects.Get(ctx, objectstore.DocumentKey(tenantID, projectID, srcRevString(revision)))
	if err != nil {
		return nil
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxJobDocumentBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxJobDocumentBytes {
		return nil
	}
	return labelsFromDocument(data)
}

// snapshotSourceRevision 取快照里的源版本号（解析文档所在的版本），不是讲稿修订号。
func snapshotSourceRevision(kind pipeline.JobKind, snapshot string) int {
	var rev struct {
		RevisionNo       int `json:"revisionNo"`
		SourceRevisionNo int `json:"sourceRevisionNo"`
	}
	if err := json.Unmarshal([]byte(snapshot), &rev); err != nil {
		return 0
	}
	if rev.SourceRevisionNo > 0 {
		return rev.SourceRevisionNo
	}
	if rev.RevisionNo > 0 {
		return rev.RevisionNo
	}
	return 0
}

// snapshotPageOrder 是快照里的页序（从 1 起）。解析文档读不到时用它代替文稿页码。
func snapshotPageOrder(kind pipeline.JobKind, snapshot string) map[string]int {
	pages := pipeline.ScopeOf(kind, snapshot).AffectedPages
	out := make(map[string]int, len(pages))
	for i, id := range pages {
		if id != "" {
			out[id] = i + 1
		}
	}
	return out
}

// parseStepTarget 从步骤键取出页 id 和分段 id。认不出的键返回空，调用方只显示步骤类型。
func parseStepTarget(stepType, stepKey string) (slideID, segmentID string) {
	switch stepType {
	case "page":
		const prefix = "page:v1:"
		if strings.HasPrefix(stepKey, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(stepKey, prefix)), ""
		}
	case "tts_segment":
		rest := strings.TrimPrefix(stepKey, "tts_segment:")
		if rest == stepKey {
			return "", ""
		}
		parts := strings.SplitN(rest, ":", 3)
		if len(parts) >= 2 {
			return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		}
	}
	return "", ""
}

// subjectsForSteps 给每一步补上页码、标题、概述。分段号按该页在本任务中的出现顺序计，
// 必须在截断「只留最近 N 步」之前算完，否则后半段的段号会从 1 重新开始。
func subjectsForSteps(steps []pipeline.JobStep, labels map[string]jobSlideLabel, order map[string]int) []stepSubject {
	out := make([]stepSubject, len(steps))
	segmentNo := map[string]int{}
	for i, st := range steps {
		slideID, _ := parseStepTarget(st.StepType, st.StepKey)
		if slideID == "" {
			continue
		}
		subject := stepSubject{}
		if label, ok := labels[slideID]; ok {
			subject.Page = label.Page
			subject.Title = label.Title
			subject.Summary = label.Summary
		}
		if subject.Page == 0 {
			subject.Page = order[slideID]
		}
		if st.StepType == "tts_segment" {
			segmentNo[slideID]++
			subject.Segment = segmentNo[slideID]
		}
		out[i] = subject
	}
	return out
}

func applyStepSubject(item map[string]any, subject stepSubject) {
	if subject.Page > 0 {
		item["page"] = subject.Page
	}
	if subject.Title != "" {
		item["title"] = subject.Title
	}
	if subject.Summary != "" {
		item["summary"] = subject.Summary
	}
	if subject.Segment > 0 {
		item["segment"] = subject.Segment
	}
}

// slideLabelPayload 把受影响页变成详情可直接展示的列表。没有标题时仍给出页码。
func slideLabelPayload(ids []string, labels map[string]jobSlideLabel, order map[string]int) []map[string]any {
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		item := map[string]any{"slideId": id}
		if label, ok := labels[id]; ok {
			if label.Page > 0 {
				item["page"] = label.Page
			}
			if label.Title != "" {
				item["title"] = label.Title
			}
			if label.Summary != "" {
				item["summary"] = label.Summary
			}
		} else if page := order[id]; page > 0 {
			item["page"] = page
		}
		out = append(out, item)
	}
	return out
}

func labelsFromDocument(data []byte) map[string]jobSlideLabel {
	var doc struct {
		Pages []struct {
			SlideID   string      `json:"slideId"`
			Name      string      `json:"name"`
			NotesText string      `json:"notesText"`
			Shapes    []shapeText `json:"shapes"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(data, &doc); err != nil || len(doc.Pages) == 0 {
		return nil
	}
	out := make(map[string]jobSlideLabel, len(doc.Pages))
	for i, pg := range doc.Pages {
		id := strings.TrimSpace(pg.SlideID)
		if id == "" {
			continue
		}
		if _, exists := out[id]; exists {
			continue
		}
		title := truncate(strings.TrimSpace(pg.Name), 40)
		summary := previewText(pg.NotesText, pg.Shapes)
		if summary == title {
			summary = ""
		}
		out[id] = jobSlideLabel{SlideID: id, Page: i + 1, Title: title, Summary: summary}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
