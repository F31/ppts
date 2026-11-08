package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/tenant"
)

type editorSlideRenderItem struct {
	SlideID string `json:"slideId"`
	URL     string `json:"url"`
}

// fallbackPageRenderer 在 job_steps 没有成功步骤引用时，按约定对象键直读页面清单，
// 只取 renderer 字段用于诊断（如 "unavailable"）。读不到返回空串，调用方维持空列表。
func fallbackPageRenderer(ctx context.Context, r *http.Request, objects objectstore.ObjectStore, projects project.ProjectStore, tenantID, userID, projectID string) string {
	revNo := 0
	if revStr := r.URL.Query().Get("revision_no"); revStr != "" {
		n, err := strconv.Atoi(revStr)
		if err != nil || n <= 0 {
			return ""
		}
		revNo = n
	} else {
		row, err := projects.GetProject(ctx, tenantID, userID, projectID)
		if err != nil || row == nil {
			return ""
		}
		revNo = int(row.CurrentRevision)
	}
	if revNo <= 0 {
		return ""
	}
	key := objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: projectID, Revision: srcRevString(revNo),
		AssetType: "render", AssetID: "pages", Ext: "json",
	}
	manifest, err := loadPageManifest(ctx, objects, tenantID, key.String())
	if err != nil {
		return ""
	}
	return manifest.Renderer
}

// writePageRenderFallback 在拿不到成功步骤引用时，把清单里的能力缺失原因透出。
// 返回 false 表示没有可透出的原因，调用方维持原来的空列表响应。
func writePageRenderFallback(w http.ResponseWriter, r *http.Request, objects objectstore.ObjectStore, projects project.ProjectStore, tenantID, userID, projectID string) bool {
	renderer := fallbackPageRenderer(r.Context(), r, objects, projects, tenantID, userID, projectID)
	if renderer == "" {
		return false
	}
	writeJSON(w, http.StatusOK, map[string]any{"slides": []editorSlideRenderItem{}, "renderer": renderer})
	return true
}

// editorSlideRender 读取最近一次成功解析任务的页面清单（PageManifest），为每页渲染 PNG 签发短期匿名可读 URL。
// 解析未完成、页面清单缺失或某页渲染图不存在时，对应项跳过；整体缺失时返回空列表，前端优雅降级为序号/标题缩略图。
func editorSlideRender(w http.ResponseWriter, r *http.Request, jobs JobStore, objects objectstore.ObjectStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	ctx := r.Context()
	job, err := jobs.LatestSucceededJob(ctx, principal.TenantID, projectID, string(pipeline.KindParse))
	if errors.Is(err, pipeline.ErrNoSucceededJob) {
		if !writePageRenderFallback(w, r, objects, projects, principal.TenantID, principal.UserID, projectID) {
			writeJSON(w, http.StatusOK, map[string]any{"slides": []editorSlideRenderItem{}})
		}
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if revStr := r.URL.Query().Get("revision_no"); revStr != "" {
		revNo, perr := strconv.Atoi(revStr)
		if perr != nil {
			writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revision_no")))
			return
		}
		matched, merr := parseJobForRevision(ctx, jobs, principal.TenantID, projectID, revNo)
		if errors.Is(merr, pipeline.ErrNoSucceededJob) {
			if !writePageRenderFallback(w, r, objects, projects, principal.TenantID, principal.UserID, projectID) {
				writeJSON(w, http.StatusOK, map[string]any{"slides": []editorSlideRenderItem{}})
			}
			return
		}
		if merr != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		job = matched
	}
	ref, err := jobs.StepResultRef(tenant.WithContext(ctx, principal.TenantID), job.ID, "pages")
	if err != nil || ref == "" {
		// 渲染器缺失时 app.publishPageManifest 会写一份 renderer="unavailable" 的清单，
		// 但把 pages 步骤标为 failed；StepResultRef 只查 state='success'，于是这里拿不到 ref，
		// 接口退化成 {"slides":[]}，前端无法区分「能力缺失」与「尚未渲染」，主区域还会
		// 显示「就绪后展示」误导用户继续等。回退按约定键直读清单，把原因透出去。
		if !writePageRenderFallback(w, r, objects, projects, principal.TenantID, principal.UserID, projectID) {
			writeJSON(w, http.StatusOK, map[string]any{"slides": []editorSlideRenderItem{}})
		}
		return
	}
	manifest, err := loadPageManifest(ctx, objects, principal.TenantID, ref)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	parser, _ := objects.(signedURLParser)
	ttl := 15 * time.Minute
	out := make([]editorSlideRenderItem, 0, len(manifest.Pages))
	for _, pg := range manifest.Pages {
		if pg.SlideID == "" || pg.Key == "" {
			continue
		}
		key, perr := objectstore.Parse(pg.Key)
		if perr != nil {
			continue
		}
		if perr = key.EnsureTenant(principal.TenantID); perr != nil {
			continue
		}
		signed, serr := objects.SignedURL(ctx, key, objectstore.OpRead, ttl)
		if serr != nil {
			continue
		}
		url := signed
		if parser != nil {
			url = rewriteLocalSignedURL(parser, signed)
		}
		out = append(out, editorSlideRenderItem{SlideID: pg.SlideID, URL: url})
	}
	// renderer 为空串=正常（有页面图）；"unavailable"=渲染器缺失，此时 slides 必为空。
	// 不把它隐含在"slides 为空"里：那样前端无法区分"能力缺失"与"暂未渲染"。
	renderer := ""
	if manifest.Renderer != "" {
		renderer = manifest.Renderer
	}
	writeJSON(w, http.StatusOK, map[string]any{"slides": out, "renderer": renderer})
}

// projectParseStatus 暴露项目当前版本的解析状态，供前端决定能否拉取页面列表，并区分
// 「解析中」与「解析失败」。前端先探测本端点，只有 ready 时才调用 GetSlides：后端用
// CodeNotFound（HTTP 404）表达「当前版本解析文档缺失」，未就绪时反复调用会在浏览器控制台
// 刷出并无实际故障的 404，也会把正常业务态误读成接口故障。
//
// 状态语义：
//
//	ready   —— 当前版本的解析文档对象已存在，GetSlides 可正常返回页面。
//	none    —— 该项目没有任何 parse 任务（从未导入，或导入未触发解析）。
//	running —— 最近一次 parse 任务仍在进行，或虽有成功记录但当前版本产物缺失且可能有新导入在进行。
//	failed  —— 最近一次 parse 任务终态失败（failed/cancel*/unknown），或成功记录存在但当前版本产物缺失（数据不一致，提示重新导入）。
//	stalled —— 排队任务从未被 worker 领取且已超时，典型成因是只启动了 server、没启动 worker。
//
// parseQueueStallAfter 是「从未被领取」的排队判定阈值。本地只起 server 不起 worker 时，
// parse 任务会永远停在 queued、attempt=0、lease_owner 为空，界面却一直显示「文档解析中」——
// 这既不是解析慢，也不是解析失败。只对这类任务快速判定，已领取（有 attempt/lease）或正在
// 执行的任务不受影响，避免把大文件正常解析误报成故障。
const parseQueueStallAfter = 2 * time.Minute

func projectParseStatus(w http.ResponseWriter, r *http.Request, jobs JobStore, objects objectstore.ObjectStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	ctx := r.Context()
	// 与 GetSlides 用同一个 docKey 判定产物是否就绪：就绪时前端才会去调 GetSlides，
	// 因此不会出现「探测说 ready、实际却 404」的竞态。objects 为 nil 时（部分单测夹具）
	// 跳过该判定，退回按任务状态推断的旧路径。
	if objects != nil {
		if projectRow, err := projects.GetProject(ctx, principal.TenantID, principal.UserID, projectID); err == nil && projectRow != nil {
			docKey := objectstore.DocumentKey(principal.TenantID, projectID, srcRevString(projectRow.CurrentRevision))
			if rc, _, err := objects.Get(ctx, docKey); err == nil {
				rc.Close()
				writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "message": "解析完成"})
				return
			}
		}
	}
	// 取该项目最近的若干任务，筛选 parse 类，按创建时间取最新（List 返回已按 created_at DESC，这里再防御性排序）。
	rows, _, err := jobs.List(ctx, principal.TenantID, projectID, "", "", 50)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var latest *pipeline.Job
	for _, j := range rows {
		if j.Kind != pipeline.KindParse {
			continue
		}
		if latest == nil || j.CreatedAt.After(latest.CreatedAt) {
			latest = j
		}
	}
	if latest == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "none", "message": "尚未导入文档或解析尚未开始"})
		return
	}
	if latest.State == pipeline.StateQueued && latest.Attempt == 0 && latest.LeaseOwner == "" &&
		time.Since(latest.CreatedAt) > parseQueueStallAfter {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":        "stalled",
			"message":       "解析任务长时间未被处理，请确认任务执行器（worker）已启动后重新导入",
			"updatedAtUnix": latest.UpdatedAt.Unix(),
		})
		return
	}
	resp := map[string]any{"updatedAtUnix": latest.UpdatedAt.Unix()}
	switch latest.State {
	case pipeline.StateQueued, pipeline.StateRunning, pipeline.StateRetryWait, pipeline.StateWaitingReview:
		resp["status"] = "running"
		resp["message"] = "文档解析中，请稍候"
	case pipeline.StateSucceeded:
		// 已有成功解析记录却取不到当前版本页面：通常是一次新导入正在解析，乐观视为 running 继续等待；
		// 若确属数据不一致（成功记录存在但产物丢失），不会再出现活跃任务，前端重试上限后停在空态，这里也给可操作提示。
		resp["status"] = "running"
		resp["message"] = "文档解析中，请稍候"
	default: // failed / cancel_requested / canceled / unknown_provider_result
		resp["status"] = "failed"
		if latest.LastError != nil && latest.LastError.Message != "" {
			resp["message"] = latest.LastError.Message
		} else {
			resp["message"] = "文档解析失败，请重新导入"
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func parseJobForRevision(ctx context.Context, jobs JobStore, tenantID, projectID string, revisionNo int) (*pipeline.Job, error) {
	parseJobs, err := jobs.ListSucceededByKind(ctx, tenantID, projectID, pipeline.KindParse)
	if err != nil {
		return nil, err
	}
	for _, job := range parseJobs {
		var snap app.ParseSnapshot
		if err := json.Unmarshal([]byte(job.InputSnapshot), &snap); err == nil && snap.RevisionNo == revisionNo {
			return job, nil
		}
	}
	return nil, pipeline.ErrNoSucceededJob
}
