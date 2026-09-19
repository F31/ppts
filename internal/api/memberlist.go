package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"

	"github.com/F31/ppts/internal/membership"
)

// registerMemberRoutes 挂载成员档案原生 HTTP 端点（buf/protoc 不可用，不新增 Connect RPC）：
//   - GET /members：返回租户成员富字段列表（任意成员可见，租户隔离），供成员与权限页展示；
//   - PUT /members/{userId}：写入成员档案（admin 级，对应 member.manage 能力）。
//
// 注意：新增任何 REST handler 必须同步在此挂载（见项目风险 R-10）。
func registerMemberRoutes(mux *http.ServeMux, members membership.Store, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /members", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listEnrichedMembers(w, r, members)
	})))
	mux.Handle("PUT /members/{userId}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		updateMemberProfile(w, r, members)
	})))
}

// listEnrichedMembers 返回当前租户全部成员（含 email/username/姓名/性别/出生年月/电话/创建时间）。
// 权限：任意已认证成员（与 proto Members 同级，viewer 可见）。
func listEnrichedMembers(w http.ResponseWriter, r *http.Request, members membership.Store) {
	principal, err := requirePrincipal(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	list, err := members.List(r.Context(), principal.TenantID)
	if err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, m := range list {
		out = append(out, map[string]any{
			"user_id":    m.UserID,
			"role":       roleProto(m.Role).String(),
			"created_at": m.CreatedAt.Format(time.RFC3339),
			"email":      m.Email,
			"username":   m.Username,
			"full_name":  m.FullName,
			"gender":     m.Gender,
			"birth_date": m.BirthDate,
			"phone":      m.Phone,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": out})
}

// updateMemberProfile 写入成员档案（admin 级）。
func updateMemberProfile(w http.ResponseWriter, r *http.Request, members membership.Store) {
	if _, err := requirePrincipal(r.Context()); err != nil {
		writeConnectError(w, err)
		return
	}
	if err := requireRole(r.Context(), members, membership.RoleAdmin); err != nil {
		writeConnectError(w, err)
		return
	}
	userID := r.PathValue("userId")
	if userID == "" {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, errors.New("user_id required")))
		return
	}
	var body struct {
		Username  string `json:"username"`
		FullName  string `json:"full_name"`
		Gender    string `json:"gender"`
		BirthDate string `json:"birth_date"`
		Phone     string `json:"phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInvalidArgument, err))
		return
	}
	if err := members.SaveProfile(r.Context(), userID, membership.Profile{
		Username:  body.Username,
		FullName:  body.FullName,
		Gender:    body.Gender,
		BirthDate: body.BirthDate,
		Phone:     body.Phone,
	}); err != nil {
		writeConnectError(w, connect.NewError(connect.CodeInternal, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
