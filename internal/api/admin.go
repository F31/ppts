package api

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/F31/ppts/internal/tenant"
	"github.com/F31/ppts/internal/usage"
)

// TenantAdminStore 是运营商后台所需的租户控制面能力。
type TenantAdminStore interface {
	ListTenantsPage(ctx context.Context, cursor string, pageSize int) ([]tenant.Summary, string, error)
	CountTenants(ctx context.Context) (int, error)
	Suspend(ctx context.Context, tenantID string) error
	Resume(ctx context.Context, tenantID string) error
}

// adminDeps 是运营商后台依赖。operatorIDs 为空时整个 /admin 面不挂载（功能关闭）。
type adminDeps struct {
	operatorIDs map[string]bool
	tenants     TenantAdminStore
	quota       usage.Store
	logger      *log.Logger
}

// registerAdminRoutes 挂载运营商后台（跨租户查看 + 挂起/恢复）。仅 PPTS_OPERATOR_USER_IDS
// 中的用户可访问；其余用户（含普通租户 owner）返回 403。
func registerAdminRoutes(mux *http.ServeMux, d *adminDeps, auth func(http.Handler) http.Handler) {
	if len(d.operatorIDs) == 0 || d.tenants == nil {
		return
	}
	guard := func(h http.HandlerFunc) http.Handler {
		return auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFromContext(r.Context())
			if !ok || !d.operatorIDs[p.UserID] {
				http.Error(w, "operator privilege required", http.StatusForbidden)
				return
			}
			h(w, r)
		}))
	}
	mux.Handle("GET /admin/tenants", guard(d.listTenants))
	mux.Handle("POST /admin/tenants/{id}/suspend", guard(d.suspendTenant))
	mux.Handle("POST /admin/tenants/{id}/resume", guard(d.resumeTenant))
}

func (d *adminDeps) listTenants(w http.ResponseWriter, r *http.Request) {
	pageSize := 200
	if v := strings.TrimSpace(r.URL.Query().Get("page_size")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			pageSize = n
		}
	}
	list, nextCursor, err := d.tenants.ListTenantsPage(r.Context(), strings.TrimSpace(r.URL.Query().Get("cursor")), pageSize)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		row := map[string]any{
			"id":         t.ID,
			"name":       t.Name,
			"type":       t.Type,
			"status":     string(t.Status),
			"created_at": t.CreatedAt,
		}
		if d.quota != nil {
			if q, err := d.quota.GetQuota(r.Context(), t.ID, usage.KindGenSeconds); err == nil && q != nil {
				row["quota"] = map[string]any{
					"kind":            string(q.Kind),
					"limit_units":     q.LimitUnits,
					"reserved_units":  q.ReservedUnits,
					"consumed_units":  q.ConsumedUnits,
					"available_units": q.Available(),
					"unlimited":       q.Unlimited(),
				}
			}
		}
		out = append(out, row)
	}
	total := 0
	if d.tenants != nil {
		if n, err := d.tenants.CountTenants(r.Context()); err == nil {
			total = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenants":     out,
		"next_cursor": nextCursor,
		"total":       total,
	})
}

func (d *adminDeps) suspendTenant(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "tenant id required", http.StatusBadRequest)
		return
	}
	if err := d.tenants.Suspend(r.Context(), id); err != nil {
		writeAdminTenantError(w, err, d.logger, "suspend", id)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": string(tenant.StatusSuspended)})
}

func (d *adminDeps) resumeTenant(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "tenant id required", http.StatusBadRequest)
		return
	}
	if err := d.tenants.Resume(r.Context(), id); err != nil {
		writeAdminTenantError(w, err, d.logger, "resume", id)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": string(tenant.StatusActive)})
}

func writeAdminTenantError(w http.ResponseWriter, err error, logger *log.Logger, op, id string) {
	if err == tenant.ErrTenantNotFound {
		http.Error(w, "tenant not found", http.StatusNotFound)
		return
	}
	if logger != nil {
		logger.Printf("admin: %s tenant %s failed: %v", op, id, err)
	}
	http.Error(w, "internal error", http.StatusInternalServerError)
}
