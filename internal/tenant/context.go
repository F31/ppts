package tenant

import "context"

type ctxKey struct{}

// WithContext 把已授权租户 ID 附加到 context，供没有显式 tenant 参数的数据访问路径
// （如 worker 的任务续租/完成、播放服务发现步骤结果）建立 RLS 上下文。
func WithContext(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, tenantID)
}

// FromContext 读取 context 中的租户 ID。
func FromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKey{}).(string)
	return v, ok && v != ""
}
