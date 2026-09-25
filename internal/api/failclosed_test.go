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

// TestDebugVarsAndMetricsFailClosed 守护 R6（alias 指标端点保护）。
//
// 旧行为有两处「默认值即裸露」：
//   - /debug/vars 直接挂 expvar.Handler()，无鉴权。它不只导出业务计数，还会导出 **完整启动命令行**
//     （可能含以命令行参数传入的密钥）与 memstats；
//   - /metrics 在 PPTS_METRICS_TOKEN 未配置时直接放行，于是"忘记配 token"这最常见的情况
//     恰恰是没有任何保护的那一档。
//
// 两处都改为 fail-closed：默认拒绝，只有显式配置 token（或 PPTS_DEBUG_VARS_PUBLIC）才放行。
func TestDebugVarsAndMetricsFailClosed(t *testing.T) {
	t.Run("no token configured rejects everything", func(t *testing.T) {
		t.Setenv("PPTS_METRICS_TOKEN", "")
		t.Setenv("PPTS_DEBUG_VARS_PUBLIC", "")
		server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{},
			&jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil))
		t.Cleanup(server.Close)

		for _, path := range []string{"/debug/vars", "/metrics"} {
			resp, err := http.Get(server.URL + path)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s 未配置 token 时应拒绝，实际 status=%d", path, resp.StatusCode)
			}
		}
	})

	t.Run("token configured admits bearer only", func(t *testing.T) {
		t.Setenv("PPTS_METRICS_TOKEN", "s3cr3t-token")
		t.Setenv("PPTS_DEBUG_VARS_PUBLIC", "")
		server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{},
			&jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil))
		t.Cleanup(server.Close)

		for _, path := range []string{"/debug/vars", "/metrics"} {
			resp, err := http.Get(server.URL + path)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s 缺少 Bearer 应拒绝，实际 status=%d", path, resp.StatusCode)
			}

			req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer s3cr3t-token")
			resp, err = http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s 带正确 Bearer 应放行，实际 status=%d", path, resp.StatusCode)
			}
		}
	})
}

// TestObjectDeleteRouteOffByDefault 守护 R5 的处置结论。
//
// 先说被推翻的原判定：代理最初认为 PUT/DELETE /ppts/object "无 auth" 是漏洞。复核后不成立——
// 这两个端点走的是预签名直传语义，令牌即授权：HMAC-SHA256 绑定 (tenant, key, op, expiry)，
// 签发侧还校验了 key 的租户前缀（internal/integrations/objectstore/sign.go）。浏览器直传时
// 不携带会话凭证，若强行套 session auth，上传会整体失败。
//
// 但 DELETE 确实留着一个没人用的写面：全仓没有任何签发方产出 OpDelete 令牌（S3 后端显式
// 返回 ErrOperationNotSupported），即这条能力当前无人使用。因此改为默认不挂载，
// 需 PPTS_OBJECT_ALLOW_DELETE=true 显式开启——不是删除代码，是不留默认攻击面。
func TestObjectDeleteRouteOffByDefault(t *testing.T) {
	t.Run("disabled without opt-in", func(t *testing.T) {
		t.Setenv("PPTS_OBJECT_ALLOW_DELETE", "")
		server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{},
			&jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil))
		t.Cleanup(server.Close)

		// Go 1.22 ServeMux：同路径已注册 GET/PUT，未注册的 DELETE 得到 405（而非 404）——
		// 405 恰恰是"路由不存在于该方法"的准确语义，比 404 更能说明问题是路由而非资源。
		req, _ := http.NewRequest(http.MethodDelete, server.URL+"/ppts/object/tenant-1/x/y.txt", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("DELETE 路由默认不应挂载，实际 status=%d", resp.StatusCode)
		}
	})

	t.Run("enabled with explicit opt-in", func(t *testing.T) {
		t.Setenv("PPTS_OBJECT_ALLOW_DELETE", "true")
		server := httptest.NewServer(NewHandler(&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{},
			&jobCreatorStub{}, &fakeArtifactStore{}, testObjects(t), nil))
		t.Cleanup(server.Close)

		// 路由挂载后会走到验签：无 token 应得到 400（而非 404 未注册）。
		req, _ := http.NewRequest(http.MethodDelete, server.URL+"/ppts/object/tenant-1/x/y.txt", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("显式开启后 DELETE 应挂载并验签，实际 status=%d", resp.StatusCode)
		}
	})
}
