package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/public"
)

// 公开区审计（P2-B2）。
//
// 这一整块此前**完全没有审计**：发布、撤稿、删除、精选、审核，以及匿名访问都不留记录。
// 对一个"把内部作品暴露到公网"的功能来说，这意味着「谁把什么放出去了、什么时候撤回来的」
// 事后无从追溯 —— 争议发生时只能靠猜。本轮补齐，并刻意区分两类事件：
//
//  1. 写操作（publish/delete/recall/review/feature）：无条件记录，量级与业务操作同级；
//  2. 匿名读（showcase / manifest）：**默认关闭**，由 PPTS_AUDIT_ANONYMOUS_ACCESS=true 开启。
//
// 匿名读默认关闭是权衡后的决定，不是漏做：公开作品的读请求量可能比写操作高几个数量级，
// 无条件记录会让审计表先于业务表膨胀，进而把审计保留策略拖垮。开关关闭时也不伪装成已覆盖 ——
// 状态由 anonymousAccessAudited() 显式表达，运营可据此判断"日志里没有"是没开还是没人访问。
//
// 脱敏约束（必须遵守，由 coverage_test.go 守着）：
//   - **不记 publicId**：publicId 就是分享链接本身（/showcase/{publicId}），
//     把它写进审计等于给日志里复制一份"全部分享链接清单"，拿到审计归档即可访问所有作品；
//     记内部主键 pub.ID，管理员足以定位作品，外部无法据此构造链接；
//   - **不记 IP / User-Agent / Referer**：匿名访客之所以匿名，就是因为不采集这些；
//     一旦落库，访问者就被去匿名化了。

// publicationAction* 是公开区审计动作名。集中定义，便于覆盖矩阵与日志检索对齐。
const (
	publicationActionPublish  = "publication.publish"
	publicationActionFeature  = "publication.feature"
	publicationActionReview   = "publication.review"
	publicationActionDelete   = "publication.delete"
	publicationActionRecall   = "publication.recall"
	publicationActionAnonRead = "publication.anonymous_access"
)

// anonymousAccessAudited 返回匿名读是否记审计（PPTS_AUDIT_ANONYMOUS_ACCESS=true）。
func anonymousAccessAudited() bool {
	v := strings.TrimSpace(os.Getenv("PPTS_AUDIT_ANONYMOUS_ACCESS"))
	return v == "1" || strings.EqualFold(v, "true")
}

// recordPublication 记录一次公开区操作。
//
// 失败被忽略是刻意的：审计存储抖动不该连带打挂发布/撤稿（否则隐私设施变成可用性依赖）。
// 代价是极端情况下可能漏记 —— 这一取舍对四类操作一致，不做区别对待。
func recordPublication(ctx context.Context, recorder audit.Recorder, tenantID, actor, action, resourceID string, meta map[string]any) {
	if recorder == nil || tenantID == "" {
		return
	}
	_ = recorder.Record(ctx, audit.Event{
		TenantID:     tenantID,
		ActorUser:    actor,
		Action:       action,
		ResourceType: "publication",
		ResourceID:   resourceID,
		Metadata:     meta,
	})
}

// registerPublicRoutes 挂载公开区 HTTP 端点（V1.6 C-1，B5-M3 增强）。
//   - 匿名只读：
//     GET /public/works           已批准作品列表（跨租户聚合）
//     GET /showcase/{publicId}    单条已批准作品详情（附封面签名 URL），public_id 不可反推
//     GET /showcase/{publicId}/manifest  匿名可播放讲解清单
//   - 受保护读（auth 中间件）：
//     GET  /public/works/mine     我的发布（含未批准，限本人）
//     GET  /public/works/queue    待审核队列（管理员）
//   - 受保护写（auth 中间件）：
//     POST /public/works          用户发布自己项目（pending）
//     POST /public/featured       管理员发布官方精选（approved）
//     PUT  /public/works/{id}/review  管理员审核
//     DELETE /public/works/{id}   管理员删除
//     POST /public/works/{publicId}/recall  owner/admin 撤回（置 withdrawn，立即失效）
//
// 与 SPA catch-all（GET /{path...}）共存：精确 /public/works* 与 /showcase* 路由优先于通配。
//
// 注意：/public/works/mine 与 /public/works/queue 必须注册，否则会被
// GET /public/works/{id}（review/delete 的 {id} 通配）捕获而进入 uuid 查询报 500，
// 前端"我的发布/审核队列"就会退化为静默空态（A26 假列表）。
// Go 1.22 ServeMux 按具体度择优（字面量 > 通配），故这里与之并列注册即可稳定生效。
// 匿名详情从原 GET /public/works/{id}（内部主键，可反推）迁移到 GET /showcase/{publicId}
// （public_id，不可反推，B5-M3 A29）。
func registerPublicRoutes(mux *http.ServeMux, store public.Store, objects objectstore.ObjectStore, members membership.Reader, jobs JobStore, auth func(http.Handler) http.Handler, recorder audit.Recorder) {
	mux.HandleFunc("GET /public/works", func(w http.ResponseWriter, r *http.Request) {
		publicListWorks(w, r, store)
	})
	mux.Handle("GET /public/works/mine", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicListMine(w, r, store)
	})))
	mux.Handle("GET /public/works/queue", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicReviewQueue(w, r, store, members)
	})))
	// 匿名只读：按不可反推的 public_id 暴露，杜绝用内部主键枚举/反推（A29）。
	mux.HandleFunc("GET /showcase/{publicId}", func(w http.ResponseWriter, r *http.Request) {
		publicGetWork(w, r, store, objects, recorder)
	})
	mux.HandleFunc("GET /showcase/{publicId}/manifest", func(w http.ResponseWriter, r *http.Request) {
		publicGetManifest(w, r, store, jobs, objects, recorder)
	})
	mux.Handle("POST /public/works/{publicId}/recall", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicRecall(w, r, store, members, recorder)
	})))
	mux.Handle("POST /public/works", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicPublish(w, r, store, recorder)
	})))
	mux.Handle("POST /public/featured", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicFeature(w, r, store, members, recorder)
	})))
	mux.Handle("PUT /public/works/{id}/review", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicReview(w, r, store, members, recorder)
	})))
	mux.Handle("DELETE /public/works/{id}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicDelete(w, r, store, members, recorder)
	})))
}

func publicListWorks(w http.ResponseWriter, r *http.Request, store public.Store) {
	kind := r.URL.Query().Get("kind")
	if kind != "" && !public.Kind(kind).Valid() {
		http.Error(w, "invalid kind", http.StatusBadRequest)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, next, err := store.ListApproved(r.Context(), public.Kind(kind), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

// publicWorkResponse 在 Publication 基础上附加匿名可读的封面签名 URL。
type publicWorkResponse struct {
	*public.Publication
	CoverURL string `json:"cover_url,omitempty"`
}

func publicGetWork(w http.ResponseWriter, r *http.Request, store public.Store, objects objectstore.ObjectStore, recorder audit.Recorder) {
	publicID := r.PathValue("publicId")
	pub, err := store.GetApprovedByPublicID(r.Context(), publicID)
	if errors.Is(err, public.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// 匿名读审计：只记内部主键与访问面，不记 publicId / IP / UA（见文件头脱敏约束）。
	if anonymousAccessAudited() {
		recordPublication(r.Context(), recorder, pub.TenantID, "anonymous", publicationActionAnonRead,
			pub.ID, map[string]any{"surface": "showcase"})
	}
	resp := publicWorkResponse{Publication: pub}
	if pub.CoverObjectKey != "" {
		if key, perr := objectstore.Parse(pub.CoverObjectKey); perr == nil {
			if raw, serr := objects.SignedURL(r.Context(), key, objectstore.OpRead, time.Hour); serr == nil {
				if parser, ok := objects.(signedURLParser); ok {
					raw = rewriteLocalSignedURL(parser, raw)
				}
				resp.CoverURL = raw
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// publicManifestResource / publicManifest 是与前端 Player（types.PlaybackManifest）同构的匿名清单 JSON。
// 枚举以 .String() 名称字符串输出（与 Connect protojson 一致），确保 Player 的 resource.type 字符串比较成立。
type publicManifestResource struct {
	Type        string `json:"type"`
	Key         string `json:"key"`
	SignedUrl   string `json:"signedUrl"`
	ContentType string `json:"contentType,omitempty"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
	ContentHash string `json:"contentHash,omitempty"`
	SlideId     string `json:"slideId,omitempty"`
	SegmentId   string `json:"segmentId,omitempty"`
}

type publicManifest struct {
	ProjectId     string                   `json:"projectId"`
	TimelineKey   string                   `json:"timelineKey"`
	TimelineJson  string                   `json:"timelineJson"`
	Resources     []publicManifestResource `json:"resources"`
	ExpiresAtUnix int64                    `json:"expiresAtUnix"`
}

// publicGetManifest 为已批准公开作品生成匿名可播放的讲解清单（B3 音频播放接入）。
// 仅放行 approved 作品；复用既有 timeline 打包与签名逻辑，音频/字幕/页面 PNG 均签发短期匿名可读 URL。
// 作品若无成功配音任务（narration 未就绪），返回 404，前端优雅降级为「暂未生成语音讲解」。
func publicGetManifest(w http.ResponseWriter, r *http.Request, store public.Store, jobs JobStore, objects objectstore.ObjectStore, recorder audit.Recorder) {
	publicID := r.PathValue("publicId")
	pub, err := store.GetApprovedByPublicID(r.Context(), publicID)
	if errors.Is(err, public.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// 匿名读审计：同上，只记内部主键与访问面。
	if anonymousAccessAudited() {
		recordPublication(r.Context(), recorder, pub.TenantID, "anonymous", publicationActionAnonRead,
			pub.ID, map[string]any{"surface": "showcase.manifest"})
	}
	// 与私密分享（#95）共用同一条时间轴打包链路，避免两处逻辑漂移。
	manifest, err := buildPlaybackManifest(r.Context(), pub.TenantID, pub.ProjectID, jobs, objects)
	if err != nil {
		if errors.Is(err, pipeline.ErrNoSucceededJob) {
			http.Error(w, "narration not ready", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// 公开作品沿用既有行为：清单携带 project_id 供播放器溯源；私密分享侧刻意留空。
	manifest.ProjectId = pub.ProjectID
	writeJSON(w, http.StatusOK, manifest)
}

func publicPublish(w http.ResponseWriter, r *http.Request, store public.Store, recorder audit.Recorder) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		ProjectID      string `json:"project_id"`
		Title          string `json:"title"`
		Summary        string `json:"summary"`
		CoverObjectKey string `json:"cover_object_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.ProjectID == "" || body.Title == "" {
		http.Error(w, "project_id and title are required", http.StatusBadRequest)
		return
	}
	if body.CoverObjectKey != "" {
		key, err := objectstore.Parse(body.CoverObjectKey)
		if err != nil {
			http.Error(w, "invalid cover_object_key", http.StatusBadRequest)
			return
		}
		if err := key.EnsureTenant(principal.TenantID); err != nil {
			http.Error(w, "cover tenant mismatch", http.StatusForbidden)
			return
		}
	}
	in := public.NewPublication{
		ProjectID:      body.ProjectID,
		Kind:           public.KindUser,
		Title:          body.Title,
		Summary:        body.Summary,
		CoverObjectKey: body.CoverObjectKey,
		CreatedBy:      principal.UserID,
	}
	pub, err := store.Publish(r.Context(), principal.TenantID, in)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// 发布即"把内部项目暴露到公网"，必须留痕：谁发的、发的是哪个项目、初始状态。
	recordPublication(r.Context(), recorder, principal.TenantID, principal.UserID, publicationActionPublish,
		pub.ID, map[string]any{"project_id": body.ProjectID, "kind": string(pub.Kind), "state": string(pub.Status)})
	writeJSON(w, http.StatusCreated, pub)
}

func publicFeature(w http.ResponseWriter, r *http.Request, store public.Store, members membership.Reader, recorder audit.Recorder) {
	principal, ok := requireAdmin(r.Context(), members, w)
	if !ok {
		return
	}
	var body struct {
		ProjectID      string `json:"project_id"`
		Title          string `json:"title"`
		Summary        string `json:"summary"`
		CoverObjectKey string `json:"cover_object_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.ProjectID == "" || body.Title == "" {
		http.Error(w, "project_id and title are required", http.StatusBadRequest)
		return
	}
	if body.CoverObjectKey != "" {
		key, err := objectstore.Parse(body.CoverObjectKey)
		if err != nil {
			http.Error(w, "invalid cover_object_key", http.StatusBadRequest)
			return
		}
		if err := key.EnsureTenant(principal.TenantID); err != nil {
			http.Error(w, "cover tenant mismatch", http.StatusForbidden)
			return
		}
	}
	in := public.NewPublication{
		ProjectID:      body.ProjectID,
		Kind:           public.KindFeatured,
		Title:          body.Title,
		Summary:        body.Summary,
		CoverObjectKey: body.CoverObjectKey,
		CreatedBy:      principal.UserID,
	}
	pub, err := store.Feature(r.Context(), principal.TenantID, in)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// 官方精选直接 approved（不经审核即对外可见），比用户发布更需要留痕。
	recordPublication(r.Context(), recorder, principal.TenantID, principal.UserID, publicationActionFeature,
		pub.ID, map[string]any{"project_id": body.ProjectID, "state": string(pub.Status)})
	writeJSON(w, http.StatusCreated, pub)
}

func publicReview(w http.ResponseWriter, r *http.Request, store public.Store, members membership.Reader, recorder audit.Recorder) {
	principal, ok := requireAdmin(r.Context(), members, w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var body struct {
		Approve bool `json:"approve"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	pub, err := store.Review(r.Context(), principal.TenantID, id, body.Approve, principal.UserID)
	if errors.Is(err, public.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// 审核是"对外可见性"的闸门动作：放行或驳回都要能追溯是谁做的。
	recordPublication(r.Context(), recorder, principal.TenantID, principal.UserID, publicationActionReview,
		pub.ID, map[string]any{"approved": body.Approve, "state": string(pub.Status)})
	writeJSON(w, http.StatusOK, pub)
}

func publicDelete(w http.ResponseWriter, r *http.Request, store public.Store, members membership.Reader, recorder audit.Recorder) {
	principal, ok := requireAdmin(r.Context(), members, w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	// 删除前先取一次：删完就没得记了（审计需要作品归属与标题做人工核对）。
	existing, _ := store.ListByProject(r.Context(), principal.TenantID, "")
	var title string
	for _, p := range existing {
		if p.ID == id {
			title = p.Title
			break
		}
	}
	err := store.Delete(r.Context(), principal.TenantID, id)
	if errors.Is(err, public.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	meta := map[string]any{}
	if title != "" {
		meta["title"] = title
	}
	recordPublication(r.Context(), recorder, principal.TenantID, principal.UserID, publicationActionDelete, id, meta)
	w.WriteHeader(http.StatusNoContent)
}

// publicRecall 由 owner/admin 撤回已发布作品：置 withdrawn 状态，匿名只读策略（status='approved'）立即拒绝。
// 复用 requireAdmin——其 roleRank 比较实际放行 admin 及其以上的 owner（B5-M3，C-8 要求 owner/admin 可撤回）。
func publicRecall(w http.ResponseWriter, r *http.Request, store public.Store, members membership.Reader, recorder audit.Recorder) {
	principal, ok := requireAdmin(r.Context(), members, w)
	if !ok {
		return
	}
	publicID := r.PathValue("publicId")
	pub, err := store.Recall(r.Context(), principal.TenantID, publicID, principal.UserID)
	if errors.Is(err, public.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// 撤稿 = 收回对外可见性。只记内部主键，不记 publicId（见文件头脱敏约束）。
	recordPublication(r.Context(), recorder, principal.TenantID, principal.UserID, publicationActionRecall,
		pub.ID, map[string]any{"state": string(pub.Status)})
	writeJSON(w, http.StatusOK, pub)
}

func requireAdmin(ctx context.Context, members membership.Reader, w http.ResponseWriter) (Principal, bool) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return Principal{}, false
	}
	if members != nil {
		role, err := members.GetRole(ctx, principal.TenantID, principal.UserID)
		if err != nil || roleRank(role) < roleRank(membership.RoleAdmin) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return Principal{}, false
		}
	}
	return principal, true
}

func publicListMine(w http.ResponseWriter, r *http.Request, store public.Store) {
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	status := public.Status(r.URL.Query().Get("status"))
	items, err := store.ListMine(r.Context(), principal.TenantID, principal.UserID, status)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// 与 GET /public/works 同构：始终带 next_cursor，避免前端 PublicWorkPage 类型出现 undefined。
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": ""})
}

func publicReviewQueue(w http.ResponseWriter, r *http.Request, store public.Store, members membership.Reader) {
	principal, ok := requireAdmin(r.Context(), members, w)
	if !ok {
		return
	}
	kind := public.Kind(r.URL.Query().Get("kind"))
	items, err := store.ListPending(r.Context(), principal.TenantID, kind)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
