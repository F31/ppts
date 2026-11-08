package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/F31/ppts/internal/audit"
)

// V20「埋点完整性」的服务端判据：view + 25/50/75/100 五个事件名是封闭集合，
// 每类只落一条，且脱敏纪律不能被后来的改动破坏。

func shareEventTestToken(t *testing.T, body string) string {
	t.Helper()
	var created struct {
		ShareLink struct {
			Token string `json:"token"`
			ID    string `json:"id"`
		} `json:"shareLink"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode share link: %v (%s)", err, body)
	}
	if len(created.ShareLink.Token) != 32 {
		t.Fatalf("token = %q", created.ShareLink.Token)
	}
	return created.ShareLink.Token
}

func postShareEvents(t *testing.T, url string, body string, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

func shareViewEventsOf(t *testing.T, events []audit.Event) []string {
	t.Helper()
	var out []string
	for _, event := range events {
		if event.Action != artifactShareActionViewEvent {
			continue
		}
		name, _ := event.Metadata["event"].(string)
		out = append(out, name)
	}
	return out
}

func TestShareEventsRecordsCompleteViewSet(t *testing.T) {
	env := newShareTestEnv(t)
	a := env.seedBundle(t, "p1")
	token := shareEventTestToken(t, readBody(t, env.createShare(t, "p1", a.ID, `{}`)))

	body := `{"sessionId":"0123456789abcdef0123456789abcdef","source":"wechat",` +
		`"events":[{"kind":"view"},{"kind":"progress","bucket":25},` +
		`{"kind":"progress","bucket":50},{"kind":"progress","bucket":75},` +
		`{"kind":"progress","bucket":100}]}`
	status, text := postShareEvents(t, env.server.URL+"/s/"+token+"/events", body, nil)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d: %s", status, text)
	}

	got := shareViewEventsOf(t, env.audit.snapshot())
	want := []string{"view", "progress_25", "progress_50", "progress_75", "progress_100"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for _, event := range env.audit.snapshot() {
		if event.ActorUser != "anonymous" || event.ResourceType != "artifact_share_link" {
			t.Fatalf("unexpected audit envelope: %+v", event)
		}
		// token 是分享地址本身，绝不能进留存；IP/UA 同理。
		for key := range event.Metadata {
			if key == "token" || key == "ip" || key == "client_ip" || key == "user_agent" {
				t.Fatalf("敏感字段 %q 被写进埋点: %+v", key, event.Metadata)
			}
		}
		if event.Metadata["session"] != "0123456789abcdef0123456789abcdef" {
			t.Fatalf("session 丢了: %+v", event.Metadata)
		}
		if event.Metadata["source"] != "wechat" {
			t.Fatalf("source 丢了: %+v", event.Metadata)
		}
	}
}

func TestShareEventsDeduplicatesWithinRequest(t *testing.T) {
	env := newShareTestEnv(t)
	a := env.seedBundle(t, "p1")
	token := shareEventTestToken(t, readBody(t, env.createShare(t, "p1", a.ID, `{}`)))

	body := `{"sessionId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",` +
		`"events":[{"kind":"view"},{"kind":"view"},{"kind":"progress","bucket":25},` +
		`{"kind":"progress","bucket":25}]}`
	status, _ := postShareEvents(t, env.server.URL+"/s/"+token+"/events", body, nil)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d", status)
	}
	got := shareViewEventsOf(t, env.audit.snapshot())
	if strings.Join(got, ",") != "view,progress_25" {
		t.Fatalf("去重后 = %v", got)
	}
}

func TestShareEventsRejectsUnknownEventsWithoutWriting(t *testing.T) {
	env := newShareTestEnv(t)
	a := env.seedBundle(t, "p1")
	token := shareEventTestToken(t, readBody(t, env.createShare(t, "p1", a.ID, `{}`)))
	url := env.server.URL + "/s/" + token + "/events"

	cases := []struct {
		name string
		body string
	}{
		{"未带 session", `{"events":[{"kind":"view"}]}`},
		{"session 带非十六进制", `{"sessionId":"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz","events":[{"kind":"view"}]}`},
		{"空事件", `{"sessionId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","events":[]}`},
		{"未知 kind", `{"sessionId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","events":[{"kind":"click"}]}`},
		{"progress 缺 bucket", `{"sessionId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","events":[{"kind":"progress"}]}`},
		{"progress 桶不在白名单", `{"sessionId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","events":[{"kind":"progress","bucket":33}]}`},
		{"view 带了 bucket", `{"sessionId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","events":[{"kind":"view","bucket":25}]}`},
		{"非法 JSON", `{`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := postShareEvents(t, url, tc.body, nil)
			if status < 400 {
				t.Fatalf("status = %d，非法事件被接受", status)
			}
		})
	}
	if got := shareViewEventsOf(t, env.audit.snapshot()); len(got) != 0 {
		t.Fatalf("非法请求写进了事件: %v", got)
	}
}

func TestShareEventsPrivacyAndTransportGuards(t *testing.T) {
	env := newShareTestEnv(t)
	a := env.seedBundle(t, "p1")
	token := shareEventTestToken(t, readBody(t, env.createShare(t, "p1", a.ID, `{}`)))
	url := env.server.URL + "/s/" + token + "/events"
	valid := `{"sessionId":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","events":[{"kind":"view"}]}`

	t.Run("服务端总闸关闭", func(t *testing.T) {
		t.Setenv("PPTS_SHARE_TELEMETRY", "0")
		status, _ := postShareEvents(t, url, valid, nil)
		if status != http.StatusNoContent {
			t.Fatalf("关闭时不应报错: %d", status)
		}
		if got := shareViewEventsOf(t, env.audit.snapshot()); len(got) != 0 {
			t.Fatalf("关闭时仍写入: %v", got)
		}
	})
	t.Run("跨站 Origin 拒绝", func(t *testing.T) {
		status, _ := postShareEvents(t, url, valid, map[string]string{"Origin": "https://evil.example"})
		if status != http.StatusForbidden {
			t.Fatalf("status = %d", status)
		}
	})
	t.Run("非 JSON Content-Type 拒绝", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(valid))
		req.Header.Set("Content-Type", "text/plain")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})
	t.Run("无效 token 不写", func(t *testing.T) {
		status, _ := postShareEvents(t, env.server.URL+"/s/"+strings.Repeat("f", 32)+"/events", valid, nil)
		if status != http.StatusNotFound {
			t.Fatalf("status = %d", status)
		}
	})
	if got := shareViewEventsOf(t, env.audit.snapshot()); len(got) != 0 {
		t.Fatalf("守卫路径写进了事件: %v", got)
	}
}

func TestShareEventsPasswordGateAndThrottle(t *testing.T) {
	env := newShareTestEnv(t)
	a := env.seedBundle(t, "p1")
	token := shareEventTestToken(t, readBody(t, env.createShare(t, "p1", a.ID, `{"password":"s3cret"}`)))
	url := env.server.URL + "/s/" + token + "/events"
	valid := `{"sessionId":"cccccccccccccccccccccccccccccccc","events":[{"kind":"view"}]}`

	status, _ := postShareEvents(t, url, valid, nil)
	if status != http.StatusNotFound {
		t.Fatalf("未解锁应 404，实际 %d", status)
	}
	if got := shareViewEventsOf(t, env.audit.snapshot()); len(got) != 0 {
		t.Fatalf("未解锁仍写入: %v", got)
	}
}

func TestShareEventThrottleWindow(t *testing.T) {
	base := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	th := newShareEventThrottle()
	now := base
	th.now = func() time.Time { return now }
	for i := 0; i < shareEventThrottleLimit; i++ {
		if !th.allow("token-a") {
			t.Fatalf("第 %d 次被拒", i+1)
		}
	}
	if th.allow("token-a") {
		t.Fatal("超过限额仍放行")
	}
	if !th.allow("token-b") {
		t.Fatal("一个 token 超限不该拖累其它 token")
	}
	now = base.Add(shareEventThrottleWindow)
	if !th.allow("token-a") {
		t.Fatal("窗口过后应重新放行")
	}
}

func TestNormalizeShareViewReportClosedEventSet(t *testing.T) {
	names, _, err := normalizeShareViewReport(shareViewReport{
		SessionID: "dddddddddddddddddddddddddddddddd",
		Events: []shareViewEvent{
			{Kind: shareEventKindView},
			{Kind: shareEventKindProgress, Bucket: 25},
			{Kind: shareEventKindProgress, Bucket: 50},
			{Kind: shareEventKindProgress, Bucket: 75},
			{Kind: shareEventKindProgress, Bucket: 100},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "view,progress_25,progress_50,progress_75,progress_100" {
		t.Fatalf("事件集合 = %v", names)
	}
	// 越界来源只置空，不丢整批事件。
	_, source, err := normalizeShareViewReport(shareViewReport{
		SessionID: "dddddddddddddddddddddddddddddddd",
		Source:    strings.Repeat("x", maxShareSourceRunes+1),
		Events:    []shareViewEvent{{Kind: shareEventKindView}},
	})
	if err != nil || source != "" {
		t.Fatalf("source = %q, err = %v", source, err)
	}
}
