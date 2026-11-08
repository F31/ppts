package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
)

// editorDiffRevisions 比较两个源版本的幻灯片差异（V4.0 §7.1 版本管理增强）。
// GET /projects/{pid}/revisions/{revA}/diff/{revB}
// 返回 {added: [{slideId,name}], removed: [...], changed: [{slideId,oldName,newName,oldNotes,newNotes}]}
func editorDiffRevisions(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, objects objectstore.ObjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	revAStr := r.PathValue("revA")
	revBStr := r.PathValue("revB")
	if projectID == "" || revAStr == "" || revBStr == "" {
		http.Error(w, "project_id, revA, revB are required", http.StatusBadRequest)
		return
	}
	revA, err := strconv.Atoi(revAStr)
	if err != nil {
		http.Error(w, "invalid revA", http.StatusBadRequest)
		return
	}
	revB, err := strconv.Atoi(revBStr)
	if err != nil {
		http.Error(w, "invalid revB", http.StatusBadRequest)
		return
	}
	if revA == revB {
		writeJSON(w, http.StatusOK, map[string]any{"added": []map[string]any{}, "removed": []map[string]any{}, "changed": []map[string]any{}})
		return
	}

	docs, err := loadProjectDocuments(r.Context(), projects, objects, principal.TenantID, projectID, revA, revB)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	diff := diffDocuments(docs[revA], docs[revB])
	writeJSON(w, http.StatusOK, diff)
}

// loadProjectDocuments 按 revision_no 顺序读取两个版本的 extracted document.json。
// 返回 map[revisionNo]*project.Document；缺失版本为 nil。
//
// 命中请求级缓存时不再读对象存储：同一请求里 document.json 是只读且不变的，
// 而批处理端点会为每一页问一次同一个版本（见 requestcache.go）。
func loadProjectDocuments(ctx context.Context, projects project.ProjectStore, objects objectstore.ObjectStore, tenantID, projectID string, revA, revB int) (map[int]*project.Document, error) {
	out := make(map[int]*project.Document, 2)
	cache := requestCacheFrom(ctx)
	// 同一个版本只读一次：调用方几乎都传 revA == revB（单页解析、页序、备注），
	// 按 [revA, revB] 逐个取会在没有缓存的时代就白读一遍整份 document.json。
	revs := []int{revA}
	if revB != revA {
		revs = append(revs, revB)
	}
	for _, rev := range revs {
		key := documentCacheKey{tenantID: tenantID, projectID: projectID, revision: rev}
		if cache != nil {
			if doc, ok := cache.documents.load(key); ok {
				out[rev] = doc
				continue
			}
		}
		doc := loadProjectDocument(ctx, projects, objects, tenantID, projectID, rev)
		if cache != nil {
			cache.documents.store(key, doc)
		}
		out[rev] = doc
	}
	return out, nil
}

// loadProjectDocument 读一个源版本的 document.json；任何一步失败都返回 nil，
// 由调用方按「该版本缺失」处理（原实现就是逐段 continue 到 nil）。
func loadProjectDocument(ctx context.Context, projects project.ProjectStore, objects objectstore.ObjectStore, tenantID, projectID string, rev int) *project.Document {
	if _, err := projects.GetSourceRevision(ctx, tenantID, projectID, rev); err != nil {
		return nil
	}
	key := objectstore.DocumentKey(tenantID, projectID, srcRevString(rev))
	rc, _, err := objects.Get(ctx, key)
	if err != nil {
		return nil
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return nil
	}
	var doc project.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	return &doc
}

// diffDocuments 比较两个 Document，返回页级差异。
// oldDoc=revA, newDoc=revB：added=新增页、removed=删除页、changed=标题/备注变更页。
func diffDocuments(oldDoc, newDoc *project.Document) map[string]any {
	added := []map[string]any{}
	removed := []map[string]any{}
	changed := []map[string]any{}

	oldIndex := make(map[string]*project.Page, len(oldDoc.Pages))
	newIndex := make(map[string]*project.Page, len(newDoc.Pages))
	for _, pg := range oldDoc.Pages {
		oldIndex[pg.SlideID] = pg
	}
	for _, pg := range newDoc.Pages {
		newIndex[pg.SlideID] = pg
	}

	// removed: 在旧版有、新版无
	for id, oldPg := range oldIndex {
		if _, ok := newIndex[id]; !ok {
			removed = append(removed, map[string]any{"slideId": id, "name": oldPg.Name, "pageCount": oldDoc.Features.PageCount})
		}
	}
	// added: 在新版有、旧版无
	for id, newPg := range newIndex {
		if _, ok := oldIndex[id]; !ok {
			added = append(added, map[string]any{"slideId": id, "name": newPg.Name, "pageCount": newDoc.Features.PageCount})
		}
	}
	// changed: 同 slideId 但 title 或 notes 变化
	for id, newPg := range newIndex {
		oldPg, ok := oldIndex[id]
		if !ok {
			continue
		}
		if oldPg.Name != newPg.Name || oldPg.NotesText != newPg.NotesText {
			changed = append(changed, map[string]any{
				"slideId":   id,
				"oldName":   oldPg.Name,
				"newName":   newPg.Name,
				"oldNotes":  truncate(oldPg.NotesText, 80),
				"newNotes":  truncate(newPg.NotesText, 80),
				"pageCount": newDoc.Features.PageCount,
			})
		}
	}
	return map[string]any{
		"added":   added,
		"removed": removed,
		"changed": changed,
	}
}

// registerDiffRevisionRoutes 挂载源版本 diff 端点。
func registerDiffRevisionRoutes(mux *http.ServeMux, projects project.ProjectStore, objects objectstore.ObjectStore, members membership.Reader, recorder audit.Recorder, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /projects/{pid}/revisions/{revA}/diff/{revB}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorDiffRevisions(w, r, projects, objects, members, recorder)
	})))
}
