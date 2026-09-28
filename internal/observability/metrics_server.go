package observability

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"
)

// MetricsTokenOK 校验请求携带的 Bearer token 与 PPTS_METRICS_TOKEN 一致（fail-closed）。
//
// **未配置 token 时返回 false**：把"忘记配置"当成"内网放行"会让最常见的部署形态
// 裸露指标面；需要内网匿名抓取时应由反向代理承担鉴权。与 api 侧策略同源，
// 供 server / worker 两个进程复用同一判定。
func MetricsTokenOK(r *http.Request) bool {
	tok := strings.TrimSpace(os.Getenv("PPTS_METRICS_TOKEN"))
	if tok == "" {
		return false
	}
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	return subtle.ConstantTimeCompare([]byte(got), []byte(tok)) == 1
}
