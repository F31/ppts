package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/share"
)

// 匿名成品分享（M6-3）。落点与项目分享（/shared/{token}）刻意分开：
// 那个返回的是「动态播放清单」，这个返回的是「某一版成片的包内条目」。
//
//   - GET  /s/{token}              → 302 到 /s/{token}/
//   - GET  /s/{token}/             → index.html（或口令门）
//   - GET  /s/{token}/{path...}    → 包内单条目
//   - POST /s/{token}/unlock       → 校验口令，种会话 cookie
//
// 为什么必须重定向到带尾斜杠的形式：index.html 里的资源引用全是相对路径
// （slides/001.png、app.js）。若停在 /s/{token}，浏览器会把它们解析到 /s/ 下，
// 于是整页 404——这是把静态包搬到 HTTP 上最容易踩且最难看出原因的一步。

const (
	// artifactShareCookie 是口令解锁后的会话 cookie。值形如 `<linkID>.<expUnix>.<hmac>`，
	// **不含口令本身**：cookie 会被浏览器持久化，把口令放进去等于让口令的寿命
	// 超过「这一次观看」。
	artifactShareCookie = "ppts_share"
	// artifactShareCookieTTL 是解锁会话的有效期。够看完一场讲解，又不至于让
	// 「撤销链接」在已解锁的浏览器里长期失灵（cookie 只在口令校验处使用，
	// 链接本身的有效性每次仍回查数据库）。
	artifactShareCookieTTL = 2 * time.Hour

	// artifactShareActionAnonAccess 与项目分享的 share_link.anonymous_access 分开：
	// 「有人看了这个项目」与「有人看了这一版成片」是两条不同的运营事实，
	// 混在一个 action 里会让按资源类型统计时把两者加在一起。
	artifactShareActionAnonAccess = "artifact_share.anonymous_access"
)

func registerArtifactShareAnonRoutes(
	mux *http.ServeMux,
	links artifact.ShareLinkStore,
	objects objectstore.ObjectStore,
	pepper string,
	recorder audit.Recorder,
) {
	source := share.NewBundleSource(func(ctx context.Context, key string) ([]byte, error) {
		parsed, err := objectstore.Parse(key)
		if err != nil {
			return nil, err
		}
		rc, _, err := objects.Get(ctx, parsed)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		// zip.NewReader 需要 ReaderAt，而对象存储给的是不可 seek 的流，只能先落成 []byte。
		// 读取上限比缓存上限多 1 字节：刚好让 BundleSource 能判断"超了"，
		// 又不至于让一个异常大的对象把内存吃干（多读的那 1 字节就是判据本身）。
		return io.ReadAll(io.LimitReader(rc, share.DefaultMaxBundleBytes+1))
	})

	mux.HandleFunc("GET /s/{token}", func(w http.ResponseWriter, r *http.Request) {
		target := "/s/" + url.PathEscape(r.PathValue("token")) + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound)
	})
	mux.HandleFunc("POST /s/{token}/unlock", func(w http.ResponseWriter, r *http.Request) {
		unlockArtifactShare(w, r, links, pepper, recorder)
	})
	mux.HandleFunc("GET /s/{token}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		serveArtifactShareEntry(w, r, links, source, pepper, recorder)
	})
}

// setShareHeaders 给 /s/ 下所有响应加安全头（design ADR-7）。
func setShareHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'self' 'unsafe-inline'; "+
			"script-src 'self'; font-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// private：允许同一浏览器缓存（同一 token 的包内容不变），
	// 但禁止 CDN/代理共享缓存——否则一条 token 的响应可能被交给另一条 token 的请求。
	w.Header().Set("Cache-Control", "private, max-age=300")
}

func shareNotFound(w http.ResponseWriter) {
	http.Error(w, "share link not found", http.StatusNotFound)
}

// serveArtifactShareEntry 是匿名读的主路径。
func serveArtifactShareEntry(
	w http.ResponseWriter, r *http.Request,
	links artifact.ShareLinkStore, source *share.BundleSource,
	pepper string, recorder audit.Recorder,
) {
	setShareHeaders(w)

	link, err := links.GetShareLinkByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		shareNotFound(w)
		return
	}
	name := r.PathValue("path")
	if name == "" {
		name = share.FileIndexHTML
	}

	// 口令门只在入口文档上渲染。子资源没有"再输一次口令"的界面，
	// 未解锁时直接 404，与链接无效同码——那里没有可供用户判断的上下文。
	if link.PasswordProtected && !shareCookieMatches(r, link.ID, pepper) {
		if name == share.FileIndexHTML {
			renderShareGate(w, r.PathValue("token"), http.StatusOK, "")
			return
		}
		shareNotFound(w)
		return
	}

	// 内容位置取自**链接行上的快照**，不是 artifacts 表。artifacts 的 upsert 会原地改写
	// object_key（同一快照重新导出），照着它走就等于让旧链接悄悄换了内容。
	//
	// 仍按租户前缀校验一次：链接行若被跨租户写脏，这里会拒绝而不是把别人的包吐出去。
	key, err := objectstore.Parse(link.ObjectKey)
	if err != nil || key.EnsureTenant(link.TenantID) != nil {
		http.Error(w, "bundle unavailable", http.StatusInternalServerError)
		return
	}

	entry, err := source.ReadEntry(r.Context(), key.String(), link.ContentHash, name)
	if err != nil {
		switch {
		case errors.Is(err, share.ErrBundleEntryNotFound):
			// 不回退到 index.html：回退会让 slides/999.png 拿到一页 HTML，
			// <img> 静默裂图，比 404 更难查。
			shareNotFound(w)
		case errors.Is(err, share.ErrBundleTooLarge), errors.Is(err, share.ErrBundleInvalid):
			http.Error(w, "bundle unavailable", http.StatusInternalServerError)
		default:
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		}
		return
	}

	w.Header().Set("Content-Type", shareContentType(entry.Name))
	w.Header().Set("Content-Length", strconv.Itoa(len(entry.Data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(entry.Data)

	// 审计与访问时间只在**入口文档**上记：一页 20 张切片，逐个记会把一次观看
	// 放大成几十条审计，既冲淡信噪比，也给了匿名端一个写入放大杠杆。
	if entry.Name == share.FileIndexHTML {
		_ = links.TouchShareLinkAccess(r.Context(), link.TenantID, link.ID)
		recordArtifactShareAccess(r.Context(), recorder, link, "shared.bundle")
	}
}

func shareContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".srt":
		return "application/x-subrip; charset=utf-8"
	case ".vtt":
		return "text/vtt; charset=utf-8"
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}
	// 未知类型一律二进制流：绝不让未知条目有机会被浏览器当 HTML 执行。
	return "application/octet-stream"
}

// ─── 口令门与 cookie ─────────────────────────────────────────────────────────

// shareGateHTML 是自包含的口令门。没有脚本、没有外链——它要在
// `default-src 'none'` 下也能正常显示与提交。
const shareGateHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>需要口令</title>
<style>
:root{color-scheme:light dark}
body{margin:0;min-height:100vh;display:grid;place-items:center;
  font:15px/1.6 -apple-system,"Segoe UI",system-ui,"Noto Sans SC",sans-serif;
  background:#f6f7f9;color:#1b1f24}
main{width:min(360px,86vw);padding:28px 24px;background:#fff;border-radius:14px;
  box-shadow:0 8px 28px rgba(16,24,40,.10);text-align:center}
h1{margin:0 0 6px;font-size:17px;font-weight:600}
p{margin:0 0 18px;color:#5b6472;font-size:13px}
input{width:100%%;box-sizing:border-box;padding:10px 12px;font-size:14px;
  border:1px solid #d5dae1;border-radius:8px;outline:none}
input:focus{border-color:#4c7cf3;box-shadow:0 0 0 3px rgba(76,124,243,.15)}
button{margin-top:14px;width:100%%;padding:10px 12px;font-size:14px;font-weight:600;
  color:#fff;background:#3559e0;border:0;border-radius:8px;cursor:pointer}
button:hover{background:#2b4ac4}
.err{margin:0 0 14px;padding:8px 10px;border-radius:8px;background:#fdecec;
  color:#b42318;font-size:13px}
@media (prefers-color-scheme:dark){
  body{background:#14171c;color:#e6e9ee}
  main{background:#1c2027;box-shadow:none}
  p{color:#98a2b3}
  input{background:#14171c;border-color:#333b47;color:#e6e9ee}
  .err{background:#3a1d1d;color:#ffb4ab}
}
</style></head>
<body><main>
<h1>此分享需要口令</h1>
<p>请输入分享者提供的口令</p>
%s
<form method="post" action="/s/%s/unlock">
  <input type="password" name="password" autofocus required autocomplete="current-password" placeholder="口令">
  <button type="submit">打开</button>
</form>
</main></body></html>`

func renderShareGate(w http.ResponseWriter, token string, status int, errMsg string) {
	// token 来自 URL 路径，插入 action 前必须转义两次：一次给 HTML 属性，一次给 URL 路径。
	action := html.EscapeString(url.PathEscape(token))
	notice := ""
	if errMsg != "" {
		notice = `<p class="err">` + html.EscapeString(errMsg) + `</p>`
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, shareGateHTML, notice, action)
}

// unlockArtifactShare 校验口令并种会话 cookie。
//
// 口令错误返回 401 并重渲染口令门（含错误提示），而不是与「链接不存在」同一 404。
// 理由：口令门**只在 token 有效时**渲染，也就是说「链接存在」在页面出现的那一刻
// 已经公开了；再拿 404 去掩盖「口令错」并不能多藏住任何东西，只会让用户以为链接坏了。
// 子资源请求仍对未解锁返回 404（那里没有可读的上下文），见 serveArtifactShareEntry。
func unlockArtifactShare(w http.ResponseWriter, r *http.Request, links artifact.ShareLinkStore,
	pepper string, recorder audit.Recorder) {
	setShareHeaders(w)
	token := r.PathValue("token")

	link, err := links.GetShareLinkByToken(r.Context(), token)
	if err != nil {
		shareNotFound(w)
		return
	}
	if err := r.ParseForm(); err != nil {
		renderShareGate(w, token, http.StatusBadRequest, "请求格式有误")
		return
	}
	if link.PasswordProtected {
		hash, err := links.ShareLinkPasswordHash(r.Context(), link.TenantID, link.ID)
		if err != nil || hash == "" || !verifyPassword(hash, r.PostFormValue("password"), pepper) {
			renderShareGate(w, token, http.StatusUnauthorized, "口令不正确")
			return
		}
	}
	exp := time.Now().Add(artifactShareCookieTTL)
	if link.ExpiresAt != nil && link.ExpiresAt.Before(exp) {
		// cookie 不应该比链接本身活得更久。
		exp = *link.ExpiresAt
	}
	http.SetCookie(w, &http.Cookie{
		Name:     artifactShareCookie,
		Value:    shareCookieValue(link.ID, exp, pepper),
		Path:     "/s/" + token,
		Expires:  exp,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// 不信任 X-Forwarded-Proto：客户端可以伪造它，把 cookie 标成 Secure 之后
		// 在明文连接上会被浏览器丢弃，表现成"口令永远不对"。只认 TLS 与显式配置。
		Secure: requestIsHTTPS(r, false),
	})
	recordArtifactShareAccess(r.Context(), recorder, link, "shared.unlock")
	http.Redirect(w, r, "/s/"+url.PathEscape(token)+"/", http.StatusSeeOther)
}

func shareCookieValue(linkID string, exp time.Time, pepper string) string {
	payload := linkID + "." + strconv.FormatInt(exp.Unix(), 10)
	return payload + "." + shareCookieSig(payload, pepper)
}

func shareCookieSig(payload, pepper string) string {
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// shareCookieMatches 校验 cookie：签名、有效期、以及 linkID 是否就是这条 token 的链接。
//
// 第三项不能省：cookie 的 Path 是客户端指令，客户端完全可以把 A 链接的 cookie
// 发给 B 链接的路径，签名只证明"这个值是我们签的"，不证明"它属于这条链接"。
func shareCookieMatches(r *http.Request, linkID, pepper string) bool {
	c, err := r.Cookie(artifactShareCookie)
	if err != nil || c.Value == "" {
		return false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 {
		return false
	}
	if !hmac.Equal([]byte(parts[2]), []byte(shareCookieSig(parts[0]+"."+parts[1], pepper))) {
		return false
	}
	if parts[0] != linkID {
		return false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() >= exp {
		return false
	}
	return true
}

// recordArtifactShareAccess 记一次匿名访问。
//
// 与项目分享同一条脱敏纪律：**不记 token、不记 IP、不记 UA**。token 就是钥匙，
// 写进审计等于把分享地址永久留在比分享有效期更长的留存里。
func recordArtifactShareAccess(ctx context.Context, recorder audit.Recorder, link *artifact.ShareLink, surface string) {
	if recorder == nil || link == nil || !anonymousAccessAudited() {
		return
	}
	_ = recorder.Record(ctx, audit.Event{
		TenantID: link.TenantID, ActorUser: "anonymous", Action: artifactShareActionAnonAccess,
		ResourceType: "artifact_share_link", ResourceID: link.ID,
		Metadata: map[string]any{
			"surface":     surface,
			"project_id":  link.ProjectID,
			"artifact_id": link.ArtifactID,
		},
	})
}
