package api

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/tenant"
	"github.com/F31/ppts/internal/usage"
)

// TenantUsageReader 提供租户配额与用量读取（G3-9）。
type TenantUsageReader interface {
	GetQuota(ctx context.Context, tenantID string, kind usage.Kind) (*usage.Quota, error)
	UsageSummary(ctx context.Context, tenantID, month string) (seconds float64, costUnits float64, err error)
}

// TenantPolicyReader 提供租户策略读取。
type TenantPolicyReader interface {
	GetPolicy(ctx context.Context, tenantID string) (*tenant.Policy, error)
}

// TenantService 提供租户配额、用量与策略只读接口（V4.0 §11.1/§12）。
// Members/Roles 依赖 G3-3 身份与角色模型，暂未实现。
type TenantService struct {
	pptsv1connect.UnimplementedTenantServiceHandler
	usage  TenantUsageReader
	policy TenantPolicyReader
}

// NewTenantService 创建租户服务。
func NewTenantService(u TenantUsageReader, p TenantPolicyReader) *TenantService {
	return &TenantService{usage: u, policy: p}
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
	seconds, cost, err := s.usage.UsageSummary(ctx, p.TenantID, month)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&pptsv1.GetUsageResponse{
		SecondsUsed: int64(seconds),
		CostUnits:   int64(cost),
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
	}), nil
}

func tenantError(err error) error {
	if errors.Is(err, tenant.ErrTenantNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}
