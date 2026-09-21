package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/gateway"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/narration"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/tenant"
)

// registerEditorRoutes 挂载核心创作辅助的 HTTP 端点（B2 M2 真实渲染缩略图；B2 M3 ⑥ 无备注页来源）。
// GET /projects/{pid}/slides/render 返回每页渲染 PNG 的短期签名可读 URL，按 slideId 对齐，供编辑器缩略图与 PPT 预览。
// PUT /projects/{pid}/slides/{sid}/source + GET /projects/{pid}/slides/sources 管理"无备注页讲稿来源"选择。
// 均受 auth 中间件保护（仅项目所属租户成员可访问）。
func registerEditorRoutes(mux *http.ServeMux, jobs JobStore, objects objectstore.ObjectStore, srcStore app.ScriptSourceStore, voiceStore app.VoiceSettingsStore, gatewayStore gateway.StoreResolver, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /projects/{pid}/slides/render", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorSlideRender(w, r, jobs, objects, projects, members, recorder)
	})))
	// M3 ⑥：无备注页讲稿来源选择。
	mux.Handle("PUT /projects/{pid}/slides/{sid}/source", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorSetSlideSource(w, r, srcStore, projects, members, recorder)
	})))
	mux.Handle("GET /projects/{pid}/slides/sources", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorListSlideSources(w, r, srcStore, projects, members, recorder)
	})))
	// 重新生成讲稿（用户显式覆盖已有讲稿）：原生 HTTP，避免改 proto。
	mux.Handle("POST /projects/{pid}/script-draft", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorRegenerateScriptDraft(w, r, jobs, srcStore, projects, members, recorder)
	})))
	// 语音属性：项目级语音模型 / 音色 / 语速的读写。
	mux.Handle("GET /projects/{pid}/voice-settings", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorGetVoiceSettings(w, r, voiceStore, projects, members, recorder)
	})))
	mux.Handle("PUT /projects/{pid}/voice-settings", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorSaveVoiceSettings(w, r, voiceStore, projects, members, recorder)
	})))
	// 可选语音模型（按 TTS 网关配置）+ 每个模型配置的音色。
	mux.Handle("GET /projects/{pid}/voice-models", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorVoiceModels(w, r, gatewayStore, projects, members, recorder)
	})))
}

type projectVoiceSettingsPayload struct {
	Model       string `json:"model"`
	Voice       string `json:"voice"`
	RatePercent int    `json:"ratePercent"`
}

// editorGetVoiceSettings 读取项目语音属性；未保存过时返回缺省（rate=100）。
func editorGetVoiceSettings(w http.ResponseWriter, r *http.Request, voiceStore app.VoiceSettingsStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	if voiceStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_disabled", "message": "voice settings store not configured"})
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID, ok := requireProjectAccess(w, r, projects, members, recorder)
	if !ok {
		return
	}
	settings, err := voiceStore.Get(r.Context(), principal.TenantID, projectID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, projectVoiceSettingsPayload{Model: settings.Model, Voice: settings.Voice, RatePercent: settings.RatePercent})
}

// editorSaveVoiceSettings 保存项目语音属性（语音模型 / 音色 / 语速）。
func editorSaveVoiceSettings(w http.ResponseWriter, r *http.Request, voiceStore app.VoiceSettingsStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	if voiceStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_disabled", "message": "voice settings store not configured"})
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "permission_denied", "message": "requires editor role"})
		return
	}
	projectID, ok := requireProjectAccess(w, r, projects, members, recorder)
	if !ok {
		return
	}
	var body projectVoiceSettingsPayload
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_argument", "message": "invalid JSON body"})
		return
	}
	settings := app.VoiceSettings{
		Model: strings.TrimSpace(body.Model), Voice: strings.TrimSpace(body.Voice), RatePercent: body.RatePercent,
	}
	if settings.RatePercent == 0 {
		settings.RatePercent = 100
	}
	if settings.RatePercent < 50 || settings.RatePercent > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_argument", "message": "rate_percent must be between 50 and 200"})
		return
	}
	if err := voiceStore.Save(r.Context(), principal.TenantID, projectID, settings); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, projectVoiceSettingsPayload{Model: settings.Model, Voice: settings.Voice, RatePercent: settings.RatePercent})
}

type voiceModelItem struct {
	Name      string   `json:"name"`
	Model     string   `json:"model"`
	Voices    []string `json:"voices"`
	IsDefault bool     `json:"isDefault"`
}

// editorVoiceModels 列出本租户已启用的 TTS 网关（语音模型）及其配置的音色。
// 音色来自网关配置的 voice 字段（多个逗号分隔）；无可用网关时返回空列表，前端据此降级。
func editorVoiceModels(w http.ResponseWriter, r *http.Request, gatewayStore gateway.StoreResolver, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	if gatewayStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_disabled", "message": "model gateway disabled"})
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	gws, err := gatewayStore.List(r.Context(), principal.TenantID, gateway.KindTTS)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]voiceModelItem, 0, len(gws))
	for _, gw := range gws {
		if gw == nil || !gw.Enabled {
			continue
		}
		voices := make([]string, 0, 4)
		seen := map[string]struct{}{}
		for _, raw := range strings.Split(gw.Voice, ",") {
			v := strings.TrimSpace(raw)
			if v == "" {
				continue
			}
			if _, dup := seen[v]; dup {
				continue
			}
			seen[v] = struct{}{}
			voices = append(voices, v)
		}
		items = append(items, voiceModelItem{Name: gw.Name, Model: gw.Model, Voices: voices, IsDefault: gw.IsDefault})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": items})
}

// editorRegenerateScriptDraft 以 overwrite=true 入队 script_draft 任务：覆盖已有讲稿。
// body: {slideIds: string[], mode: "SCRIPT_MODE_ORIGINAL|SCRIPT_MODE_POLISH|SCRIPT_MODE_AI_GENERATED"}。
func editorRegenerateScriptDraft(w http.ResponseWriter, r *http.Request, jobs JobStore, srcStore app.ScriptSourceStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "permission_denied", "message": "requires editor role"})
		return
	}
	projectID, ok := requireProjectAccess(w, r, projects, members, recorder)
	if !ok {
		return
	}
	var body struct {
		SlideIDs []string `json:"slideIds"`
		Mode     string   `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_argument", "message": "invalid JSON body"})
		return
	}
	mode := scriptModeFromString(body.Mode)
	snap := app.ScriptDraftSnapshot{
		ProjectID: projectID, Language: requestLanguage(r.Header), Mode: string(mode), Overwrite: true,
	}
	if srcStore != nil {
		if choices, lerr := srcStore.List(r.Context(), principal.TenantID, projectID); lerr == nil && len(choices) > 0 {
			sources := make(map[string]string, len(choices))
			customs := make(map[string]string, len(choices))
			for slideID, choice := range choices {
				sources[slideID] = string(choice.Kind)
				if choice.Kind == app.ScriptSourceCustom {
					customs[slideID] = choice.CustomText
				}
			}
			snap.Sources = sources
			snap.CustomSources = customs
		}
	}
	seen := make(map[string]struct{}, len(body.SlideIDs))
	for _, raw := range body.SlideIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		snap.SlideIDs = append(snap.SlideIDs, id)
	}
	snapBytes, err := json.Marshal(snap)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	idem := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idem == "" {
		idem = "regen-script:" + projectID + ":" + string(mode) + ":" + strings.Join(snap.SlideIDs, ",") + ":" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	job, err := jobs.Create(r.Context(), principal.TenantID, projectID, string(pipeline.KindScriptDraft), idem, string(snapBytes), time.Time{})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobId": job.ID})
}

// scriptModeFromString 兼容 proto 枚举名与领域字符串（original/polish/ai_generated）。
func scriptModeFromString(raw string) narration.ScriptMode {
	switch strings.TrimSpace(raw) {
	case "SCRIPT_MODE_POLISH", string(narration.ModePolish):
		return narration.ModePolish
	case "SCRIPT_MODE_AI_GENERATED", string(narration.ModeAIGenerated):
		return narration.ModeAIGenerated
	default:
		return narration.ModeOriginal
	}
}

type editorSlideRenderItem struct {
	SlideID string `json:"slideId"`
	URL     string `json:"url"`
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
		writeJSON(w, http.StatusOK, map[string]any{"slides": []editorSlideRenderItem{}})
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ref, err := jobs.StepResultRef(tenant.WithContext(ctx, principal.TenantID), job.ID, "pages")
	if err != nil || ref == "" {
		writeJSON(w, http.StatusOK, map[string]any{"slides": []editorSlideRenderItem{}})
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
	writeJSON(w, http.StatusOK, map[string]any{"slides": out})
}

type editorSlideSourceItem struct {
	SlideID    string `json:"slideId"`
	Source     string `json:"source"`
	CustomText string `json:"customText"`
}

// editorSetSlideSource 持久化单页讲稿来源选择（M3 ⑥）。body: {source, customText?}。
// source ∈ layout/title/body/notes/custom；custom 时 customText 必填且非空。
func editorSetSlideSource(w http.ResponseWriter, r *http.Request, srcStore app.ScriptSourceStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	if srcStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_disabled", "message": "slide script source store not configured"})
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	slideID := r.PathValue("sid")
	if projectID == "" || slideID == "" {
		http.Error(w, "project_id and slide_id are required", http.StatusBadRequest)
		return
	}
	var body struct {
		Source     string `json:"source"`
		CustomText string `json:"customText"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	kind := app.ScriptSourceKind(body.Source)
	if !app.ValidScriptSourceKinds[kind] {
		http.Error(w, "invalid source kind", http.StatusBadRequest)
		return
	}
	if kind == app.ScriptSourceCustom && strings.TrimSpace(body.CustomText) == "" {
		http.Error(w, "custom source requires customText", http.StatusBadRequest)
		return
	}
	if err := srcStore.Set(tenant.WithContext(r.Context(), principal.TenantID), principal.TenantID, projectID, slideID, kind, body.CustomText); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, editorSlideSourceItem{SlideID: slideID, Source: body.Source, CustomText: body.CustomText})
}

// editorListSlideSources 返回项目内所有页的讲稿来源选择（M3 ⑥）。
func editorListSlideSources(w http.ResponseWriter, r *http.Request, srcStore app.ScriptSourceStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	if srcStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"code": "feature_disabled", "message": "slide script source store not configured"})
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	if projectID == "" {
		http.Error(w, "project_id is required", http.StatusBadRequest)
		return
	}
	choices, err := srcStore.List(tenant.WithContext(r.Context(), principal.TenantID), principal.TenantID, projectID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	items := make([]editorSlideSourceItem, 0, len(choices))
	for _, choice := range choices {
		items = append(items, editorSlideSourceItem{SlideID: choice.SlideID, Source: string(choice.Kind), CustomText: choice.CustomText})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": items})
}

// registerRevisionRoutes 挂载源版本历史只读端点（buf/protoc 不可用，不新增 Connect RPC）：
//   - GET    /projects/{pid}/revisions                 返回 current_revision 与未软删版本列表（倒序）。
//   - PATCH  /projects/{pid}/revisions/{revisionNo}    更新 PPT 显示名。
//   - DELETE /projects/{pid}/revisions/{revisionNo}    软删指定版本（禁止删除当前生效版本）。
//   - GET    /projects/{pid}/slides/{sid}/notes        读取单页备注。
//   - PATCH  /projects/{pid}/slides/{sid}/notes        保存单页备注。
func registerRevisionRoutes(mux *http.ServeMux, projects project.ProjectStore, members membership.Reader, notesStore project.SlideNotesStore, objects objectstore.ObjectStore, recorder audit.Recorder, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /projects/{pid}/revisions", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorListRevisions(w, r, projects, members, recorder)
	})))
	mux.Handle("PATCH /projects/{pid}/revisions/{revisionNo}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorUpdateRevisionDisplayName(w, r, projects, members, recorder)
	})))
	mux.Handle("DELETE /projects/{pid}/revisions/{revisionNo}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editorDeleteRevision(w, r, projects, members, recorder)
	})))
	if notesStore != nil {
		mux.Handle("GET /projects/{pid}/slides/{sid}/notes", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			editorGetSlideNotes(w, r, notesStore, objects)
		})))
		mux.Handle("PATCH /projects/{pid}/slides/{sid}/notes", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			editorSetSlideNotes(w, r, notesStore, projects, members, recorder)
		})))
	}
}

// editorListRevisions 返回项目源版本历史。current_revision 来自 projects 行（即"当前生效版本"）；
// 版本列表排除 source_deleted_at 非空的软删版本（保留不可变版本行用于追溯，但不可预览）。
func editorListRevisions(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	userID, _ := projectAccessUser(r.Context(), members)
	proj, err := projects.GetProject(r.Context(), principal.TenantID, userID, projectID)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeNotFound, err))
		return
	}
	revs, err := projects.ListSourceRevisions(r.Context(), principal.TenantID, projectID)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	out := make([]map[string]any, 0, len(revs))
	for _, rv := range revs {
		out = append(out, map[string]any{
			"revision_no":    rv.RevisionNo,
			"created_at":     rv.CreatedAt.Format(time.RFC3339),
			"page_count":     rv.PageCount,
			"parser_version": rv.ParserVersion,
			"object_key":     rv.ObjectKey,
			"display_name":   rv.DisplayName,
			"is_current":     rv.RevisionNo == proj.CurrentRevision,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"current_revision": proj.CurrentRevision, "revisions": out})
}

// editorUpdateRevisionDisplayName 更新 PPT 展示名（EDITOR+）。
func editorUpdateRevisionDisplayName(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	projectID := r.PathValue("pid")
	revStr := r.PathValue("revisionNo")
	if projectID == "" || revStr == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("pid and revisionNo required")))
		return
	}
	revNo, err := strconv.Atoi(revStr)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revisionNo")))
		return
	}
	var body struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, err))
		return
	}
	if err := projects.UpdateSourceRevisionDisplayName(r.Context(), principal.TenantID, projectID, revNo, body.DisplayName); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// editorDeleteRevision 软删指定源版本（禁止删除当前生效版本）。
func editorDeleteRevision(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	projectID := r.PathValue("pid")
	revStr := r.PathValue("revisionNo")
	if projectID == "" || revStr == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("pid and revisionNo required")))
		return
	}
	revNo, err := strconv.Atoi(revStr)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revisionNo")))
		return
	}
	if err := projects.DeleteSourceRevision(r.Context(), principal.TenantID, projectID, revNo); err != nil {
		switch {
		case errors.Is(err, project.ErrProjectNotFound):
			writeConnectError(w, connect.NewError(connect.CodeNotFound, err))
		case errors.Is(err, project.ErrDeleteCurrentRevision):
			writeConnectError(w, connect.NewError(connect.CodeFailedPrecondition, err))
		default:
			writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		}
		return
	}
	recorder.Record(r.Context(), audit.Event{
		TenantID:     principal.TenantID,
		ActorUser:    principal.UserID,
		Action:       "project.source_revision.delete",
		ResourceType: "project",
		ResourceID:   projectID,
		Metadata:     map[string]any{"revision_no": revNo},
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

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
func loadProjectDocuments(ctx context.Context, projects project.ProjectStore, objects objectstore.ObjectStore, tenantID, projectID string, revA, revB int) (map[int]*project.Document, error) {
	out := make(map[int]*project.Document, 2)
	for _, rev := range [...]int{revA, revB} {
	_, err := projects.GetSourceRevision(ctx, tenantID, projectID, rev)
		if err != nil {
			out[rev] = nil
			continue
		}
		key := objectstore.ObjectKey{
			TenantID: tenantID, ProjectID: projectID,
			Revision: srcRevString(rev), AssetType: "document", AssetID: "extracted", Ext: "json",
		}
		rc, _, err := objects.Get(ctx, key)
		if err != nil {
			out[rev] = nil
			continue
		}
		var data []byte
		data, err = io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			out[rev] = nil
			continue
		}
		var doc project.Document
		if err := json.Unmarshal(data, &doc); err != nil {
			out[rev] = nil
			continue
		}
		out[rev] = &doc
	}
	return out, nil
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
				"slideId":    id,
				"oldName":    oldPg.Name,
				"newName":    newPg.Name,
				"oldNotes":   truncate(oldPg.NotesText, 80),
				"newNotes":   truncate(newPg.NotesText, 80),
				"pageCount":  newDoc.Features.PageCount,
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

// editorGetSlideNotes 读取单页演讲者备注（任意已认证成员可见）。
// 用户手写备注优先；缺失时回退解析文档中的原始备注，保证 PPT 自带备注可见。
func editorGetSlideNotes(w http.ResponseWriter, r *http.Request, notesStore project.SlideNotesStore, objects objectstore.ObjectStore) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	projectID := r.PathValue("pid")
	slideID := r.PathValue("sid")
	if projectID == "" || slideID == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("pid and sid required")))
		return
	}
	revStr := r.URL.Query().Get("revision_no")
	if revStr == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("revision_no required")))
		return
	}
	revNo, err := strconv.Atoi(revStr)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revision_no")))
		return
	}
	m, err := notesStore.Get(r.Context(), principal.TenantID, projectID, revNo)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	notes := ""
	stored, hasStored := "", false
	if m != nil {
		stored, hasStored = m[slideID]
	}
	switch {
	case hasStored:
		notes = stored // 用户显式覆盖（可能为空串 = 已清空）
	case objects != nil:
		notes = docSlideNotes(r.Context(), objects, principal.TenantID, projectID, revNo, slideID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"slide_id": slideID, "notes": notes})
}

// docSlideNotes 从解析产物中读取指定页的原始备注；读取失败或无该页时返回空串。
func docSlideNotes(ctx context.Context, objects objectstore.ObjectStore, tenantID, projectID string, revisionNo int, slideID string) string {
	docKey := objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: projectID,
		Revision: srcRevString(revisionNo), AssetType: "document", AssetID: "extracted", Ext: "json",
	}
	rc, _, err := objects.Get(ctx, docKey)
	if err != nil {
		return ""
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return ""
	}
	var doc struct {
		Pages []struct {
			SlideID   string `json:"slideId"`
			NotesText string `json:"notesText"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}
	for _, pg := range doc.Pages {
		if pg.SlideID == slideID {
			return strings.TrimSpace(pg.NotesText)
		}
	}
	return ""
}

// editorSetSlideNotes 保存单页演讲者备注（EDITOR+）。
func editorSetSlideNotes(w http.ResponseWriter, r *http.Request, notesStore project.SlideNotesStore, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if _, ok := requireProjectAccess(w, r, projects, members, recorder); !ok {
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	projectID := r.PathValue("pid")
	slideID := r.PathValue("sid")
	if projectID == "" || slideID == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("pid and sid required")))
		return
	}
	revStr := r.URL.Query().Get("revision_no")
	if revStr == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("revision_no required")))
		return
	}
	revNo, err := strconv.Atoi(revStr)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid revision_no")))
		return
	}
	var body struct {
		Notes string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, err))
		return
	}
	if err := notesStore.Set(r.Context(), principal.TenantID, projectID, revNo, slideID, body.Notes); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
