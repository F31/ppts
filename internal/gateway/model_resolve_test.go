package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/F31/ppts/internal/db"
)

// newModelGateway 造一行可区分来源的 TTS 网关配置。
// baseURL 带上模型名，是为了在断言里能看出"解析到的是哪一行"，
// 而不是只比一个可能恰好相等的 Model 字段。
func newModelGateway(tenantID, name, model string, isDefault bool) *Gateway {
	gw := newTestGateway(tenantID, true)
	gw.Name = name
	gw.Model = model
	gw.BaseURL = "https://" + model + ".example.com/v1"
	gw.IsDefault = isDefault
	return gw
}

// TestResolveModelPicksNamedModel 守护"选了哪个模型就用哪个模型"。
//
// 这是本次修复的核心断言：默认行是 model-a，用户点名 model-b，解析结果必须换成 model-b。
// 若实现里图省事直接 store.Resolve 拿默认行，这条会红——而这正是修复前的真实行为
// （界面上的模型下拉只改了显示，生成仍走默认网关）。
func TestResolveModelPicksNamedModel(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteGatewayStore(t)
	const tenantID = db.LocalTenantID

	if err := store.Create(ctx, newModelGateway(tenantID, "gw-a", "model-a", true), "sk-a"); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if err := store.Create(ctx, newModelGateway(tenantID, "gw-b", "model-b", false), "sk-b"); err != nil {
		t.Fatalf("create b: %v", err)
	}

	cfg, err := ResolveModel(ctx, store, tenantID, KindTTS, "model-b")
	if err != nil {
		t.Fatalf("ResolveModel(model-b): %v", err)
	}
	if cfg.Model != "model-b" {
		t.Fatalf("Model = %q want model-b（按名解析没生效，仍解析到默认行）", cfg.Model)
	}
	if cfg.Name != "gw-b" {
		t.Fatalf("Name = %q want gw-b", cfg.Name)
	}
	if cfg.APIKey != "sk-b" {
		t.Fatalf("APIKey = %q want sk-b（配置行与凭据错配）", cfg.APIKey)
	}
}

// TestResolveModelByGatewayName 守护第二匹配维度：下拉里的值可能是网关名称而非模型名。
// 真实部署里网关常按用途命名（如 "main-tts"），Model 字段反而不唯一。
func TestResolveModelByGatewayName(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteGatewayStore(t)
	const tenantID = db.LocalTenantID

	if err := store.Create(ctx, newModelGateway(tenantID, "gw-main", "model-a", true), "sk-a"); err != nil {
		t.Fatalf("create: %v", err)
	}

	cfg, err := ResolveModel(ctx, store, tenantID, KindTTS, "gw-main")
	if err != nil {
		t.Fatalf("ResolveModel(gw-main): %v", err)
	}
	if cfg.Model != "model-a" {
		t.Fatalf("Model = %q want model-a", cfg.Model)
	}
}

// TestResolveModelUnknownDoesNotFallBack 是本函数存在的理由。
//
// 用户点名一个不存在的模型（下拉里选了已停用的那行、或客户端拼错），必须报错。
// 若静默回落到默认模型，合成会"成功"、音频时长正常、任务状态 SUCCEEDED，
// 唯一异常是声音不对——而且没有任何一处日志会说它换了模型。
func TestResolveModelUnknownDoesNotFallBack(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteGatewayStore(t)
	const tenantID = db.LocalTenantID

	if err := store.Create(ctx, newModelGateway(tenantID, "gw-a", "model-a", true), "sk-a"); err != nil {
		t.Fatalf("create: %v", err)
	}

	cfg, err := ResolveModel(ctx, store, tenantID, KindTTS, "model-missing")
	if !errors.Is(err, ErrModelNotConfigured) {
		t.Fatalf("err = %v want ErrModelNotConfigured", err)
	}
	if cfg != nil {
		t.Fatalf("cfg = %+v want nil（不得回落默认行）", cfg)
	}
}

// TestResolveModelSkipsDisabledRow 守护"停用的行不能被选中"。
// 停用是用户表达"别再用了"的唯一手段，若解析忽略 enabled，停用就只是界面上的一行灰字。
func TestResolveModelSkipsDisabledRow(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteGatewayStore(t)
	const tenantID = db.LocalTenantID

	disabled := newModelGateway(tenantID, "gw-old", "model-old", false)
	disabled.Enabled = false
	if err := store.Create(ctx, disabled, "sk-old"); err != nil {
		t.Fatalf("create disabled: %v", err)
	}
	if err := store.Create(ctx, newModelGateway(tenantID, "gw-a", "model-a", true), "sk-a"); err != nil {
		t.Fatalf("create default: %v", err)
	}

	if _, err := ResolveModel(ctx, store, tenantID, KindTTS, "model-old"); !errors.Is(err, ErrModelNotConfigured) {
		t.Fatalf("err = %v want ErrModelNotConfigured（停用行被选中了）", err)
	}
}

// TestResolveModelEmptyUsesDefault 固化向后兼容：不传模型的老客户端行为不变。
func TestResolveModelEmptyUsesDefault(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteGatewayStore(t)
	const tenantID = db.LocalTenantID

	if err := store.Create(ctx, newModelGateway(tenantID, "gw-a", "model-a", true), "sk-a"); err != nil {
		t.Fatalf("create: %v", err)
	}

	cfg, err := ResolveModel(ctx, store, tenantID, KindTTS, "")
	if err != nil {
		t.Fatalf("ResolveModel(\"\"): %v", err)
	}
	if cfg.Model != "model-a" {
		t.Fatalf("Model = %q want model-a", cfg.Model)
	}
}

// TestResolveModelTenantOverridesPlatform 守护优先级：租户自己配的同名模型优先。
// 若反过来，私有化客户自建的网关会被平台默认行盖住——配置改了却一点效果都没有。
func TestResolveModelTenantOverridesPlatform(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteGatewayStore(t)
	const tenantID = db.LocalTenantID

	if err := store.Create(ctx, newModelGateway(PlatformTenantID, "platform-gw", "shared-model", true), "sk-platform"); err != nil {
		t.Fatalf("create platform: %v", err)
	}
	if err := store.Create(ctx, newModelGateway(tenantID, "tenant-gw", "shared-model", false), "sk-tenant"); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	cfg, err := ResolveModel(ctx, store, tenantID, KindTTS, "shared-model")
	if err != nil {
		t.Fatalf("ResolveModel: %v", err)
	}
	if cfg.APIKey != "sk-tenant" {
		t.Fatalf("APIKey = %q want sk-tenant（租户行未覆盖平台行）", cfg.APIKey)
	}
}

// TestResolveModelFallsBackToPlatform 守护另一半：租户没这个模型时仍能用平台共享的。
func TestResolveModelFallsBackToPlatform(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteGatewayStore(t)
	const tenantID = db.LocalTenantID

	if err := store.Create(ctx, newModelGateway(PlatformTenantID, "platform-gw", "shared-model", true), "sk-platform"); err != nil {
		t.Fatalf("create platform: %v", err)
	}

	cfg, err := ResolveModel(ctx, store, tenantID, KindTTS, "shared-model")
	if err != nil {
		t.Fatalf("ResolveModel: %v", err)
	}
	if cfg.Model != "shared-model" {
		t.Fatalf("Model = %q want shared-model", cfg.Model)
	}
}
