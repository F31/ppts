// testdata/fixture_audit.go 是被 Scan 用例扫描的样本：它不属于任何构建目标
// （testdata 目录被 go tool 忽略），因此可以自由地写"看起来像坏代码"的样本。
//
// 这里刻意覆盖三种埋点写法 —— extractor 少认一种，就会漏掉整整一类真实埋点。
package fixture

import (
	"context"

	audit "example.com/audit"
)

type Principal struct {
	TenantID string
	UserID   string
}

type Recorder interface {
	Record(ctx context.Context, e audit.Event) error
}

// 样式一：直接构造事件，Action 写字面量。
func directLiteral(ctx context.Context, rec Recorder, p Principal) {
	_ = rec.Record(ctx, audit.Event{
		TenantID: p.TenantID, ActorUser: p.UserID, Action: "widget.create",
		ResourceType: "widget", ResourceID: "w-1",
	})
}

// 样式二：Action 用包级常量。
const actionWidgetDelete = "widget.delete"

func viaConst(ctx context.Context, rec Recorder, p Principal) {
	_ = rec.Record(ctx, audit.Event{
		TenantID: p.TenantID, ActorUser: p.UserID, Action: actionWidgetDelete,
		ResourceType: "widget", ResourceID: "w-1",
	})
}

// 样式三：动作名经助手形参下传 —— 埋点位置与动作名分处两个函数（真实项目里常分处两个文件），
// 肉眼搜动作名字符串搜不到，只能靠"哪个形参被原样放进 Action"来认。
func record(ctx context.Context, rec Recorder, tenantID, actor, action, resourceID string) {
	if rec == nil {
		return
	}
	_ = rec.Record(ctx, audit.Event{
		TenantID: tenantID, ActorUser: actor, Action: action,
		ResourceType: "widget", ResourceID: resourceID,
	})
}

func viaHelper(ctx context.Context, rec Recorder, p Principal) {
	record(ctx, rec, p.TenantID, p.UserID, "widget.update", "w-1")
	record(ctx, rec, p.TenantID, p.UserID, actionWidgetDelete, "w-2")
}

// 动态动作：既没传字面量也没传常量 —— 必须退回占位值，而不是默默解析成空串，
// 否则"没解析出来"和"动作名真的是空串"会长得一模一样。
func dynamic(ctx context.Context, rec Recorder, p Principal, action string) {
	record(ctx, rec, p.TenantID, p.UserID, action, "w-3")
	_ = rec.Record(ctx, audit.Event{
		TenantID: p.TenantID, ActorUser: p.UserID, Action: action,
		ResourceType: "widget", ResourceID: "w-4",
	})
}
