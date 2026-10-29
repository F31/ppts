package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
)

// 成品分享链接（M6-3）。与项目分享链接（collab.go）是两套东西，刻意不合并：
//   - 项目链接分享「会变的项目」，成品链接分享「不变的那一版成片」；
//   - 两者的匿名落点不同（/shared/{token} 返回动态播放清单，/s/{token} 返回包内条目）。
//
// 受保护（auth 中间件，editor 及以上）：
//   - GET  /projects/{pid}/artifacts/{aid}/shares                列出该成品的分享链接
//   - POST /projects/{pid}/artifacts/{aid}/shares                新建 {accessMode?, password?, expiresInDays?}
//   - POST /projects/{pid}/artifacts/{aid}/shares/{linkId}/revoke 撤销
func registerArtifactShareRoutes(
	mux *http.ServeMux,
	artifacts artifact.Store,
	links artifact.ShareLinkStore,
	projects project.ProjectStore,
	members membership.Reader,
	pepper string,
	recorder audit.Recorder,
	auth func(http.Handler) http.Handler,
) {
	mux.Handle("GET /projects/{pid}/artifacts/{aid}/shares", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listArtifactShares(w, r, artifacts, links, projects, members, recorder)
	})))
	mux.Handle("POST /projects/{pid}/artifacts/{aid}/shares", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		createArtifactShare(w, r, artifacts, links, projects, members, pepper, recorder)
	})))
	mux.Handle("POST /projects/{pid}/artifacts/{aid}/shares/{linkId}/revoke", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revokeArtifactShare(w, r, artifacts, links, projects, members, recorder, r.PathValue("linkId"))
	})))
}

// loadShareableArtifact 做三件事，顺序不能换：
//  1. 鉴权（principal / editor / 项目可见）；
//  2. 取成品并校验它属于路径里的项目——**不匹配一律 404**，不能返回 403：
//     否则「这个成品 ID 存在但不在你这个项目」本身就是一条可枚举的信息；
//  3. 校验格式是 html_bundle。创建阶段就拒绝，而不是等到匿名访问时才 500。
func loadShareableArtifact(
	w http.ResponseWriter, r *http.Request,
	artifacts artifact.Store, projects project.ProjectStore, members membership.Reader, recorder audit.Recorder,
) (*artifact.Artifact, bool) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return nil, false
	}
	if err := requireRole(r.Context(), members, membership.RoleEditor); err != nil {
		writeConnectError(w, err)
		return nil, false
	}
	projectID, ok := requireProjectAccess(w, r, projects, members, recorder)
	if !ok {
		return nil, false
	}
	a, err := artifacts.Get(r.Context(), principal.TenantID, r.PathValue("aid"))
	if err != nil {
		if errors.Is(err, artifact.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return nil, false
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	if a.ProjectID != projectID {
		http.Error(w, "not found", http.StatusNotFound)
		return nil, false
	}
	if a.Format != artifact.FormatHTMLBundle {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument,
			errors.New("only html_bundle artifacts can be shared online")))
		return nil, false
	}
	return a, true
}

func artifactShareLinkJSON(l *artifact.ShareLink) map[string]any {
	return map[string]any{
		"id":                l.ID,
		"artifactId":        l.ArtifactID,
		"projectId":         l.ProjectID,
		"token":             l.Token,
		"url":               "/s/" + l.Token + "/",
		"accessMode":        l.AccessMode,
		"passwordProtected": l.PasswordProtected,
		"expiresAt":         optionalTime(l.ExpiresAt),
		"revoked":           l.Revoked,
		"createdBy":         l.CreatedBy,
		"createdAt":         l.CreatedAt.Format(time.RFC3339),
		"lastAccessedAt":    optionalTime(l.LastAccessedAt),
	}
}

func listArtifactShares(w http.ResponseWriter, r *http.Request, artifacts artifact.Store, links artifact.ShareLinkStore,
	projects project.ProjectStore, members membership.Reader, recorder audit.Recorder) {
	a, ok := loadShareableArtifact(w, r, artifacts, projects, members, recorder)
	if !ok {
		return
	}
	principal, _ := requirePrincipal(r.Context())
	items, err := links.ListShareLinks(r.Context(), principal.TenantID, a.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, l := range items {
		out = append(out, artifactShareLinkJSON(l))
	}
	writeJSON(w, http.StatusOK, map[string]any{"shareLinks": out})
}

func createArtifactShare(w http.ResponseWriter, r *http.Request, artifacts artifact.Store, links artifact.ShareLinkStore,
	projects project.ProjectStore, members membership.Reader, pepper string, recorder audit.Recorder) {
	a, ok := loadShareableArtifact(w, r, artifacts, projects, members, recorder)
	if !ok {
		return
	}
	principal, _ := requirePrincipal(r.Context())

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
	// 枚举取自 project 包，不另定义一套：两套枚举迟早会出现「一边认、一边不认」的取值。
	if !project.ValidShareAccessMode(mode) {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid accessMode")))
		return
	}
	if body.ExpiresInDays < 0 {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid expiresInDays")))
		return
	}
	var expiresAt *time.Time
	if body.ExpiresInDays > 0 {
		t := time.Now().AddDate(0, 0, body.ExpiresInDays)
		expiresAt = &t
	}
	var hash string
	if strings.TrimSpace(body.Password) != "" {
		h, err := hashPassword(body.Password, pepper)
		if err != nil {
			writeConnectError(w, connect.NewError(connect.CodeInternal, err))
			return
		}
		hash = h
	}

	l, err := links.CreateShareLink(r.Context(), principal.TenantID, artifact.NewShareLink{
		ArtifactID: a.ID, ProjectID: a.ProjectID, AccessMode: mode,
		// 快照成品此刻指向的字节位置。不这么做的话，同一快照重新导出会原地改写
		// artifacts.object_key，旧链接就会指向新产物（门禁用例钉住这一点）。
		ObjectKey:    a.ObjectKey,
		ContentHash:  a.ContentHash,
		PasswordHash: hash, CreatedBy: principal.UserID, ExpiresAt: expiresAt,
	})
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"shareLink": artifactShareLinkJSON(l)})
}

func revokeArtifactShare(w http.ResponseWriter, r *http.Request, artifacts artifact.Store, links artifact.ShareLinkStore,
	projects project.ProjectStore, members membership.Reader, recorder audit.Recorder, linkID string) {
	a, ok := loadShareableArtifact(w, r, artifacts, projects, members, recorder)
	if !ok {
		return
	}
	principal, _ := requirePrincipal(r.Context())
	// 先确认 linkID 属于本合同品：否则「用成品 A 的路径撤销成品 B 的链接」会成功，
	// 因为 store 的 Revoke 只按 (id, tenant) 定位。
	items, err := links.ListShareLinks(r.Context(), principal.TenantID, a.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var owned bool
	for _, item := range items {
		if item.ID == linkID {
			owned = true
			break
		}
	}
	if !owned {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := links.RevokeShareLink(r.Context(), principal.TenantID, linkID); err != nil {
		if errors.Is(err, artifact.ErrShareLinkNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
