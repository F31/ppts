package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	// ErrAnimationUnavailable 是降级信号而不是错误：上层据此回退静态导出，
	// 并提示"动画服务暂不可用，已导出静态版本"（§8.8）。动画是增强，不是前置条件。
	ErrAnimationUnavailable = errors.New("render: animation service unavailable")
	// ErrFontUnavailable 表示渲染器缺少必需字体。宁可明确拒绝，也不产出方框字。
	ErrFontUnavailable = errors.New("render: required font unavailable")
)

// HTTPError 是渲染服务返回的非 2xx 响应。
type HTTPError struct {
	Status  int
	Code    string
	Field   string
	Message string
}

func (e *HTTPError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("render: http %d %s at %s: %s", e.Status, e.Code, e.Field, e.Message)
	}
	return fmt.Sprintf("render: http %d %s: %s", e.Status, e.Code, e.Message)
}

// ClientConfig 是渲染客户端配置。
type ClientConfig struct {
	BaseURL string
	// HTTPClient 可注入（测试用）；nil 时按 Timeout 构造。
	HTTPClient *http.Client
	// Retries 是重试次数（不含首次）。0 会用默认值 2。
	Retries int
	// Backoff 是首次退避时长（0 = 200ms），之后翻倍。
	Backoff time.Duration
	// Timeout 是单次请求超时（0 = 60s）。
	Timeout time.Duration
}

// Client 是渲染服务的 HTTP 客户端。
type Client struct {
	base    string
	hc      *http.Client
	retries int
	backoff time.Duration

	probeOnce sync.Once
	health    Health
	probeErr  error
}

// NewClient 返回客户端。BaseURL 去掉尾部斜杠，避免拼出 //v1/render。
func NewClient(cfg ClientConfig) (*Client, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("render: base url is required")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		hc = &http.Client{Timeout: timeout}
	}
	retries := cfg.Retries
	if retries <= 0 {
		retries = 2
	}
	backoff := cfg.Backoff
	if backoff <= 0 {
		backoff = 200 * time.Millisecond
	}
	return &Client{
		base:    strings.TrimRight(cfg.BaseURL, "/"),
		hc:      hc,
		retries: retries,
		backoff: backoff,
	}, nil
}

// Health 探测服务能力。结果按进程缓存一次：字体清单在容器生命周期内不会变。
func (c *Client) Health(ctx context.Context) (Health, error) {
	c.probeOnce.Do(func() {
		var h Health
		if err := c.do(ctx, http.MethodGet, "/healthz", nil, &h); err != nil {
			c.probeErr = fmt.Errorf("%w: %v", ErrAnimationUnavailable, err)
			return
		}
		if h.SchemaVersion != IRSchemaVersion {
			c.probeErr = fmt.Errorf("%w: renderer speaks %q, client speaks %q",
				ErrAnimationUnavailable, h.SchemaVersion, IRSchemaVersion)
			return
		}
		c.health = h
	})
	return c.health, c.probeErr
}

// Available 报告渲染服务是否可用。上层用法：不可用就直接走静态导出，不构造计划。
func (c *Client) Available(ctx context.Context) bool {
	_, err := c.Health(ctx)
	return err == nil
}

// RequireFonts 校验渲染器是否提供指定字体族。缺失时返回 ErrFontUnavailable，
// 而不是让渲染器退回默认字体产出方框字（§8.6）。
func (c *Client) RequireFonts(ctx context.Context, families ...string) error {
	h, err := c.Health(ctx)
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(h.Fonts))
	for _, f := range h.Fonts {
		have[f.Family] = true
	}
	var missing []string
	for _, f := range families {
		if !have[f] {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrFontUnavailable, strings.Join(missing, ", "))
	}
	return nil
}

// Submit 提交计划，返回 jobId。本地先做一次 IR 校验：格式错误不该占用一次网络往返。
func (c *Client) Submit(ctx context.Context, plan *Timeline) (string, error) {
	if err := plan.Validate(); err != nil {
		return "", err
	}
	body, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("render: marshal plan: %w", err)
	}
	var st JobStatus
	if err := c.do(ctx, http.MethodPost, "/v1/render", body, &st); err != nil {
		return "", err
	}
	if st.ID == "" {
		return "", errors.New("render: submit returned empty job id")
	}
	return st.ID, nil
}

// Status 查询任务状态。
func (c *Client) Status(ctx context.Context, id string) (JobStatus, error) {
	var st JobStatus
	err := c.do(ctx, http.MethodGet, "/v1/render/"+id, nil, &st)
	return st, err
}

// DownloadArtifact 把产物（通常是第 i 页的 SegmentName(i)）写入 w，返回写入字节数。
//
// 刻意不复用 do()：那里把响应体读进内存并限 1MB，而分段是 1~10MB 的二进制；
// 也不做重试——调用方是按页下载的，单页失败重试的粒度比「整段重来」更小，
// 且重试会把尾部延迟叠在已经耗时的渲染之后。
//
// 显式比对 Content-Length：截断的分段能被拼接器当成好片（ffprobe 只看头），
// 最后表现成「某一页黑屏」，比这里直接报错难查得多。
func (c *Client) DownloadArtifact(ctx context.Context, id, name string, w io.Writer) (int64, error) {
	u := c.base + "/v1/render/" + url.PathEscape(id) + "/artifacts/" + escapeArtifactPath(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("%w: %v", ErrAnimationUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return 0, decodeHTTPError(resp.StatusCode, data)
	}
	n, err := io.Copy(w, resp.Body)
	if err != nil {
		return n, fmt.Errorf("render: download %s: %w", name, err)
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return n, fmt.Errorf("render: download %s: got %d bytes, want %d", name, n, resp.ContentLength)
	}
	return n, nil
}

// escapeArtifactPath 逐段转义，保留 "/" 作为分隔符：
// 整个字符串送进 PathEscape 会把目录层级压成一个文件名，服务端就找不到产物。
func escapeArtifactPath(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// Cancel 取消任务（尽力而为）。
func (c *Client) Cancel(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/render/"+id, nil, nil)
}

// Wait 轮询直到终态或超时。poll 为 0 时用 500ms。
func (c *Client) Wait(ctx context.Context, id string, poll, timeout time.Duration) (JobStatus, error) {
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		st, err := c.Status(ctx, id)
		if err != nil {
			return st, err
		}
		switch st.State {
		case StateSucceeded, StateFailed, StateCanceled:
			return st, nil
		}
		if timeout > 0 && time.Now().After(deadline) {
			return st, fmt.Errorf("render: job %s timed out after %s", id, timeout)
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-t.C:
		}
	}
}

// do 是带重试的请求。重试策略见 design.md D7：
// 5xx/408/429/网络错误重试；4xx（其余）与解码失败立即返回。
func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	var netErr error
	var httpErr *HTTPError
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			delay := c.backoff << (attempt - 1)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			netErr = err
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			netErr = readErr
			continue
		}

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			if out == nil || len(data) == 0 {
				return nil
			}
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("render: decode response: %w", err)
			}
			return nil
		case retryable(resp.StatusCode):
			httpErr = decodeHTTPError(resp.StatusCode, data)
			continue
		default:
			return decodeHTTPError(resp.StatusCode, data)
		}
	}
	// 重试耗尽：5xx 说明服务在但渲染不了，交给上层按 HTTPError 处理；
	// 连接层的失败说明服务根本没起来，这是降级信号。
	if httpErr != nil {
		return httpErr
	}
	return fmt.Errorf("%w: %v", ErrAnimationUnavailable, netErr)
}

func retryable(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

func decodeHTTPError(status int, data []byte) *HTTPError {
	e := &HTTPError{Status: status, Code: "http_error"}
	var body struct {
		Code    string `json:"code"`
		Field   string `json:"field"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &body); err == nil {
		if body.Code != "" {
			e.Code = body.Code
		}
		e.Field = body.Field
		e.Message = body.Message
	}
	if e.Message == "" {
		e.Message = http.StatusText(status)
	}
	return e
}

// IsUnavailable 报告错误是否属于"服务不可用"（应当降级而不是报错）。
func IsUnavailable(err error) bool {
	if errors.Is(err, ErrAnimationUnavailable) {
		return true
	}
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status == http.StatusBadGateway || he.Status == http.StatusServiceUnavailable ||
			he.Status == http.StatusGatewayTimeout
	}
	return false
}
