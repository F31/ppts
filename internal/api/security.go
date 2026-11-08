package api

import (
	"net/http"
	"strings"

	"github.com/F31/ppts/internal/config"
)

// SecurityHeadersConfig 控制安全响应头。CSP 默认启用；通过环境变量可扩展 connect-src
// （外部 OIDC/埋点）或整体关闭（排障时）。
type SecurityHeadersConfig struct {
	Enabled      bool
	HSTS         bool
	CSP          string
	ConnectExtra []string
}

// securityHeadersFromEnv 按环境变量构建配置。
//   - PPTS_SECURITY_HEADERS=false 关闭全部安全头（仅排障用）；
//   - PPTS_HSTS=true 追加 Strict-Transport-Security（仅在 TLS 终止后开启）；
//   - PPTS_CSP_CONNECT_EXTRA="https://idp.example.com https://api.example.com" 扩展 connect-src。
func securityHeadersFromEnv() SecurityHeadersConfig {
	cfg := SecurityHeadersConfig{
		Enabled: config.Bool("PPTS_SECURITY_HEADERS", true),
		HSTS:    config.Bool("PPTS_HSTS", false),
	}
	// 用空白分隔而不是逗号：CSP 源列表在白名单里本来就是空格分隔的，
	// 让人把同一段值从 nginx 配置抄过来时不必换写法。
	if extra := config.String("PPTS_CSP_CONNECT_EXTRA", ""); extra != "" {
		cfg.ConnectExtra = strings.Fields(extra)
	}
	cfg.CSP = buildCSP(cfg.ConnectExtra)
	return cfg
}

// buildCSP 组装 SPA 可用的保守策略：
//   - 脚本仅同源（vite 产物为外部哈希文件，无需 inline）；
//   - 样式允许 inline（React 内联 style 属性）；字体同源 + data:；
//   - 图片同源 + data:/blob:（页面缩略图、下载）；
//   - 连接同源 + 显式外链（OIDC 令牌端点等）；
//   - 禁止被 iframe 嵌套（frame-ancestors 'none'）。
func buildCSP(connectExtra []string) string {
	connect := []string{"'self'"}
	connect = append(connect, connectExtra...)
	directives := []string{
		"default-src 'self'",
		"base-uri 'self'",
		"object-src 'none'",
		"frame-ancestors 'none'",
		"form-action 'self'",
		"script-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"media-src 'self' blob:",
		"connect-src " + strings.Join(connect, " "),
	}
	return strings.Join(directives, "; ")
}

// securityHeaders 为所有响应追加安全头。Connect RPC 与静态资源同样受益。
func securityHeaders(next http.Handler, cfg SecurityHeadersConfig) http.Handler {
	if !cfg.Enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=(), payment=()")
		if cfg.CSP != "" {
			h.Set("Content-Security-Policy", cfg.CSP)
		}
		if cfg.HSTS {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// SecurityHeaders 以环境变量配置包装 handler（cmd/ppts 装配用）。
func SecurityHeaders(next http.Handler) http.Handler {
	return securityHeaders(next, securityHeadersFromEnv())
}
