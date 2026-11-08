package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
)

// importReportRevision 是随报告一起返回的版本元信息。
// 报告本身只描述"解析成了什么"，版本号/文件名/上传时间来自 source_revisions，
// 不混进 project.ImportReport（那是可缓存、可复算的纯解析产物）。
type importReportRevision struct {
	RevisionNo  int    `json:"revisionNo"`
	DisplayName string `json:"displayName"`
	// PageCount 是解析回写到 source_revisions 的页数。报告里的 pageCount 来自解析产物本身，
	// 两者不一致说明回写没落上（历史上出现过），所以两个都返回，让界面能如实提示。
	PageCount     int       `json:"pageCount"`
	ParserVersion string    `json:"parserVersion"`
	CreatedAt     time.Time `json:"createdAt"`
}

type importReportResponse struct {
	Revision importReportRevision  `json:"revision"`
	Report   *project.ImportReport `json:"report"`
}

// editorImportReport 返回某个源版本的导入报告（GET /projects/{pid}/revisions/{rev}/import-report）。
//
// 报告读的是解析产物（document/extracted.json），不是源文件重新解析：
// 用户看到必须是与当前讲稿/配音同源的那一次解析结果，重算会得到另一份"看起来更好"的报告。
func editorImportReport(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, objects objectstore.ObjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID, ok := requireProjectAccess(w, r, projects, members, recorder)
	if !ok {
		return
	}
	revNo, err := strconv.Atoi(r.PathValue("rev"))
	if err != nil || revNo <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_revision", "error": "invalid revision number"})
		return
	}
	ctx := r.Context()
	rev, err := projects.GetSourceRevision(ctx, principal.TenantID, projectID, revNo)
	if err != nil {
		if errors.Is(err, project.ErrSourceRevisionNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"code": "revision_not_found", "error": "source revision not found"})
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	key := objectstore.DocumentKey(principal.TenantID, projectID, srcRevString(revNo))
	rc, _, err := objects.Get(ctx, key)
	if err != nil {
		if errors.Is(err, objectstore.ErrObjectNotFound) {
			// 解析还没产出（任务排队/进行中）或产物被清理。这与"版本不存在"是两件事：
			// 用户该等一会儿再试，而不是以为版本被删了。
			writeJSON(w, http.StatusConflict, map[string]string{"code": "report_not_ready", "error": "import report is not available yet"})
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	data, rerr := io.ReadAll(rc)
	_ = rc.Close()
	if rerr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var doc project.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		// 产物存在但读不懂：这是真的坏了，不能说"稍后再试"。
		writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "report_corrupt", "error": "stored extracted document is not readable"})
		return
	}
	rep := project.BuildImportReport(&doc)
	if rep == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "report_corrupt", "error": "stored extracted document is empty"})
		return
	}
	displayName := rev.DisplayName
	if displayName == "" {
		displayName = rev.Filename
	}
	writeJSON(w, http.StatusOK, importReportResponse{
		Revision: importReportRevision{
			RevisionNo:    rev.RevisionNo,
			DisplayName:   displayName,
			PageCount:     rev.PageCount,
			ParserVersion: rev.ParserVersion,
			CreatedAt:     rev.CreatedAt,
		},
		Report: rep,
	})
}
