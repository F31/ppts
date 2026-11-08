package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/F31/ppts/internal/artifact"
	"github.com/F31/ppts/internal/audit"
)

// 在线分享观看埋点（设计 §20.8 V20）。
//
// 边界只有一条：**仅在线分享上报**。成品包本身是零外链、断网可播的自包含 HTML，
// 它不会、也不能因为"加了个埋点"就变成需要联网才能播的东西。因此播放器只在
// http(s) + /s/<token>/ 这个同源形态下启用上报（见 web/src/sharePlayer/telemetry.ts）；
// file:// 双击、本地静态托管、门禁浏览器环境一律零请求。
//
// 为什么事件落审计表而不是新建一张匿名可写表：
//   - 匿名端每多一张可写表就多一个写放大面，而 audit_events 已经有
//     artifact_share.anonymous_access 这条匿名写入先例（PG 走 tenant.Run 设上下文）。
//   - 观看事件是「有人看了这一版成片」这条运营事实的一个维度，不是新的业务实体。
//   - 脱敏纪律沿用现成的：不记 token、不记 IP、不记 UA。sessionId 是播放器
//     这一次观看自己生成的随机值，不与任何账号关联。
//
// 与 anonymousAccessAudited() 刻意分开：那个开关管的是「匿名访问要不要进审计留存」，
// 是合规口径；这个开关管的是「要不要采集观看进度」，是产品口径。隐私承诺的第一道
// 在浏览器（DNT / GPC / privacy=1 / 离线），PPTS_SHARE_TELEMETRY 是运营侧总闸。

const (
	artifactShareActionViewEvent = "artifact_share.view_event"
	// 写入与聚合必须共用同一个资源类型：两处各写一个字符串，改一处就会让
	// 看板静默查不到数据（而事件仍照常写，所有写入测试都是绿的）。
	artifactShareLinkResourceType = "artifact_share_link"

	shareEventKindView     = "view"
	shareEventKindProgress = "progress"

	maxShareEventBodyBytes = 4 << 10
	maxShareEventCount     = 8
	maxShareSourceRunes    = 64

	shareEventThrottleWindow = time.Minute
	shareEventThrottleLimit  = 40
	shareEventThrottleCap    = 4096
)

var (
	shareProgressBuckets = []int{25, 50, 75, 100}
	shareSessionPattern  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	shareSourcePattern   = regexp.MustCompile(`^[A-Za-z0-9._-]*$`)
)

// shareTelemetryEnabled 是服务端总闸。默认开启，显式设为 0/false/off 才关闭。
// 「默认开启」只作用于在线分享形态；隐私与离线由浏览器侧判据兜住。
func shareTelemetryEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("PPTS_SHARE_TELEMETRY")))
	return v != "0" && v != "false" && v != "off"
}

// shareViewEvent 是播放器上报的单条事件。
type shareViewEvent struct {
	Kind   string `json:"kind"`
	Bucket int    `json:"bucket,omitempty"`
}

// shareViewReport 是一次上报的批次。播放器把同一批事件合起来发，
// 避免 25/50/75/100 各开一个请求（匿名端的请求数就是写放大杠杆）。
type shareViewReport struct {
	SessionID string           `json:"sessionId"`
	Source    string           `json:"source,omitempty"`
	Events    []shareViewEvent `json:"events"`
}

// normalizeShareViewReport 把请求收敛成一组**规范事件名**。
//
// 返回的每个名字形如 view / progress_25 / … / progress_100。单请求内
// (kind,bucket) 去重：播放器要重试时不会把一次观看算成两次。
// 来源只做长度与字符白名单，越界时置空而不整单拒绝——分享来源是可有可无的
// 维度，为它丢掉首帧曝光不符合「埋点齐全」的初衷。
func normalizeShareViewReport(in shareViewReport) ([]string, string, error) {
	if !shareSessionPattern.MatchString(in.SessionID) {
		return nil, "", errors.New("invalid_session")
	}
	if len(in.Events) == 0 || len(in.Events) > maxShareEventCount {
		return nil, "", errors.New("invalid_events")
	}
	source := strings.TrimSpace(in.Source)
	if len([]rune(source)) > maxShareSourceRunes || !shareSourcePattern.MatchString(source) {
		source = ""
	}
	seen := make(map[string]struct{}, len(in.Events))
	out := make([]string, 0, len(in.Events))
	for _, event := range in.Events {
		var name string
		switch event.Kind {
		case shareEventKindView:
			if event.Bucket != 0 {
				return nil, "", errors.New("unexpected_bucket")
			}
			name = shareEventKindView
		case shareEventKindProgress:
			valid := false
			for _, bucket := range shareProgressBuckets {
				if event.Bucket == bucket {
					valid = true
					break
				}
			}
			if !valid {
				return nil, "", errors.New("invalid_bucket")
			}
			name = "progress_" + itoaShareBucket(event.Bucket)
		default:
			return nil, "", errors.New("invalid_kind")
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out, source, nil
}

// itoaShareBucket 只处理四个白名单值：不引入 strconv 的通用格式化，
// 事件名集合因此是封闭的，门禁可以直接按字面量核对。
func itoaShareBucket(bucket int) string {
	switch bucket {
	case 25:
		return "25"
	case 50:
		return "50"
	case 75:
		return "75"
	case 100:
		return "100"
	}
	return ""
}

// shareEventThrottle 按 token 做进程内固定窗口计数。
//
// 为什么不用 PGBackend：匿名端没有租户上下文，而限流本身只是防「一个 token
// 被脚本无限刷」的兜底；进程内计数在多副本下会按副本数放宽，但每个副本仍有
// 上限，且事件写入还有单请求去重。为它引入共享存储的写路径不划算。
type shareEventThrottle struct {
	mu     sync.Mutex
	now    func() time.Time
	counts map[string]shareEventThrottleEntry
}

type shareEventThrottleEntry struct {
	start time.Time
	n     int
}

func newShareEventThrottle() *shareEventThrottle {
	return &shareEventThrottle{
		now:    time.Now,
		counts: make(map[string]shareEventThrottleEntry),
	}
}

func (t *shareEventThrottle) allow(key string) bool {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.counts) >= shareEventThrottleCap {
		// 先淘汰过期窗口；仍满则清干净重来。宁可短暂放宽，也不让匿名端
		// 通过造新 token 把进程内存撑到无界。
		for k, entry := range t.counts {
			if now.Sub(entry.start) >= shareEventThrottleWindow {
				delete(t.counts, k)
			}
		}
		if len(t.counts) >= shareEventThrottleCap {
			t.counts = make(map[string]shareEventThrottleEntry)
		}
	}
	entry, ok := t.counts[key]
	if !ok || now.Sub(entry.start) >= shareEventThrottleWindow {
		t.counts[key] = shareEventThrottleEntry{start: now, n: 1}
		return true
	}
	if entry.n >= shareEventThrottleLimit {
		return false
	}
	entry.n++
	t.counts[key] = entry
	return true
}

// shareEventHandler 持有匿名上报所需的依赖。
type shareEventHandler struct {
	links    artifact.ShareLinkStore
	pepper   string
	recorder audit.Recorder
	throttle *shareEventThrottle
}

// serve 处理 POST /s/{token}/events。
//
// 所有失败都返回 2xx/4xx 且不携带任何可供探测的内容：埋点失败必须静默，
// 绝不能让一次 fetch 失败影响播放（V16 要求无 console error）。
func (h *shareEventHandler) serve(w http.ResponseWriter, r *http.Request) {
	setShareHeaders(w)
	if !shareTelemetryEnabled() || h.recorder == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !sameOriginPost(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "forbidden"})
		return
	}
	if ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type"))); !strings.HasPrefix(ct, "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"code": "invalid_content_type"})
		return
	}

	token := r.PathValue("token")
	link, err := h.links.GetShareLinkByToken(r.Context(), token)
	if err != nil {
		shareNotFound(w)
		return
	}
	// 口令保护的链接：未解锁时与子资源同码。否则没输口令的人也能往这条链接写事件。
	if link.PasswordProtected && !shareCookieMatches(r, link.ID, h.pepper) {
		shareNotFound(w)
		return
	}
	if !h.throttle.allow(token) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"code": "rate_limited"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxShareEventBodyBytes+1))
	if err != nil || len(body) > maxShareEventBodyBytes {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_body"})
		return
	}
	var report shareViewReport
	if err := json.Unmarshal(body, &report); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_body"})
		return
	}
	names, source, err := normalizeShareViewReport(report)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"code": err.Error()})
		return
	}

	for _, name := range names {
		recordShareViewEvent(r.Context(), h.recorder, link, report.SessionID, source, name)
	}
	w.WriteHeader(http.StatusNoContent)
}

// sameOriginPost 挡住跨站表单。Content-Type: application/json 已经让简单表单
// 无法伪造（会触发预检，而 /s/ 不提供 CORS）；这里再对带 Origin 的请求核一次 host，
// 覆盖「客户端自己塞了 JSON 头」的情形。
func sameOriginPost(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || origin == "null" {
		return true
	}
	host := ""
	if u, err := url.Parse(origin); err == nil {
		host = u.Host
	}
	return host != "" && strings.EqualFold(host, r.Host)
}

// recordShareViewEvent 写一条匿名观看事件。忽略错误：审计存储抖动不该连带
// 让播放器收到一个失败响应（与 recordArtifactShareAccess 同一取舍）。
func recordShareViewEvent(ctx context.Context, recorder audit.Recorder, link *artifact.ShareLink,
	sessionID, source, event string) {
	_ = recorder.Record(ctx, audit.Event{
		TenantID:     link.TenantID,
		ActorUser:    "anonymous",
		Action:       artifactShareActionViewEvent,
		ResourceType: artifactShareLinkResourceType,
		ResourceID:   link.ID,
		Metadata: map[string]any{
			"surface":     "shared.bundle.online",
			"session":     sessionID,
			"source":      source,
			"event":       event,
			"project_id":  link.ProjectID,
			"artifact_id": link.ArtifactID,
		},
	})
}
