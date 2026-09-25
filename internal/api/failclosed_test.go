package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	pptsv1connect "github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
)

// TestHandlerDevHeadersRequireExplicitOptIn 守护 fail-closed（Phase 0.2）：
// 即使没有配置任何 Authenticator，也必须拒绝未经签名的 X-PPTS-Tenant-ID / X-PPTS-User-ID 头，
// 除非调用方显式传入 Options{DevHeaders: true}。
//
// 被替换掉的旧实现是 `allowDevHeaders := opt.Auth == nil || opt.DevHeaders`，
// 它把"没有配认证"当作"允许开发头"的充分条件——后果是生产漏配 PPTS_JWT_SECRET 时，
// 任何人带上两个未签名请求头即可冒充任意租户的任意用户，且全程无告警、无错误。
//
// 注意：本文件刻意保持 NewHandler 调用**不带** DevHeaders。若将来要对测试做
// 批量参数注入（例如给所有 NewHandler 补 DevHeaders），必须排除本文件，否则这条守护会失效。
func TestHandlerDevHeadersRequireExplicitOptIn(t *testing.T) {
	server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil))
	t.Cleanup(server.Close)
	client := pptsv1connect.NewProjectServiceClient(http.DefaultClient, server.URL)

	// authRequest 会带上 tenant/user 两个未签名头：这正是旧实现会放行的输入。
	_, err := client.Create(context.Background(), authRequest(&pptsv1.CreateProjectRequest{Title: "spoofed"}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("未显式开启 DevHeaders 却接受了未签名身份头：code=%v err=%v", connect.CodeOf(err), err)
	}
}
