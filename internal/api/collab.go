package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/internal/tenant"
)

// registerCollabRoutes 挂载私密分享与协作者原生 HTTP 端点（buf/protoc 不可用，不新增 Connect RPC；#95）。
//
// 与「发布到公开作品广场」是两种**完全不同**的能力，不共用一个开关：
// 公开广场面向所有匿名访问者且需审核；私密分享只面向持有链接的人，不出现在广场。
//
// 受保护（auth 中间件，读取任意成员可见，写入要求 editor 及以上）：
//   - GET    /projects/{pid}/collaborators              列出项目协作者
//   - POST   /projects/{pid}/collaborators              邀请协作者 {email, role}
//   - PUT    /projects/{pid}/collaborators/{userId}     变更协作者角色 {role}
//   - DELETE /projects/{pid}/collaborators/{userId}     移除协作者
//   - GET    /projects/{pid}/shares                     列出私密分享链接
//   - POST   /projects/{pid}/shares                     新建分享链接 {accessMode, password?, expiresInDays?}
//   - POST   /projects/{pid}/shares/{linkId}/revoke     撤回分享链接（立即失效）
//
// 匿名（无 auth，最小字段，绝不携带 tenant_id / 内部主键）：
//   - GET /shared/{token}              分享元信息（标题、访问模式、是否需要口令、有效期）
//   - GET /shared/{token}/manifest     匿名可播放讲解清单（口令经 X-Share-Password 头传递）
//
// 安全要点（对应《实施计划》R-2/R-15 越权风险）：
//   - token 为不可反推随机串，命中即代表"未撤回且未过期"（由 anon_read RLS 策略保证）。
//   - 口令错误与链接不存在返回**同一 404 文案**，防止枚举有效链接。
//   - 口令只通过请求头传递，不进 URL / 访问日志。
func registerCollabRoutes(
	mux *http.ServeMux,
	projects project.ProjectStore,
	members membership.Reader,
	jobs JobStore,
	objects objectstore.ObjectStore,
	pepper string,
	auth func(http.Handler) http.Handler,
) {
	mux.Handle("GET /projects/{pid}/collaborators", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listCollaborators(w, r, projects)
	})))
	mux.Handle("POST /projects/{pid}/collaborators", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inviteCollaborator(w, r, projects, members)
	})))
	mux.Handle("PUT /projects/{pid}/collaborators/{userId}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		updateCollaborator(w, r, projects, members, r.PathValue("userId"))
	})))
	mux.Handle("DELETE /projects/{pid}/collaborators/{userId}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		removeCollaborator(w, r, projects, members, r.PathValue("userId"))
	})))

	mux.Handle("GET /projects/{pid}/shares", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listShareLinks(w, r, projects)
	})))
	mux.Handle("POST /projects/{pid}/shares", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		createShareLink(w, r, projects, members, pepper)
	})))
	mux.Handle("POST /projects/{pid}/shares/{linkId}/revoke", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revokeShareLink(w, r, projects, members, r.PathValue("linkId"))
	})))

	mux.HandleFunc("GET /shared/{token}", func(w http.ResponseWriter, r *http.Request) {
		sharedMeta(w, r, projects)
	})
	mux.HandleFunc("GET /shared/{token}/manifest", func(w http.ResponseWriter, r *http.Request) {
		sharedManifest(w, r, projects, jobs, objects, pepper)
	})
}

// collabError 将领域错误映射为带 HTTP 码的 connect 错误。
func collabError(err error) error {
	switch {
	case errors.Is(err, project.ErrCollaboratorNotFound),
		errors.Is(err, project.ErrShareLinkNotFound),
		errors.Is(err, project.ErrProjectNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, project.ErrInvalidCollaboratorRole):
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func collaboratorJSON(c *project.Collaborator) map[string]any {
	return map[string]any{
		"userId":         c.UserID,
		"role":           c.Role,
		"email":          c.Email,
		"username":       c.Username,
		"fullName":       c.FullName,
		"invitedBy":      c.InvitedBy,
		"createdAt":      c.CreatedAt.Format(time.RFC3339),
		"lastAccessedAt": optionalTime(c.LastAccessedAt),
	}
}

func shareLinkJSON(l *project.ShareLink) map[string]any {
	return map[string]any{
		"id":                l.ID,
		"projectId":         l.ProjectID,
		"token":             l.Token,
		"url":               "/shared/" + l.Token,
		"accessMode":        l.AccessMode,
		"passwordProtected": l.PasswordProtected,
		"expiresAt":         optionalTime(l.ExpiresAt),
		"revoked":           l.Revoked,
		"createdBy":         l.CreatedBy,
		"createdAt":         l.CreatedAt.Format(time.RFC3339),
		"lastAccessedAt":    optionalTime(l.LastAccessedAt),
	}
}

func optionalTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

func listCollaborators(w http.ResponseWriter, r *http.Request, projects project.ProjectStore) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	items, err := projects.ListCollaborators(r.Context(), principal.TenantID, r.PathValue("pid"))
	if err != nil {
		writeConnectError(w, collabError(err))
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, c := range items {
		out = append(out, collaboratorJSON(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"collaborators": out})
}

// inviteCollaborator 按邮箱邀请**本租户成员**为项目协作者。
// 租户外邮箱暂不支持（无邮件邀约链路），返回明确 404 原因而非静默失败（A26）。
func inviteCollaborator(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	var body struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, err))
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("email is required")))
		return
	}
	userID, err := resolveTenantUser(r.Context(), members, principal.TenantID, email)
	if err != nil {
		writeConnectError(w, err)
		return
	}
	c, err := projects.InviteCollaborator(r.Context(), principal.TenantID, r.PathValue("pid"), userID, body.Role, principal.UserID)
	if err != nil {
		writeConnectError(w, collabError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"collaborator": collaboratorJSON(c)})
}

// resolveTenantUser 在本租户成员里按邮箱（回退用户名）解析 user_id。
// 找不到时返回 NotFound 并说明"暂不支持邀请租户外用户"，不静默造一个假协作者。
func resolveTenantUser(ctx context.Context, members membership.Reader, tenantID, email string) (string, error) {
	if members == nil {
		return "", connect.NewError(connect.CodeFailedPrecondition, errors.New("membership store is unavailable"))
	}
	list, err := members.List(ctx, tenantID)
	if err != nil {
		return "", connect.NewError(connect.CodeInternal, err)
	}
	for _, m := range list {
		if strings.EqualFold(strings.TrimSpace(m.Email), email) {
			return m.UserID, nil
		}
	}
	for _, m := range list {
		if strings.EqualFold(strings.TrimSpace(m.Username), email) {
			return m.UserID, nil
		}
	}
	return "", connect.NewError(connect.CodeNotFound,
		errors.New("该邮箱/用户名不是本租户成员；当前仅支持邀请租户内成员，暂不支持向租户外发送邀约"))
}

func updateCollaborator(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, userID string) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, err))
		return
	}
	c, err := projects.UpdateCollaboratorRole(r.Context(), principal.TenantID, r.PathValue("pid"), userID, body.Role)
	if err != nil {
		writeConnectError(w, collabError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"collaborator": collaboratorJSON(c)})
}

func removeCollaborator(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, userID string) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	if err := projects.RemoveCollaborator(r.Context(), principal.TenantID, r.PathValue("pid"), userID); err != nil {
		writeConnectError(w, collabError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func listShareLinks(w http.ResponseWriter, r *http.Request, projects project.ProjectStore) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	items, err := projects.ListShareLinks(r.Context(), principal.TenantID, r.PathValue("pid"))
	if err != nil {
		writeConnectError(w, collabError(err))
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, l := range items {
		out = append(out, shareLinkJSON(l))
	}
	writeJSON(w, http.StatusOK, map[string]any{"shareLinks": out})
}

func createShareLink(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, pepper string) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	var body struct {
		AccessMode    string `json:"accessMode"`
		Password      string `json:"password"`
		ExpiresInDays int    `json:"expiresInDays"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, err))
		return
	}
	mode := body.AccessMode
	if mode == "" {
		mode = project.ShareAccessViewOnly
	}
	if !project.ValidShareAccessMode(mode) {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid accessMode")))
		return
	}
	var expiresAt *time.Time
	if body.ExpiresInDays > 0 {
		t := time.Now().AddDate(0, 0, body.ExpiresInDays)
		expiresAt = &t
	}
	var hash string
	if strings.TrimSpace(body.Password) != "" {
		hash, err = hashPassword(body.Password, pepper)
		if err != nil {
			writeConnectError(w, connect.NewError(connect.CodeInternal, err))
			return
		}
	}
	l, err := projects.CreateShareLink(r.Context(), principal.TenantID, r.PathValue("pid"), mode, hash, principal.UserID, expiresAt)
	if err != nil {
		writeConnectError(w, collabError(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"shareLink": shareLinkJSON(l)})
}

func revokeShareLink(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, members membership.Reader, linkID string) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return
	}
	if err := projects.RevokeShareLink(r.Context(), principal.TenantID, linkID); err != nil {
		writeConnectError(w, collabError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- 匿名链路：最小字段，绝不携带 tenant_id / 内部主键 ----

// sharedMetaResponse 只暴露展示必需字段；project_id / tenant_id 一律不下发。
type sharedMetaResponse struct {
	Title             string `json:"title"`
	AccessMode        string `json:"accessMode"`
	PasswordProtected bool   `json:"passwordProtected"`
	ExpiresAtUnix     int64  `json:"expiresAtUnix"`
}

// loadSharedLink 按 token 取链接：命中即代表"未撤回且未过期"（anon_read RLS 策略保证）。
// 未命中（不存在 / 已撤回 / 已过期）统一归一为 ErrShareLinkNotFound，防枚举。
// 口令**不在这里校验**——元信息接口必须在未输入口令时也能返回，客户端才知道要提示输入口令。
func loadSharedLink(ctx context.Context, token string, projects project.ProjectStore) (*project.ShareLink, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, project.ErrShareLinkNotFound
	}
	link, err := projects.GetShareLinkByToken(ctx, token)
	if err != nil {
		return nil, project.ErrShareLinkNotFound
	}
	return link, nil
}

// authorizeSharePassword 校验口令：失败与链接无效返回同一结果，不区分"口令错/链接不存在"，防枚举。
func authorizeSharePassword(ctx context.Context, link *project.ShareLink, projects project.ProjectStore, pepper, provided string) bool {
	if !link.PasswordProtected {
		return true
	}
	hash, err := projects.ShareLinkPasswordHash(ctx, link.TenantID, link.ID)
	if err != nil || hash == "" {
		return false
	}
	return verifyPassword(hash, provided, pepper)
}

func sharedMeta(w http.ResponseWriter, r *http.Request, projects project.ProjectStore) {
	link, err := loadSharedLink(r.Context(), r.PathValue("token"), projects)
	if err != nil {
		http.Error(w, "share link not found", http.StatusNotFound)
		return
	}
	title := ""
	if p, perr := projects.GetProject(r.Context(), link.TenantID, link.ProjectID); perr == nil {
		title = p.Title
	}
	var expires int64
	if link.ExpiresAt != nil {
		expires = link.ExpiresAt.Unix()
	}
	writeJSON(w, http.StatusOK, sharedMetaResponse{
		Title:             title,
		AccessMode:        link.AccessMode,
		PasswordProtected: link.PasswordProtected,
		ExpiresAtUnix:     expires,
	})
}

func sharedManifest(w http.ResponseWriter, r *http.Request, projects project.ProjectStore, jobs JobStore, objects objectstore.ObjectStore, pepper string) {
	link, err := loadSharedLink(r.Context(), r.PathValue("token"), projects)
	if err != nil {
		http.Error(w, "share link not found", http.StatusNotFound)
		return
	}
	if !authorizeSharePassword(r.Context(), link, projects, pepper, r.Header.Get("X-Share-Password")) {
		http.Error(w, "share link not found", http.StatusNotFound)
		return
	}
	manifest, err := buildPlaybackManifest(r.Context(), link.TenantID, link.ProjectID, jobs, objects)
	if err != nil {
		if errors.Is(err, pipeline.ErrNoSucceededJob) {
			http.Error(w, "narration not ready", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// 访问时间只在真正取到清单后更新，避免无效口令刷写。
	_ = projects.TouchShareLinkAccess(r.Context(), link.TenantID, link.ID)
	writeJSON(w, http.StatusOK, manifest)
}

// buildPlaybackManifest 为指定租户/项目生成匿名可播放讲解清单（公开广场与私密分享共用同一链路）。
// 仅复用已成功配音任务的时间轴产物；未就绪时返回 pipeline.ErrNoSucceededJob 由上层降级。
func buildPlaybackManifest(ctx context.Context, tenantID, projectID string, jobs JobStore, objects objectstore.ObjectStore) (*publicManifest, error) {
	job, err := jobs.LatestSucceededJob(ctx, tenantID, projectID, string(pipeline.KindNarration))
	if err != nil {
		return nil, err
	}
	timelineKey, err := jobs.StepResultRef(tenant.WithContext(ctx, tenantID), job.ID, "timeline")
	if err != nil || timelineKey == "" {
		return nil, pipeline.ErrNoSucceededJob
	}
	// 页面 PNG 为可选项：获取失败则优雅降级为音频+字幕。
	pagePngKeys, _ := resolvePagePngKeys(ctx, jobs, objects, tenantID, projectID, timelineKey)
	bundle, _, err := loadBundle(ctx, objects, tenantID, timelineKey)
	if err != nil {
		return nil, err
	}
	parser, _ := objects.(signedURLParser)
	ttl := time.Hour
	resources, err := signManifestResources(ctx, objects, parser, tenantID, timelineKey, bundle, pagePngKeys, ttl)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	return &publicManifest{
		// 私密分享不暴露内部 project_id：前端仅按资源播放，无需项目标识。
		ProjectId:     "",
		TimelineKey:   timelineKey,
		TimelineJson:  string(timelineJSON),
		Resources:     out,
		ExpiresAtUnix: time.Now().Add(ttl).Unix(),
	}, nil
}
