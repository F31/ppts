package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/db"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/project"
	"github.com/F31/ppts/migrations"
)

const shareTestPepper = "test-pepper"

type shareAuditStub struct {
	mu     sync.Mutex
	events []audit.Event
}

func (s *shareAuditStub) Record(_ context.Context, e audit.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

func (s *shareAuditStub) snapshot() []audit.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]audit.Event(nil), s.events...)
}

type shareTestEnv struct {
	server  *httptest.Server
	store   *artifact.SQLiteStore
	objects objectstore.ObjectStore
	audit   *shareAuditStub
	client  *http.Client
}

func newShareTestEnv(t *testing.T) *shareTestEnv {
	t.Helper()
	ctx := context.Background()
	sqldb, err := db.OpenSQLite(ctx, filepath.Join(t.TempDir(), "ppts.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	fsys, err := migrations.SQLite()
	if err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if _, err := db.MigrateSQLite(ctx, sqldb, fsys); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.EnsureLocalIdentity(ctx, sqldb); err != nil {
		t.Fatalf("identity: %v", err)
	}

	store := artifact.NewSQLiteStore(sqldb)
	objects := objectstore.NewLocal(t.TempDir(), []byte("download-secret"))
	recorder := &shareAuditStub{}

	projects := &fakeProjectStore{projects: []*project.Project{
		{ID: "p1", TenantID: db.LocalTenantID, OwnerUser: db.LocalUserID, Title: "项目一"},
		{ID: "p2", TenantID: db.LocalTenantID, OwnerUser: db.LocalUserID, Title: "项目二"},
	}}

	mux := http.NewServeMux()
	auth := func(h http.Handler) http.Handler {
		return AuthMiddlewareWithOptions(h, AuthOptions{
			LocalPrincipal: &Principal{TenantID: db.LocalTenantID, UserID: db.LocalUserID},
		})
	}
	registerArtifactShareRoutes(mux, store, store, projects, nil, shareTestPepper, recorder, auth)
	registerArtifactShareAnonRoutes(mux, store, objects, shareTestPepper, recorder)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &shareTestEnv{
		server: ts, store: store, objects: objects, audit: recorder,
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // 断言 302/303 本身，不自动跟随
		}},
	}
}

// seedBundle 在对象存储写一个最小成品包，并登记一条 html_bundle 成品。
func (e *shareTestEnv) seedBundle(t *testing.T, projectID string) *artifact.Artifact {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string]string{
		"index.html":     "<html><body>INDEX-BODY</body></html>",
		"slides/001.png": "PNG-BYTES",
		"app.js":         "console.log(1)",
	}
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}

	key := objectstore.ObjectKey{
		TenantID: db.LocalTenantID, ProjectID: projectID, Revision: "artifact",
		AssetType: "artifact", AssetID: "bundle", Ext: "zip",
	}
	if err := e.objects.Put(context.Background(), key, bytes.NewReader(buf.Bytes()),
		objectstore.ObjectMeta{ContentType: "application/zip", Size: int64(buf.Len())}); err != nil {
		t.Fatalf("put bundle: %v", err)
	}
	a, err := e.store.Create(context.Background(), db.LocalTenantID, artifact.NewArtifact{
		ProjectID: projectID, SnapshotHash: "snap-" + projectID, Format: artifact.FormatHTMLBundle,
		ObjectKey: key.String(), ContentHash: "hash-" + projectID, SizeBytes: int64(buf.Len()),
	})
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	return a
}

func (e *shareTestEnv) createShare(t *testing.T, projectID, artifactID, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		e.server.URL+"/projects/"+projectID+"/artifacts/"+artifactID+"/shares", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("create share: %v", err)
	}
	return resp
}

func (e *shareTestEnv) do(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// TestArtifactShareLifecycle 走一遍「建链接 → 打开 → 撤销 → 打不开」。
func TestArtifactShareLifecycle(t *testing.T) {
	env := newShareTestEnv(t)
	a := env.seedBundle(t, "p1")

	resp := env.createShare(t, "p1", a.ID, `{}`)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d: %s", resp.StatusCode, body)
	}
	var created struct {
		ShareLink struct {
			Token string `json:"token"`
			URL   string `json:"url"`
			ID    string `json:"id"`
		} `json:"shareLink"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	token := created.ShareLink.Token
	if len(token) != 32 {
		t.Fatalf("token = %q", token)
	}
	if created.ShareLink.URL != "/s/"+token+"/" {
		t.Fatalf("url = %q", created.ShareLink.URL)
	}

	listResp := env.do(t, mustReq(t, http.MethodGet, env.server.URL+"/projects/p1/artifacts/"+a.ID+"/shares"))
	listBody := readBody(t, listResp)
	if listResp.StatusCode != http.StatusOK || !strings.Contains(listBody, token) {
		t.Fatalf("list = %d %s", listResp.StatusCode, listBody)
	}

	// 无尾斜杠必须 302 到带斜杠的形式，否则包内相对路径全部解析错。
	root := env.do(t, mustReq(t, http.MethodGet, env.server.URL+"/s/"+token))
	_ = readBody(t, root)
	if root.StatusCode != http.StatusFound {
		t.Fatalf("root status = %d, want 302", root.StatusCode)
	}
	if loc := root.Header.Get("Location"); loc != "/s/"+token+"/" {
		t.Fatalf("location = %q", loc)
	}

	index := env.do(t, mustReq(t, http.MethodGet, env.server.URL+"/s/"+token+"/"))
	indexBody := readBody(t, index)
	if index.StatusCode != http.StatusOK || !strings.Contains(indexBody, "INDEX-BODY") {
		t.Fatalf("index = %d %s", index.StatusCode, indexBody)
	}
	if csp := index.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("missing CSP: %q", csp)
	}
	if index.Header.Get("X-Content-Type-Options") != "nosniff" || index.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("missing security headers: %+v", index.Header)
	}

	sub := env.do(t, mustReq(t, http.MethodGet, env.server.URL+"/s/"+token+"/slides/001.png"))
	subBody := readBody(t, sub)
	if sub.StatusCode != http.StatusOK || subBody != "PNG-BYTES" {
		t.Fatalf("entry = %d %q", sub.StatusCode, subBody)
	}
	if ct := sub.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q", ct)
	}

	revoke := env.do(t, mustReq(t, http.MethodPost,
		env.server.URL+"/projects/p1/artifacts/"+a.ID+"/shares/"+created.ShareLink.ID+"/revoke"))
	_ = readBody(t, revoke)
	if revoke.StatusCode != http.StatusOK {
		t.Fatalf("revoke status = %d", revoke.StatusCode)
	}
	gone := env.do(t, mustReq(t, http.MethodGet, env.server.URL+"/s/"+token+"/"))
	_ = readBody(t, gone)
	if gone.StatusCode != http.StatusNotFound {
		t.Fatalf("after revoke status = %d, want 404", gone.StatusCode)
	}
}

// TestArtifactShareAnchorsToArtifact 是本变更的门禁核心：
// 项目重新导出（artifacts 行被指到新对象）后，旧链接读到的字节必须一模一样。
func TestArtifactShareAnchorsToArtifact(t *testing.T) {
	env := newShareTestEnv(t)
	a := env.seedBundle(t, "p1")

	resp := env.createShare(t, "p1", a.ID, `{}`)
	body := readBody(t, resp)
	var created struct {
		ShareLink struct {
			Token string `json:"token"`
		} `json:"shareLink"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	before := readBody(t, env.do(t, mustReq(t, http.MethodGet, env.server.URL+"/s/"+created.ShareLink.Token+"/")))
	if !strings.Contains(before, "INDEX-BODY") {
		t.Fatalf("before = %q", before)
	}

	// 模拟「同一快照重新导出」：ON CONFLICT(snapshot_hash, format) 更新 object_key 与 content_hash。
	// 这正是真实导出路径（artifact.PGStore.Create / SQLiteStore.Create 的 upsert）的行为。
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("index.html")
	_, _ = w.Write([]byte("<html>NEW-VERSION</html>"))
	_ = zw.Close()
	key2 := objectstore.ObjectKey{
		TenantID: db.LocalTenantID, ProjectID: "p1", Revision: "artifact",
		AssetType: "artifact", AssetID: "bundle-v2", Ext: "zip",
	}
	if err := env.objects.Put(context.Background(), key2, bytes.NewReader(buf.Bytes()),
		objectstore.ObjectMeta{ContentType: "application/zip", Size: int64(buf.Len())}); err != nil {
		t.Fatalf("put v2: %v", err)
	}
	if _, err := env.store.Create(context.Background(), db.LocalTenantID, artifact.NewArtifact{
		ProjectID: "p1", SnapshotHash: "snap-p1", Format: artifact.FormatHTMLBundle,
		ObjectKey: key2.String(), ContentHash: "hash-p1-v2", SizeBytes: int64(buf.Len()),
	}); err != nil {
		t.Fatalf("re-export artifact: %v", err)
	}
	// 前提校验：upsert 必须真的改掉了 object_key，否则后面的断言等于没验。
	refreshed, err := env.store.Get(context.Background(), db.LocalTenantID, a.ID)
	if err != nil || refreshed.ObjectKey == a.ObjectKey {
		t.Fatalf("re-export did not refresh object_key (err=%v)", err)
	}

	after := readBody(t, env.do(t, mustReq(t, http.MethodGet, env.server.URL+"/s/"+created.ShareLink.Token+"/")))
	if after != before {
		t.Fatalf("content drifted after re-export:\n before=%q\n after=%q", before, after)
	}
}

func TestArtifactShareRejectsWrongTargets(t *testing.T) {
	env := newShareTestEnv(t)
	mp4, err := env.store.Create(context.Background(), db.LocalTenantID, artifact.NewArtifact{
		ProjectID: "p1", SnapshotHash: "snap-mp4", Format: artifact.FormatMP4,
		ObjectKey: "x", ContentHash: "c", SizeBytes: 1,
	})
	if err != nil {
		t.Fatalf("seed mp4: %v", err)
	}
	other := env.seedBundle(t, "p2")
	// 同项目里的第二个成品：用来验证「跨成品撤销」而不是被格式校验挡下来。
	second := env.seedBundle(t, "p1")

	if resp := env.createShare(t, "p1", mp4.ID, `{}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("mp4 share status = %d, want 400", resp.StatusCode)
	}
	// 用项目 p1 的路径去分享 p2 的成品：404，不是 403 —— 否则「成品存在但不在你的项目」
	// 本身就是一条可枚举的信息。
	if resp := env.createShare(t, "p1", other.ID, `{}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-project share status = %d, want 404", resp.StatusCode)
	}
	// 用成品 A 的路径撤销成品 B 的链接：必须 404。store.Revoke 只按 (id, tenant) 定位，
	// 所以这条防线只能放在 handler 里——漏了它就是一个跨成品越权撤销。
	p2Body := readBody(t, env.createShare(t, "p2", other.ID, `{}`))
	var p2Created struct {
		ShareLink struct{ ID string } `json:"shareLink"`
	}
	if err := json.Unmarshal([]byte(p2Body), &p2Created); err != nil {
		t.Fatalf("decode p2 share: %v (%s)", err, p2Body)
	}
	resp := env.do(t, mustReq(t, http.MethodPost,
		env.server.URL+"/projects/p1/artifacts/"+second.ID+"/shares/"+p2Created.ShareLink.ID+"/revoke"))
	_ = readBody(t, resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-artifact revoke status = %d, want 404", resp.StatusCode)
	}
}

func TestArtifactSharePasswordGate(t *testing.T) {
	env := newShareTestEnv(t)
	a := env.seedBundle(t, "p1")

	body := readBody(t, env.createShare(t, "p1", a.ID, `{"password":"s3cret"}`))
	var created struct {
		ShareLink struct {
			Token             string `json:"token"`
			PasswordProtected bool   `json:"passwordProtected"`
		} `json:"shareLink"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if !created.ShareLink.PasswordProtected {
		t.Fatal("passwordProtected should be true")
	}
	token := created.ShareLink.Token

	gate := env.do(t, mustReq(t, http.MethodGet, env.server.URL+"/s/"+token+"/"))
	gateBody := readBody(t, gate)
	if gate.StatusCode != http.StatusOK || !strings.Contains(gateBody, "需要口令") {
		t.Fatalf("gate = %d %q", gate.StatusCode, gateBody)
	}
	// 口令门绝不能顺带把包内字节吐出来。
	if strings.Contains(gateBody, "INDEX-BODY") {
		t.Fatal("gate page leaked bundle content")
	}

	// 未解锁的子资源直接 404：那里没有可以再输一次口令的界面。
	if sub := env.do(t, mustReq(t, http.MethodGet, env.server.URL+"/s/"+token+"/slides/001.png")); sub.StatusCode != http.StatusNotFound {
		t.Fatalf("locked entry status = %d, want 404", sub.StatusCode)
	}

	wrong := env.do(t, formPost(t, env.server.URL+"/s/"+token+"/unlock", "password=wrong"))
	wrongBody := readBody(t, wrong)
	if wrong.StatusCode != http.StatusUnauthorized || !strings.Contains(wrongBody, "口令不正确") {
		t.Fatalf("wrong password = %d %q", wrong.StatusCode, wrongBody)
	}

	ok := env.do(t, formPost(t, env.server.URL+"/s/"+token+"/unlock", "password=s3cret"))
	_ = readBody(t, ok)
	if ok.StatusCode != http.StatusSeeOther {
		t.Fatalf("unlock status = %d, want 303", ok.StatusCode)
	}
	cookies := ok.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	c := cookies[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/s/"+token {
		t.Fatalf("cookie attributes = %+v", c)
	}

	withCookie := mustReq(t, http.MethodGet, env.server.URL+"/s/"+token+"/")
	withCookie.AddCookie(c)
	unlocked := env.do(t, withCookie)
	unlockedBody := readBody(t, unlocked)
	if unlocked.StatusCode != http.StatusOK || !strings.Contains(unlockedBody, "INDEX-BODY") {
		t.Fatalf("unlocked = %d %q", unlocked.StatusCode, unlockedBody)
	}

	// cookie 里不能有口令原文。
	if strings.Contains(c.Value, "s3cret") {
		t.Fatal("cookie contains the password")
	}
	// 伪造签名的 cookie 必须被拒。
	forged := mustReq(t, http.MethodGet, env.server.URL+"/s/"+token+"/")
	forged.AddCookie(&http.Cookie{Name: artifactShareCookie, Value: strings.Split(c.Value, ".")[0] + "." + strings.Split(c.Value, ".")[1] + ".deadbeef"})
	if resp := env.do(t, forged); resp.StatusCode != http.StatusOK {
		t.Fatalf("forged cookie status = %d, want 200 (gate page)", resp.StatusCode)
	}
}

func TestArtifactShareRejectsTraversal(t *testing.T) {
	env := newShareTestEnv(t)
	a := env.seedBundle(t, "p1")
	body := readBody(t, env.createShare(t, "p1", a.ID, `{}`))
	var created struct {
		ShareLink struct{ Token string } `json:"shareLink"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	token := created.ShareLink.Token

	cases := []string{
		"/s/" + token + "/%2e%2e%2fmanifest.json",
		"/s/" + token + "/..%2fmanifest.json",
		"/s/" + token + "/slides/",
		"/s/" + token + "/nope.png",
	}
	for _, p := range cases {
		resp := env.do(t, mustReq(t, http.MethodGet, env.server.URL+p))
		b := readBody(t, resp)
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("%s returned 200 with %q", p, b)
		}
		if strings.Contains(b, "INDEX-BODY") || strings.Contains(b, "PNG-BYTES") {
			t.Fatalf("%s leaked bundle bytes", p)
		}
	}
}

func TestArtifactShareAuditHasNoCredentials(t *testing.T) {
	t.Setenv("PPTS_AUDIT_ANONYMOUS_ACCESS", "1")
	env := newShareTestEnv(t)
	a := env.seedBundle(t, "p1")
	body := readBody(t, env.createShare(t, "p1", a.ID, `{}`))
	var created struct {
		ShareLink struct{ Token string } `json:"shareLink"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_ = readBody(t, env.do(t, mustReq(t, http.MethodGet, env.server.URL+"/s/"+created.ShareLink.Token+"/")))

	events := env.audit.snapshot()
	var found int
	for _, e := range events {
		if e.Action != artifactShareActionAnonAccess {
			continue
		}
		found++
		if e.ResourceType != "artifact_share_link" || e.ResourceID == "" {
			t.Fatalf("audit resource = %s/%s", e.ResourceType, e.ResourceID)
		}
		// token 就是钥匙：进审计等于让审计库变成比分享有效期更长的二次泄漏面。
		for k, v := range e.Metadata {
			if s, ok := v.(string); ok && s == created.ShareLink.Token {
				t.Fatalf("metadata %q contains the token", k)
			}
		}
		if strings.Contains(e.ResourceID, created.ShareLink.Token) {
			t.Fatal("resource id contains the token")
		}
		if e.Metadata["artifact_id"] != a.ID {
			t.Fatalf("metadata artifact_id = %v", e.Metadata["artifact_id"])
		}
	}
	if found != 1 {
		t.Fatalf("audit events = %d, want exactly 1 for one index delivery", found)
	}

	// 子资源不再重复记：一页 20 张切片，逐个记会把一次观看放大成几十条。
	_ = readBody(t, env.do(t, mustReq(t, http.MethodGet, env.server.URL+"/s/"+created.ShareLink.Token+"/slides/001.png")))
	if n := len(env.audit.snapshot()); n != found {
		t.Fatalf("subresource wrote an extra audit event (%d -> %d)", found, n)
	}
}

func mustReq(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

func formPost(t *testing.T, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}
