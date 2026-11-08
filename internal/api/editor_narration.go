package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/tenant"
)

type revisionVoiceStatus struct {
	RevisionNo   int      `json:"revisionNo"`
	Status       string   `json:"status"`
	PageCount    int      `json:"pageCount"`
	VoicedPages  int      `json:"voicedPages"`
	MissingPages int      `json:"missingPages"`
	StalePages   int      `json:"stalePages"`
	MissingIDs   []string `json:"missingSlideIds,omitempty"`
	StaleIDs     []string `json:"staleSlideIds,omitempty"`
}

type revisionPages struct {
	count int
	ids   map[string]struct{}
}

func editorRevisionVoiceStatus(w http.ResponseWriter, r *http.Request, jobs JobStore, objects objectstore.ObjectStore, scripts narration.Store, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	revs, err := projects.ListSourceRevisions(r.Context(), principal.TenantID, projectID)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	language := requestLanguage(r.Header)
	pagesByRev := make(map[int]revisionPages, len(revs))
	// 讲稿按源版本隔离：逐版本取稿，避免同 slide_id 跨版本串扰。
	currentByRev := make(map[int]map[string]*narration.Revision, len(revs))
	for _, rv := range revs {
		docs, _ := loadProjectDocuments(r.Context(), projects, objects, principal.TenantID, projectID, rv.RevisionNo, rv.RevisionNo)
		ids := map[string]struct{}{}
		count := rv.PageCount
		if doc := docs[rv.RevisionNo]; doc != nil {
			count = len(doc.Pages)
			for _, pg := range doc.Pages {
				if pg != nil && pg.SlideID != "" {
					ids[pg.SlideID] = struct{}{}
				}
			}
		}
		pagesByRev[rv.RevisionNo] = revisionPages{count: count, ids: ids}
		list, _ := scripts.ListByProject(r.Context(), principal.TenantID, projectID, rv.RevisionNo, language)
		m := make(map[string]*narration.Revision, len(list))
		for _, s := range list {
			m[s.SlideID] = s
		}
		currentByRev[rv.RevisionNo] = m
	}
	voicedByRev := map[int]map[string]int64{}
	if jobs != nil {
		narrationJobs, lerr := jobs.ListSucceededByKind(r.Context(), principal.TenantID, projectID, pipeline.KindNarration)
		if lerr != nil {
			writeConnectError(w, connect.NewError(connect.CodeInternal, lerr))
			return
		}
		for _, job := range narrationJobs {
			var snap app.NarrationSnapshot
			if err := json.Unmarshal([]byte(job.InputSnapshot), &snap); err != nil {
				continue
			}
			revNo := snap.RevisionNo
			if revNo == 0 {
				revNo = inferNarrationRevision(snap, revs, pagesByRev)
			}
			if revNo == 0 {
				continue
			}
			m := voicedByRev[revNo]
			if m == nil {
				m = map[string]int64{}
				voicedByRev[revNo] = m
			}
			for _, slide := range snap.Slides {
				if slide.SlideID == "" {
					continue
				}
				if slide.ScriptRevision > m[slide.SlideID] {
					m[slide.SlideID] = slide.ScriptRevision
				}
			}
		}
	}
	out := make([]revisionVoiceStatus, 0, len(revs))
	for _, rv := range revs {
		pages := pagesByRev[rv.RevisionNo]
		voiced := voicedByRev[rv.RevisionNo]
		item := revisionVoiceStatus{RevisionNo: rv.RevisionNo, PageCount: pages.count, Status: "not_voiced"}
		for slideID := range pages.ids {
			scriptRev, ok := voiced[slideID]
			if !ok {
				item.MissingIDs = append(item.MissingIDs, slideID)
				continue
			}
			item.VoicedPages++
			if cur := currentByRev[rv.RevisionNo][slideID]; cur != nil && cur.Revision > scriptRev {
				item.StalePages++
				item.StaleIDs = append(item.StaleIDs, slideID)
			}
		}
		item.MissingPages = len(item.MissingIDs)
		switch {
		case item.VoicedPages == 0:
			item.Status = "not_voiced"
		case item.PageCount == 0 || item.VoicedPages < item.PageCount:
			item.Status = "partial"
		case item.StalePages > 0:
			item.Status = "stale"
		default:
			item.Status = "complete"
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": out})
}

// editorRevisionNarration 读取指定源版本的配音状态（ready/timelineKey/pagePngKeys/revisionNo）。
// 供 PPT 列表页「导出」按钮就地弹 ExportDialog：只取该版本自己的配音任务（按 snapshot.RevisionNo
// 归集，复刻 editorRevisionVoiceStatus 的扫描逻辑），避免 GetNarration 只回“最新成功任务”导致
// 旧版本弹窗无素材。
func editorRevisionNarration(w http.ResponseWriter, r *http.Request, jobs JobStore, objects objectstore.ObjectStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	revNo, err := strconv.Atoi(r.PathValue("rev"))
	if err != nil || revNo <= 0 {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revision_no")))
		return
	}
	ctx := r.Context()
	// 找该修订号匹配的成功配音任务（与 editorRevisionVoiceStatus 相同的按版本归集口径）。
	// SQL 端按 kind 过滤一次取回，避免对全类型成功任务分页扫描后本地再过滤。
	var timelineKey string
	var jobID string
	narrationJobs, lerr := jobs.ListSucceededByKind(ctx, principal.TenantID, projectID, pipeline.KindNarration)
	if lerr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for _, job := range narrationJobs {
		var snap app.NarrationSnapshot
		if err := json.Unmarshal([]byte(job.InputSnapshot), &snap); err != nil {
			continue
		}
		if snap.RevisionNo != revNo {
			continue
		}
		ref, rerr := jobs.StepResultRef(tenant.WithContext(ctx, principal.TenantID), job.ID, "timeline")
		if rerr != nil || ref == "" {
			continue
		}
		timelineKey = ref
		jobID = job.ID
		break
	}
	if timelineKey == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ready": false, "timelineKey": "", "pagePngKeys": []string{}, "revisionNo": revNo})
		return
	}
	pagePngKeys, _ := resolvePagePngKeys(ctx, jobs, objects, principal.TenantID, projectID, timelineKey)
	// 本次任务的音频来源构成（真实合成 / 命中缓存）。按 job 独立存储；旧 job 或未产出时
	// 保持 null，前端据此不谎报——"没有统计"与"统计为 0"必须是两个状态（A26）。
	var synthesis *app.SynthesisStats
	if rc, _, gerr := objects.Get(ctx, app.SynthesisStatsKey(principal.TenantID, projectID, jobID)); gerr == nil {
		var s app.SynthesisStats
		derr := json.NewDecoder(rc).Decode(&s)
		_ = rc.Close()
		if derr == nil {
			synthesis = &s
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ready": true, "timelineKey": timelineKey, "pagePngKeys": pagePngKeys, "revisionNo": revNo, "jobId": jobID,
		"synthesis": synthesis,
	})
}

func inferNarrationRevision(snap app.NarrationSnapshot, revs []*project.SourceRevision, pagesByRev map[int]revisionPages) int {
	if len(snap.Slides) == 0 {
		return 0
	}
	for _, rv := range revs {
		pages := pagesByRev[rv.RevisionNo]
		if len(pages.ids) == 0 {
			continue
		}
		matched := true
		for _, slide := range snap.Slides {
			if _, ok := pages.ids[slide.SlideID]; !ok {
				matched = false
				break
			}
		}
		if matched {
			return rv.RevisionNo
		}
	}
	return 0
}
