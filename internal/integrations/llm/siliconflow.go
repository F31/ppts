package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/F31/ppts/internal/animation/bind"
)

const (
	defaultSiliconFlowLLMBaseURL  = "https://api.siliconflow.cn"
	defaultSiliconFlowLLMModel    = "Qwen/Qwen2.5-7B-Instruct"
	defaultSiliconFlowVisionModel = "Qwen/Qwen3-VL-8B-Instruct"
)

// SiliconFlowConfig configures the OpenAI-compatible chat completions adapter.
type SiliconFlowConfig struct {
	BaseURL     string
	APIKey      string
	Model       string
	VisionModel string
	Timeout     time.Duration
	Client      *http.Client
}

// NewSiliconFlowProvider creates a SiliconFlow chat completions provider.
func NewSiliconFlowProvider(cfg SiliconFlowConfig) (*SiliconFlowProvider, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = defaultSiliconFlowLLMBaseURL
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = defaultSiliconFlowLLMModel
	}
	visionModel := strings.TrimSpace(cfg.VisionModel)
	if visionModel == "" {
		visionModel = defaultSiliconFlowVisionModel
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("llm: PPTS_LLM_API_KEY is required for siliconflow")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = llmTimeoutFromEnv()
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &SiliconFlowProvider{baseURL: base, apiKey: cfg.APIKey, model: model, visionModel: visionModel, client: client}, nil
}

func llmTimeoutFromEnv() time.Duration {
	for _, name := range []string{"PPTS_LLM_TIMEOUT", "PPTS_LLM_REQUEST_TIMEOUT"} {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			continue
		}
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return 180 * time.Second
}

// FromEnv returns the configured text rewriter. Empty provider disables LLM features.
func FromEnv() (TextRewriter, error) {
	switch strings.TrimSpace(strings.ToLower(os.Getenv("PPTS_LLM_PROVIDER"))) {
	case "", "none":
		return nil, nil
	case "siliconflow":
		return NewSiliconFlowProvider(SiliconFlowConfig{
			BaseURL: os.Getenv("PPTS_LLM_BASE_URL"), APIKey: os.Getenv("PPTS_LLM_API_KEY"),
			Model: os.Getenv("PPTS_LLM_MODEL"), VisionModel: os.Getenv("PPTS_LLM_VISION_MODEL"),
		})
	default:
		return nil, fmt.Errorf("llm: unsupported PPTS_LLM_PROVIDER=%q", os.Getenv("PPTS_LLM_PROVIDER"))
	}
}

// SiliconFlowProvider implements TextRewriter via /v1/chat/completions.
type SiliconFlowProvider struct {
	baseURL     string
	apiKey      string
	model       string
	visionModel string
	client      *http.Client
}

func (p *SiliconFlowProvider) Rewrite(ctx context.Context, req RewriteRequest) (RewriteResult, error) {
	if strings.TrimSpace(req.SourceText) == "" {
		return RewriteResult{}, errors.New("llm: source text is required")
	}
	payload := chatRequest{
		Model: p.model,
		Messages: []chatMessage{
			{Role: "system", Content: "你是专业 PPT 演示讲解文案编辑。只输出正文，不输出解释、标题或项目符号。必须保留输入中的数字、单位、日期、型号和专有名词，不编造事实。"},
			{Role: "user", Content: buildRewritePrompt(req)},
		},
		Temperature: 0.35,
		MaxTokens:   800,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return RewriteResult{}, err
	}
	out, err := p.doChat(ctx, body)
	if err != nil {
		return RewriteResult{}, err
	}
	if len(out.Choices) == 0 {
		return RewriteResult{}, errors.New("llm: empty choices")
	}
	text := strings.TrimSpace(contentString(out.Choices[0].Message.Content))
	if text == "" && out.Choices[0].Message.ReasoningContent != "" {
		text = strings.TrimSpace(out.Choices[0].Message.ReasoningContent)
	}
	if text == "" {
		return RewriteResult{}, errors.New("llm: empty content")
	}
	return RewriteResult{
		Text: text, ProviderRequestID: out.ID, Model: out.Model,
		PromptTokens: int64(out.Usage.PromptTokens), CompletionTokens: int64(out.Usage.CompletionTokens),
	}, nil
}

// Complete 实现 bind.ModelCompleter：把一段提示词发出去，原样带回模型输出。
//
// 与 Rewrite 的关键差别：
//   - temperature=0、top_p=1、response_format=json_object：绑定要的是「同一输入尽量同一输出」；
//     虽然实测 temperature=0 仍不确定（三次 6/11/7 条），但至少不主动加随机性，
//     确定性靠调用方的缓存拿。
//   - 不做 TrimSpace 以外的任何修剪：剥 markdown 代码块属于校验侧的统一口径，
//     适配器各剥一次会让「格式合法率」说不清是谁在兜底。
func (p *SiliconFlowProvider) Complete(ctx context.Context, req bind.ModelPrompt) (bind.ModelCompletion, error) {
	if strings.TrimSpace(req.System) == "" || strings.TrimSpace(req.User) == "" {
		return bind.ModelCompletion{}, errors.New("llm: binding prompt is empty")
	}
	payload := chatRequest{
		Model: p.model,
		Messages: []chatMessage{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.User},
		},
		Temperature:    0,
		TopP:           1,
		MaxTokens:      1024,
		ResponseFormat: &chatResponseFormat{Type: "json_object"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return bind.ModelCompletion{}, err
	}
	out, err := p.doChat(ctx, body)
	if err != nil {
		return bind.ModelCompletion{}, err
	}
	if len(out.Choices) == 0 {
		return bind.ModelCompletion{}, errors.New("llm: empty choices")
	}
	text := strings.TrimSpace(contentString(out.Choices[0].Message.Content))
	if text == "" && out.Choices[0].Message.ReasoningContent != "" {
		text = strings.TrimSpace(out.Choices[0].Message.ReasoningContent)
	}
	if text == "" {
		return bind.ModelCompletion{}, errors.New("llm: empty content")
	}
	return bind.ModelCompletion{
		Text: text, Model: out.Model, ProviderRequestID: out.ID,
		PromptTokens: int64(out.Usage.PromptTokens), CompletionTokens: int64(out.Usage.CompletionTokens),
	}, nil
}

// ModelName 供调用方拼缓存键：缓存键里的模型名必须与实际发出去的模型一致，
// 否则换了模型仍会命中旧缓存。
func (p *SiliconFlowProvider) ModelName() string { return p.model }

// chatURL 拼 chat/completions。base 末尾已经有 /v1 时不再拼一次——
// 用户给的 `https://host/v1` 直接拼会变成 `/v1/v1/chat/completions`，报 404。
func (p *SiliconFlowProvider) chatURL() string {
	if strings.HasSuffix(p.baseURL, "/v1") {
		return p.baseURL + "/chat/completions"
	}
	return p.baseURL + "/v1/chat/completions"
}

// doChat 是所有文本通道共用的传输层：鉴权、状态码分类、重试标记、响应解码。
// 各方法只负责构造 payload 与解释结果。
func (p *SiliconFlowProvider) doChat(ctx context.Context, body []byte) (chatResponse, error) {
	var out chatResponse
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.chatURL(), bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return out, &RetryableError{Err: fmt.Errorf("llm: request failed: %w", err)}
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return out, readErr
	}
	if resp.StatusCode != http.StatusOK {
		err := providerHTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(data))}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return out, &RetryableError{Err: err, RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
		}
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("llm: decode response: %w", err)
	}
	return out, nil
}

func (p *SiliconFlowProvider) ExtractVisual(ctx context.Context, req VisualExtractRequest) ([]VisualAnchor, error) {
	if len(req.ImagePNG) == 0 {
		return nil, errors.New("llm: image png is required")
	}
	prompt := "请从这页 PPT 截图中提取两件事：① 可见的关键文字事实（标题、数字、单位、型号、日期、图表标签），" +
		"每项形如 {\"kind\":\"visual_text\",\"raw\":\"原文\",\"confidence\":0.7}；" +
		"② 一条整页场景描述（{\"kind\":\"visual_scene\",\"raw\":\"这张图整体展示了什么、主体对象与布局\"}）。" +
		"只输出 JSON 数组，不要输出解释。"
	payload := chatRequest{
		Model: p.visionModel,
		Messages: []chatMessage{
			{Role: "system", Content: "你是 PPT 页面视觉理解助手，只基于截图可见内容提取事实，不猜测。"},
			{Role: "user", Content: []chatContentPart{
				{Type: "text", Text: prompt},
				{Type: "image_url", ImageURL: &chatImageURL{URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(req.ImagePNG)}},
			}},
		},
		Temperature: 0.1,
		MaxTokens:   800,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, &RetryableError{Err: fmt.Errorf("llm: vision request failed: %w", err)}
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode != http.StatusOK {
		err := providerHTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(data))}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return nil, &RetryableError{Err: err, RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
		}
		return nil, err
	}
	var out chatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("llm: decode vision response: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, errors.New("llm: empty vision choices")
	}
	anchors, err := parseVisualAnchors(contentString(out.Choices[0].Message.Content))
	if err != nil {
		return nil, err
	}
	if len(anchors) == 0 && out.Choices[0].Message.ReasoningContent != "" {
		anchors, err = parseVisualAnchors(out.Choices[0].Message.ReasoningContent)
		if err != nil {
			return nil, err
		}
	}
	return anchors, nil
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	// TopP 与 ResponseFormat 只在绑定通道用；omitempty 保证既有的讲稿/视觉请求
	// 序列化字节不变（它们的 golden 还钉着请求体）。
	TopP           float64             `json:"top_p,omitempty"`
	MaxTokens      int                 `json:"max_tokens"`
	ResponseFormat *chatResponseFormat `json:"response_format,omitempty"`
}

type chatResponseFormat struct {
	Type string `json:"type"`
}

type chatMessage struct {
	Role             string `json:"role"`
	Content          any    `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type chatContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL string `json:"url"`
}

type chatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func buildRewritePrompt(req RewriteRequest) string {
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "polish"
	}
	language := strings.TrimSpace(req.Language)
	if language == "" {
		language = "zh-CN"
	}
	instructions := strings.TrimSpace(req.Instructions)
	if instructions == "" {
		instructions = "把原文改写为更适合 PPT 演示讲解的自然口播稿，保持事实和数字不变。"
	}
	return fmt.Sprintf("模式：%s\n语言：%s\n要求：%s\n\n原文：\n%s", mode, language, instructions, req.SourceText)
}

func contentString(v any) string {
	s, _ := v.(string)
	return s
}

func parseVisualAnchors(raw string) ([]VisualAnchor, error) {
	text := strings.TrimSpace(raw)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("llm: empty visual content")
	}
	var anchors []VisualAnchor
	if err := json.Unmarshal([]byte(text), &anchors); err != nil {
		return nil, fmt.Errorf("llm: parse visual anchors: %w", err)
	}
	out := make([]VisualAnchor, 0, len(anchors))
	for _, a := range anchors {
		a.Raw = strings.TrimSpace(a.Raw)
		if a.Raw == "" {
			continue
		}
		if strings.TrimSpace(a.Kind) == "" {
			a.Kind = "visual_text"
		}
		if a.Confidence <= 0 || a.Confidence > 1 {
			a.Confidence = 0.7
		}
		out = append(out, a)
		if len(out) >= 20 {
			break
		}
	}
	return out, nil
}

type providerHTTPError struct {
	status int
	body   string
}

func (e providerHTTPError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("llm: provider status %d", e.status)
	}
	return fmt.Sprintf("llm: provider status %d: %s", e.status, e.body)
}

func (e providerHTTPError) HTTPStatus() int { return e.status }

func retryAfter(raw string) time.Duration {
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(raw); err == nil {
		return time.Until(when)
	}
	return 0
}
