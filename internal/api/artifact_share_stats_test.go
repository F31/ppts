package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type shareStatsResponse struct {
	LinkID           string `json:"linkId"`
	ArtifactID       string `json:"artifactId"`
	ProjectID        string `json:"projectId"`
	TelemetryEnabled bool   `json:"telemetryEnabled"`
	RetentionNote    string `json:"retentionNote"`
	Window           struct {
		Days  int    `json:"days"`
		Since string `json:"since"`
		Until string `json:"until"`
	} `json:"window"`
	Stats struct {
		Views          int64   `json:"views"`
		Sessions       int64   `json:"sessions"`
		Completed      int64   `json:"completed"`
		CompletionRate float64 `json:"completionRate"`
		Progress       []struct {
			Bucket   int   `json:"bucket"`
			Sessions int64 `json:"sessions"`
		} `json:"progress"`
		FirstEventAt *string `json:"firstEventAt"`
		LastEventAt  *string `json:"lastEventAt"`
	} `json:"stats"`
}

func getShareStats(t *testing.T, env *shareTestEnv, path string) (int, string) {
	t.Helper()
	resp := env.do(t, mustReq(t, http.MethodGet, env.server.URL+path))
	return resp.StatusCode, readBody(t, resp)
}

func decodeShareStats(t *testing.T, status int, body string) shareStatsResponse {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("stats status = %d: %s", status, body)
	}
	var out shareStatsResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode stats: %v (%s)", err, body)
	}
	return out
}

func shareLinkID(t *testing.T, createBody string) string {
	t.Helper()
	var created struct {
		ShareLink struct {
			ID string `json:"id"`
		} `json:"shareLink"`
	}
	if err := json.Unmarshal([]byte(createBody), &created); err != nil {
		t.Fatalf("decode share link: %v (%s)", err, createBody)
	}
	if created.ShareLink.ID == "" {
		t.Fatalf("share link id empty: %s", createBody)
	}
	return created.ShareLink.ID
}

func statsPath(projectID, artifactID, linkID, query string) string {
	p := "/projects/" + projectID + "/artifacts/" + artifactID + "/shares/" + linkID + "/stats"
	if query != "" {
		p += "?" + query
	}
	return p
}

// TestShareStatsReadsRealAnonymousReports 是整条链的对账：事件由匿名端点真实写入
// SQLite 审计表，统计端点用同一份聚合 SQL 读回来。任何一处字段名漂移都会红。
func TestShareStatsReadsRealAnonymousReports(t *testing.T) {
	env := newShareTestEnv(t)
	a := env.seedBundle(t, "p1")
	createBody := readBody(t, env.createShare(t, "p1", a.ID, `{}`))
	linkID := shareLinkID(t, createBody)
	token := shareEventTestToken(t, createBody)

	// s1 看完全程；s2 只到 50；s3 只有首帧。s1 的 view 重复一次（重试语义）。
	reports := []string{
		`{"sessionId":"11111111111111111111111111111111","events":[{"kind":"view"},{"kind":"progress","bucket":25},{"kind":"progress","bucket":50},{"kind":"progress","bucket":75},{"kind":"progress","bucket":100}]}`,
		`{"sessionId":"11111111111111111111111111111111","events":[{"kind":"view"}]}`,
		`{"sessionId":"22222222222222222222222222222222","events":[{"kind":"view"},{"kind":"progress","bucket":25},{"kind":"progress","bucket":50}]}`,
		`{"sessionId":"33333333333333333333333333333333","events":[{"kind":"view"}]}`,
	}
	for _, body := range reports {
		status, text := postShareEvents(t, env.server.URL+"/s/"+token+"/events", body, nil)
		if status != http.StatusNoContent {
			t.Fatalf("report status = %d: %s", status, text)
		}
	}

	status, body := getShareStats(t, env, statsPath("p1", a.ID, linkID, ""))
	got := decodeShareStats(t, status, body)
	if got.LinkID != linkID || got.ArtifactID != a.ID || got.ProjectID != "p1" {
		t.Fatalf("envelope mismatch: %+v", got)
	}
	if !got.TelemetryEnabled || got.RetentionNote != shareStatsRetentionNote || got.Window.Days != shareStatsDefaultDays {
		t.Fatalf("口径字段缺失/不实: %+v", got)
	}
	if got.Window.Since == "" || got.Window.Until == "" {
		t.Error("window 必须给出起止，否则前端没法说清「最近 N 天」")
	}
	if got.Stats.Views != 3 || got.Stats.Sessions != 3 {
		t.Errorf("views=%d sessions=%d, want 3/3", got.Stats.Views, got.Stats.Sessions)
	}
	wantBuckets := map[int]int64{25: 2, 50: 2, 75: 1, 100: 1}
	if len(got.Stats.Progress) != 4 {
		t.Fatalf("progress buckets = %d, want 4", len(got.Stats.Progress))
	}
	for _, b := range got.Stats.Progress {
		if wantBuckets[b.Bucket] != b.Sessions {
			t.Errorf("bucket %d = %d, want %d", b.Bucket, b.Sessions, wantBuckets[b.Bucket])
		}
	}
	if got.Stats.Completed != 1 || got.Stats.CompletionRate != float64(1)/3 {
		t.Errorf("completed=%d rate=%v, want 1 / 1/3", got.Stats.Completed, got.Stats.CompletionRate)
	}
	if got.Stats.FirstEventAt == nil || got.Stats.LastEventAt == nil {
		t.Error("有事件时首末时间不应为 null")
	}
	// 响应不得带任何单条事件、session 或 token。
	for _, leak := range []string{"11111111111111111111111111111111", token, "sessionId", "client_ip"} {
		if strings.Contains(body, leak) {
			t.Errorf("响应泄露 %q", leak)
		}
	}
}

func TestShareStatsIsolatesLinksAndRejectsForeignOwnership(t *testing.T) {
	env := newShareTestEnv(t)
	a1 := env.seedBundle(t, "p1")
	a2 := env.seedBundle(t, "p2")
	body1 := readBody(t, env.createShare(t, "p1", a1.ID, `{}`))
	body2 := readBody(t, env.createShare(t, "p1", a1.ID, `{}`))
	id1, id2 := shareLinkID(t, body1), shareLinkID(t, body2)
	if id1 == id2 {
		t.Fatal("两个链接 ID 相同，隔离用例失去意义")
	}
	token1 := shareEventTestToken(t, body1)
	if status, text := postShareEvents(t, env.server.URL+"/s/"+token1+"/events",
		`{"sessionId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","events":[{"kind":"view"}]}`, nil); status != http.StatusNoContent {
		t.Fatalf("report = %d: %s", status, text)
	}

	status, text := getShareStats(t, env, statsPath("p1", a1.ID, id2, ""))
	got := decodeShareStats(t, status, text)
	if got.Stats.Views != 0 || got.Stats.Sessions != 0 {
		t.Errorf("另一条链接被串数: %+v", got.Stats)
	}

	// 用 p2 的路径读 p1 的成品 → 404；用 p1 的路径读属于 a1 的链接但把成品换成 a2 → 404。
	for _, path := range []string{
		statsPath("p2", a1.ID, id1, ""),
		statsPath("p1", a2.ID, id1, ""),
		statsPath("p1", a1.ID, "link-does-not-exist", ""),
	} {
		st, _ := getShareStats(t, env, path)
		if st != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, st)
		}
	}
}

func TestShareStatsDaysValidationAndEmptyWindow(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
		ok   bool
	}{
		{"", 30, true}, {"1", 1, true}, {"90", 90, true},
		{"0", 0, false}, {"91", 0, false}, {"abc", 0, false}, {"-3", 0, false}, {"7.5", 0, false},
	} {
		got, err := parseShareStatsDays(tc.raw)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("parseShareStatsDays(%q) = %d, %v", tc.raw, got, err)
		}
	}

	env := newShareTestEnv(t)
	a := env.seedBundle(t, "p1")
	id := shareLinkID(t, readBody(t, env.createShare(t, "p1", a.ID, `{}`)))
	for _, query := range []string{"days=0", "days=91", "days=abc"} {
		st, _ := getShareStats(t, env, statsPath("p1", a.ID, id, query))
		if st != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", query, st)
		}
	}
	// 空数据也必须带完整口径，不能让前端把「没数据」显示成「累计 0」。
	st, body := getShareStats(t, env, statsPath("p1", a.ID, id, "days=7"))
	got := decodeShareStats(t, st, body)
	if got.Window.Days != 7 || got.RetentionNote != shareStatsRetentionNote || !got.TelemetryEnabled {
		t.Errorf("空窗口口径缺失: %+v", got)
	}
	if got.Stats.FirstEventAt != nil || got.Stats.LastEventAt != nil {
		t.Error("空数据首末时间应为 null")
	}
	if len(got.Stats.Progress) != 4 {
		t.Error("空数据仍须给全四个桶（前端才能画一条空漏斗，而不是当成加载失败）")
	}
}

func TestShareStatsWhenTelemetryDisabled(t *testing.T) {
	t.Setenv("PPTS_SHARE_TELEMETRY", "0")
	env := newShareTestEnv(t)
	a := env.seedBundle(t, "p1")
	createBody := readBody(t, env.createShare(t, "p1", a.ID, `{}`))
	id := shareLinkID(t, createBody)
	token := shareEventTestToken(t, createBody)

	if status, _ := postShareEvents(t, env.server.URL+"/s/"+token+"/events",
		`{"sessionId":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","events":[{"kind":"view"}]}`, nil); status != http.StatusNoContent {
		t.Fatalf("总闸关闭时上报应静默 204，got %d", status)
	}
	st, body := getShareStats(t, env, statsPath("p1", a.ID, id, ""))
	got := decodeShareStats(t, st, body)
	if got.TelemetryEnabled {
		t.Error("telemetryEnabled 应为 false，否则前端会把 0 显示成「0 次观看」")
	}
	if got.Stats.Views != 0 || got.Stats.Sessions != 0 {
		t.Errorf("总闸关闭仍写入了事件: %+v", got.Stats)
	}
}
