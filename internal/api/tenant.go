package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/tenant"
	"github.com/F31/ppts/internal/usage"
)

// TenantUsageReader 提供租户配额与用量读取（G3-9）。
type TenantUsageReader interface {
	GetQuota(ctx context.Context, tenantID string, kind usage.Kind) (*usage.Quota, error)
	UsageSummary(ctx context.Context, tenantID, month string) (seconds float64, userAmount float64, supplierCost float64, err error)
	ProjectUsage(ctx context.Context, tenantID, projectID string) (usage.ProjectUsage, error)
	// Currency 返回定价表币种（G3-2 分账）。
	Currency() string
}

// TenantPolicyReader 提供租户策略读取。
type TenantPolicyReader interface {
	GetPolicy(ctx context.Context, tenantID string) (*tenant.Policy, error)
}

// TenantLifecycle 提供租户导出与数据擦除（G3-4）。
type TenantLifecycle interface {
	ExportTenant(ctx context.Context, tenantID string, objects objectstore.ObjectStore) (*tenant.ExportManifest, error)
	PurgeTenant(ctx context.Context, tenantID string, objects objectstore.ObjectStore) (int64, error)
}

// TenantStorageReader provides tenant storage usage visibility (G3-6/G3-8).
type TenantStorageReader interface {
	StorageUsage(ctx context.Context, tenantID string) (*tenant.StorageUsage, error)
}

// TenantArchiveReader provides audit archive object listing (G3-4).
type TenantArchiveReader interface {
	ListAuditArchives(ctx context.Context, tenantID string, limit int) ([]tenant.ArchiveFile, error)
}

// TenantService 提供租户成员、角色、配额与策略接口（V4.0 §11.1/§12）。
type TenantService struct {
	pptsv1connect.UnimplementedTenantServiceHandler
	usage     TenantUsageReader
	policy    TenantPolicyReader
	members   membership.Store
	audit     audit.Store
	lifecycle TenantLifecycle
	objects   objectstore.ObjectStore
	storage   TenantStorageReader
	archive   TenantArchiveReader
}

// NewTenantService 创建租户服务。
func NewTenantService(u TenantUsageReader, p TenantPolicyReader, members membership.Store, auditStore audit.Store, lifecycle TenantLifecycle, objects objectstore.ObjectStore, storage TenantStorageReader, archive TenantArchiveReader) *TenantService {
	return &TenantService{usage: u, policy: p, members: members, audit: auditStore, lifecycle: lifecycle, objects: objects, storage: storage, archive: archive}
}

func (s *TenantService) Members(ctx context.Context, _ *connect.Request[pptsv1.GetMembersRequest]) (*connect.Response[pptsv1.GetMembersResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if s.members == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("members not configured"))
	}
	list, err := s.members.List(ctx, p.TenantID)
	if err != nil {
		return nil, tenantError(err)
	}
	out := make([]*pptsv1.Member, 0, len(list))
	for _, m := range list {
		out = append(out, &pptsv1.Member{UserId: m.UserID, Role: roleProto(m.Role)})
	}
	return connect.NewResponse(&pptsv1.GetMembersResponse{Members: out}), nil
}

func (s *TenantService) Roles(ctx context.Context, _ *connect.Request[pptsv1.GetRolesRequest]) (*connect.Response[pptsv1.GetRolesResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if s.members == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("members not configured"))
	}
	list, err := s.members.List(ctx, p.TenantID)
	if err != nil {
		return nil, tenantError(err)
	}
	roles := make(map[string]pptsv1.Role, len(list))
	for _, m := range list {
		roles[m.UserID] = roleProto(m.Role)
	}
	return connect.NewResponse(&pptsv1.GetRolesResponse{UserRoles: roles}), nil
}

func (s *TenantService) SetMemberRole(ctx context.Context, req *connect.Request[pptsv1.SetMemberRoleRequest]) (*connect.Response[pptsv1.SetMemberRoleResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if s.members == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("members not configured"))
	}
	if err := requireRole(ctx, s.members, membership.RoleAdmin); err != nil {
		return nil, err
	}
	userID := strings.TrimSpace(req.Msg.GetUserId())
	role, err := roleDomain(req.Msg.GetRole())
	if err != nil {
		return nil, err
	}
	if userID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("user_id is required"))
	}
	if err := s.requireOwnerForOwnerChange(ctx, p.TenantID, role); err != nil {
		return nil, err
	}
	if err := s.members.SetRole(ctx, p.TenantID, userID, role); err != nil {
		return nil, tenantError(err)
	}
	return connect.NewResponse(&pptsv1.SetMemberRoleResponse{Member: &pptsv1.Member{UserId: userID, Role: roleProto(role)}}), nil
}

func (s *TenantService) RemoveMember(ctx context.Context, req *connect.Request[pptsv1.RemoveMemberRequest]) (*connect.Response[pptsv1.RemoveMemberResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if s.members == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("members not configured"))
	}
	if err := requireRole(ctx, s.members, membership.RoleAdmin); err != nil {
		return nil, err
	}
	userID := strings.TrimSpace(req.Msg.GetUserId())
	if userID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("user_id is required"))
	}
	role, err := s.members.GetRole(ctx, p.TenantID, userID)
	if err != nil {
		return nil, tenantError(err)
	}
	if err := s.requireOwnerForOwnerChange(ctx, p.TenantID, role); err != nil {
		return nil, err
	}
	if err := s.members.Remove(ctx, p.TenantID, userID); err != nil {
		return nil, tenantError(err)
	}
	return connect.NewResponse(&pptsv1.RemoveMemberResponse{}), nil
}

func (s *TenantService) requireOwnerForOwnerChange(ctx context.Context, tenantID string, targetRole membership.Role) error {
	if targetRole != membership.RoleOwner {
		return nil
	}
	if err := requireRole(ctx, s.members, membership.RoleOwner); err != nil {
		return connect.NewError(connect.CodePermissionDenied, errors.New("owner role changes require owner"))
	}
	return nil
}

func roleDomain(r pptsv1.Role) (membership.Role, error) {
	switch r {
	case pptsv1.Role_ROLE_OWNER:
		return membership.RoleOwner, nil
	case pptsv1.Role_ROLE_ADMIN:
		return membership.RoleAdmin, nil
	case pptsv1.Role_ROLE_EDITOR:
		return membership.RoleEditor, nil
	case pptsv1.Role_ROLE_REVIEWER:
		return membership.RoleReviewer, nil
	case pptsv1.Role_ROLE_VIEWER:
		return membership.RoleViewer, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("role is required"))
	}
}

func roleProto(r membership.Role) pptsv1.Role {
	switch r {
	case membership.RoleOwner:
		return pptsv1.Role_ROLE_OWNER
	case membership.RoleAdmin:
		return pptsv1.Role_ROLE_ADMIN
	case membership.RoleEditor:
		return pptsv1.Role_ROLE_EDITOR
	case membership.RoleReviewer:
		return pptsv1.Role_ROLE_REVIEWER
	case membership.RoleViewer:
		return pptsv1.Role_ROLE_VIEWER
	default:
		return pptsv1.Role_ROLE_UNSPECIFIED
	}
}

func (s *TenantService) Quota(ctx context.Context, _ *connect.Request[pptsv1.GetQuotaRequest]) (*connect.Response[pptsv1.TenantQuota], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	quota, err := s.usage.GetQuota(ctx, p.TenantID, usage.KindGenSeconds)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	policy, err := s.policy.GetPolicy(ctx, p.TenantID)
	if err != nil {
		return nil, tenantError(err)
	}
	monthly := int64(quota.LimitUnits)
	if quota.Unlimited() {
		monthly = -1
	}
	return connect.NewResponse(&pptsv1.TenantQuota{
		MonthlySeconds:    monthly,
		UsedSeconds:       int64(quota.ConsumedUnits),
		MaxConcurrentJobs: int64(policy.MaxConcurrentJobs),
		MaxStorageBytes:   policy.MaxStorageBytes,
	}), nil
}

func (s *TenantService) Usage(ctx context.Context, req *connect.Request[pptsv1.GetUsageRequest]) (*connect.Response[pptsv1.GetUsageResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	month := strings.TrimSpace(req.Msg.GetMonth())
	seconds, userAmount, supplierCost, err := s.usage.UsageSummary(ctx, p.TenantID, month)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&pptsv1.GetUsageResponse{
		SecondsUsed:  int64(seconds),
		UserAmount:   userAmount,
		SupplierCost: supplierCost,
		Currency:     s.usage.Currency(),
	}), nil
}

func (s *TenantService) ProjectUsage(ctx context.Context, req *connect.Request[pptsv1.GetProjectUsageRequest]) (*connect.Response[pptsv1.GetProjectUsageResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	// 方案 A：单项目用量属管理态（可见全租户任意项目用量），限 admin/owner 并记审计。
	if err := requireRole(ctx, s.members, membership.RoleAdmin); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(req.Msg.GetProjectId())
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id is required"))
	}
	usage, err := s.usage.ProjectUsage(ctx, p.TenantID, projectID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if s.audit != nil {
		_ = s.audit.Record(ctx, audit.Event{
			TenantID: p.TenantID, ActorUser: p.UserID, Action: "project.admin_override_usage",
			ResourceType: "project", ResourceID: projectID, Metadata: map[string]any{"surface": "rpc.ProjectUsage"},
		})
	}
	return connect.NewResponse(&pptsv1.GetProjectUsageResponse{
		ProjectId:    usage.ProjectID,
		Seconds:      int64(usage.Seconds),
		JobCount:     usage.JobCount,
		UserAmount:   usage.UserAmount,
		SupplierCost: usage.SupplierCost,
		Currency:     usage.Currency,
	}), nil
}

func (s *TenantService) StorageUsage(ctx context.Context, _ *connect.Request[pptsv1.GetStorageUsageRequest]) (*connect.Response[pptsv1.GetStorageUsageResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if s.storage == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("storage usage not configured"))
	}
	usage, err := s.storage.StorageUsage(ctx, p.TenantID)
	if err != nil {
		return nil, tenantError(err)
	}
	return connect.NewResponse(&pptsv1.GetStorageUsageResponse{
		SourceBytes:     usage.SourceBytes,
		ArtifactBytes:   usage.ArtifactBytes,
		TotalBytes:      usage.TotalBytes,
		SourceObjects:   usage.SourceObjects,
		ArtifactObjects: usage.ArtifactObjects,
		OtherBytes:      usage.OtherBytes,
		OtherObjects:    usage.OtherObjects,
	}), nil
}

func (s *TenantService) Policy(ctx context.Context, _ *connect.Request[pptsv1.GetPolicyRequest]) (*connect.Response[pptsv1.TenantPolicy], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	policy, err := s.policy.GetPolicy(ctx, p.TenantID)
	if err != nil {
		return nil, tenantError(err)
	}
	return connect.NewResponse(&pptsv1.TenantPolicy{
		StorageBackend:           policy.StorageBackend,
		StorageRegion:            policy.StorageRegion,
		SourceRetentionDays:      int32(policy.SourceRetentionDays),
		EnvelopeEncryption:       policy.EnvelopeEncryption,
		DeleteSourceAfterDefault: policy.DeleteSourceAfterDefault,
		StorageTransitionDays:    int32(policy.StorageTransitionDays),
		StorageExpirationDays:    int32(policy.StorageExpirationDays),
	}), nil
}

func (s *TenantService) ListAuditEvents(ctx context.Context, req *connect.Request[pptsv1.ListAuditEventsRequest]) (*connect.Response[pptsv1.ListAuditEventsResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if s.audit == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("audit not configured"))
	}
	if s.members == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("members not configured"))
	}
	if err := requireRole(ctx, s.members, membership.RoleAdmin); err != nil {
		return nil, err
	}
	filter := audit.Filter{
		Action:       strings.TrimSpace(req.Msg.GetAction()),
		ResourceType: strings.TrimSpace(req.Msg.GetResourceType()),
		Limit:        int(req.Msg.GetPageSize()),
	}
	if req.Msg.GetSinceUnix() > 0 {
		filter.Since = time.Unix(req.Msg.GetSinceUnix(), 0)
	}
	events, err := s.audit.List(ctx, p.TenantID, filter)
	if err != nil {
		return nil, tenantError(err)
	}
	out := make([]*pptsv1.AuditEvent, 0, len(events))
	for _, e := range events {
		metadata := "{}"
		if e.Metadata != nil {
			data, err := json.Marshal(e.Metadata)
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, err)
			}
			metadata = string(data)
		}
		out = append(out, &pptsv1.AuditEvent{
			Id: e.ID, ActorUser: e.ActorUser, Action: e.Action, ResourceType: e.ResourceType,
			ResourceId: e.ResourceID, MetadataJson: metadata, CreatedAtUnix: e.CreatedAt.Unix(),
		})
	}
	return connect.NewResponse(&pptsv1.ListAuditEventsResponse{Events: out}), nil
}

func (s *TenantService) ListAuditArchives(ctx context.Context, req *connect.Request[pptsv1.ListAuditArchivesRequest]) (*connect.Response[pptsv1.ListAuditArchivesResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if s.archive == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("audit archive not configured"))
	}
	if s.members == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("members not configured"))
	}
	if err := requireRole(ctx, s.members, membership.RoleAdmin); err != nil {
		return nil, err
	}
	files, err := s.archive.ListAuditArchives(ctx, p.TenantID, int(req.Msg.GetLimit()))
	if err != nil {
		return nil, tenantError(err)
	}
	out := make([]*pptsv1.AuditArchiveFile, 0, len(files))
	for _, f := range files {
		out = append(out, &pptsv1.AuditArchiveFile{
			ObjectKey:     f.ObjectKey,
			SizeBytes:     f.SizeBytes,
			UpdatedAtUnix: f.UpdatedAt.Unix(),
		})
	}
	return connect.NewResponse(&pptsv1.ListAuditArchivesResponse{Files: out}), nil
}

func (s *TenantService) ExportTenant(ctx context.Context, _ *connect.Request[pptsv1.ExportTenantRequest]) (*connect.Response[pptsv1.ExportTenantResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if s.lifecycle == nil || s.objects == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("tenant lifecycle not configured"))
	}
	if err := s.requireOwner(ctx); err != nil {
		return nil, err
	}
	manifest, err := s.lifecycle.ExportTenant(ctx, p.TenantID, s.objects)
	if err != nil {
		return nil, tenantError(err)
	}
	files := make([]*pptsv1.ExportFile, 0, len(manifest.Files))
	for _, f := range manifest.Files {
		files = append(files, &pptsv1.ExportFile{Table: f.Table, ObjectKey: f.ObjectKey, Rows: f.Rows})
	}
	return connect.NewResponse(&pptsv1.ExportTenantResponse{
		ManifestObjectKey: manifest.ManifestKey,
		Files:             files,
		ExportedAtUnix:    manifest.ExportedAt.Unix(),
	}), nil
}

func (s *TenantService) PurgeTenant(ctx context.Context, _ *connect.Request[pptsv1.PurgeTenantRequest]) (*connect.Response[pptsv1.PurgeTenantResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if s.lifecycle == nil || s.objects == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("tenant lifecycle not configured"))
	}
	if err := s.requireOwner(ctx); err != nil {
		return nil, err
	}
	deleted, err := s.lifecycle.PurgeTenant(ctx, p.TenantID, s.objects)
	if err != nil {
		return nil, tenantError(err)
	}
	return connect.NewResponse(&pptsv1.PurgeTenantResponse{DeletedRows: deleted}), nil
}

// requireOwner 要求当前身份为租户 owner（数据擦除/导出等控制面操作）。
func (s *TenantService) requireOwner(ctx context.Context) error {
	if s.members == nil {
		return connect.NewError(connect.CodeUnimplemented, errors.New("members not configured"))
	}
	return requireRole(ctx, s.members, membership.RoleOwner)
}

func tenantError(err error) error {
	if errors.Is(err, tenant.ErrTenantNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	if errors.Is(err, membership.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}
