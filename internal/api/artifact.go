package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
)

// registerArtifactRoutes 挂载成品列表原生 HTTP 端点（buf/protoc 不可用，不新增 Connect RPC）：
//   - GET /projects/{pid}/artifacts：按项目列出全部产物（前端再按 snapshotHash 分组），供成品与版本页展示与下载（B3-M1）；
//   - GET /artifacts：跨项目成品库（owner 级），供 B5-M2 成品库页展示。
//
// 注意：/artifacts 此前 handler（globalArtifacts）已写但漏挂路由，请求落到 SPA 兜底（server.go 的 "/"）
// 拿到 index.html，前端 JSON 解析报 `Unexpected token '<', "<!doctype"`。新增任何 REST handler
// 必须同步在注册表挂载（见项目风险 R-10）。
func registerArtifactRoutes(mux *http.ServeMux, artifacts artifact.Store, members membership.Store, projects project.ProjectStore, recorder audit.Recorder, jobs JobCreator, objects objectstore.ObjectStore, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /projects/{pid}/artifacts", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicProjectArtifacts(w, r, artifacts, members, projects, recorder)
	})))
	mux.Handle("GET /artifacts", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		globalArtifacts(w, r, artifacts, members)
	})))
	// 成品内嵌预览：以该成品绑定的时间轴构建播放清单（timeline/字幕/音频/页图签名 URL）。
	// 与下载同源（同一 snapshot），不会出现"预览是当前版本、下载是旧版本"的错位。
	mux.Handle("GET /artifacts/{id}/manifest", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		artifactManifest(w, r, artifacts, members, jobs, objects)
	})))
	// 成品库删除：owner 级（与 GET /artifacts 跨项目曝光同门禁）。
	mux.Handle("DELETE /artifacts/{id}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deleteArtifact(w, r, artifacts, members, objects, recorder)
	})))
}

// artifactManifest 为成品库内嵌预览构建播放清单（B5-M2 播放/预览）。
// 权限与下载一致（viewer）；时间轴为空的历史成品返回 404，由前端降级为"仅下载"。
func artifactManifest(w http.ResponseWriter, r *http.Request, artifacts artifact.Store, members membership.Store, jobs JobCreator, objects objectstore.ObjectStore) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleViewer); err != nil {
		writeConnectError(w, err)
		return
	}
	a, err := artifacts.Get(r.Context(), principal.TenantID, r.PathValue("id"))
	if errors.Is(err, artifact.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	if a.TimelineKey == "" {
		// 0040 之前导出的历史成品没有时间轴绑定。不回退"项目最新讲解"——那可能播放
		// 与下载文件不同源的另一版内容，宁可明说不可预览。
		http.Error(w, "artifact has no bound timeline", http.StatusNotFound)
		return
	}
	// 页面 PNG 为可选项：取不到时优雅降级为音频+字幕（与公开区清单同一策略）。
	// 用**时间轴自带的项目**而非成品行的 project 解析页图：绑定时间轴可能来自较早的
	// 快照/另一项目（历史脏数据），按时间轴项目解析才能与时间轴的 slideId 对齐。
	timelineProject := a.ProjectID
	if key, perr := objectstore.Parse(a.TimelineKey); perr == nil && key.ProjectID != "" {
		timelineProject = key.ProjectID
	}
	pagePngKeys, _ := resolvePagePngKeys(r.Context(), jobs, objects, principal.TenantID, timelineProject, a.TimelineKey)
	bundle, _, err := loadBundle(r.Context(), objects, principal.TenantID, a.TimelineKey)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	parser, _ := objects.(signedURLParser)
	ttl := time.Hour
	resources, err := signManifestResources(r.Context(), objects, parser, principal.TenantID, a.TimelineKey, bundle, pagePngKeys, ttl)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	out := make([]publicManifestResource, 0, len(resources))
	for _, res := range resources {
		out = append(out, publicManifestResource{
			Type:        res.Type.String(),
			Key:         res.Key,
			SignedUrl:   res.SignedUrl,
			ContentType: res.ContentType,
			SizeBytes:   res.SizeBytes,
			ContentHash: res.ContentHash,
			SlideId:     res.SlideId,
			SegmentId:   res.SegmentId,
		})
	}
	timelineJSON, err := json.Marshal(bundle.Timeline)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	writeJSON(w, http.StatusOK, publicManifest{
		ProjectId:     a.ProjectID,
		TimelineKey:   a.TimelineKey,
		TimelineJson:  string(timelineJSON),
		Resources:     out,
		ExpiresAtUnix: time.Now().Add(ttl).Unix(),
	})
}

// deleteArtifact 删除成品库中的一条成品记录（owner 级，与 GET /artifacts 跨项目曝光同门禁）。
// 先删记录再尽力清理对象：记录删除失败必须上报；对象为内容寻址、可能被其它成品复用，
// 清理失败不阻断删除语义。删除成功后写入审计事件。
func deleteArtifact(w http.ResponseWriter, r *http.Request, artifacts artifact.Store, members membership.Store, objects objectstore.ObjectStore, recorder audit.Recorder) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleOwner); err != nil {
		writeConnectError(w, err)
		return
	}
	id := r.PathValue("id")
	a, err := artifacts.Get(r.Context(), principal.TenantID, id)
	if errors.Is(err, artifact.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	if err := artifacts.Delete(r.Context(), principal.TenantID, id); err != nil {
		writeConnectError(w, artifactError(err))
		return
	}
	if a.ObjectKey != "" {
		if key, perr := objectstore.Parse(a.ObjectKey); perr == nil && key.EnsureTenant(principal.TenantID) == nil {
			_ = objects.Delete(r.Context(), key)
		}
	}
	if recorder != nil {
		_ = recorder.Record(r.Context(), audit.Event{
			TenantID:     principal.TenantID,
			ActorUser:    principal.UserID,
			Action:       "artifact.delete",
			ResourceType: "artifact",
			ResourceID:   id,
			Metadata:     map[string]any{"project_id": a.ProjectID, "format": string(a.Format)},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

func publicProjectArtifacts(w http.ResponseWriter, r *http.Request, artifacts artifact.Store, members membership.Store, projects project.ProjectStore, recorder audit.Recorder) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	pid := r.PathValue("pid")
	list, err := artifacts.ListByProject(r.Context(), principal.TenantID, pid)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, map[string]any{
			"id":           a.ID,
			"snapshotHash": a.SnapshotHash,
			"format":       string(a.Format),
			"sizeBytes":    a.SizeBytes,
			// durationMs = 0 表示未知/未记录（0027 之前落库的历史行），前端显示「—」不伪造。
			"durationMs":   a.DurationMS,
			"createdAt":    a.CreatedAt.Format(time.RFC3339),
			"downloadable": a.ObjectKey != "",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": out})
}

// globalArtifacts 返回当前租户全部项目的成品（B5-M2 跨项目成品库）。
// 权限：owner（跨项目曝光，高于单项目 artifact.list 的 editor）；按创建时间倒序。
func globalArtifacts(w http.ResponseWriter, r *http.Request, artifacts artifact.Store, members membership.Store) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleOwner); err != nil {
		writeConnectError(w, err)
		return
	}
	list, err := artifacts.ListAll(r.Context(), principal.TenantID)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, map[string]any{
			"id":           a.ID,
			"projectId":    a.ProjectID,
			"projectName":  a.ProjectName,
			"snapshotHash": a.SnapshotHash,
			"format":       string(a.Format),
			"sizeBytes":    a.SizeBytes,
			"durationMs":   a.DurationMS,
			"createdAt":    a.CreatedAt.Format(time.RFC3339),
			"downloadable": a.ObjectKey != "",
			// previewable=false（0040 之前的历史行）时前端隐藏"预览"按钮，降级为仅下载。
			"previewable": a.TimelineKey != "",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": out})
}
