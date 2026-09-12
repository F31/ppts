// Package sidecar 定义桌面 Go sidecar 与 Tauri Rust 之间的受限 IPC 协议（V4.0 §11.5）。
//
// 传输：父子进程标准输入/输出上的有界 NDJSON（一行一个 JSON），不开放本地 HTTP 端口。
// 协议是 ppts 项目设计，不是 Connect RPC；云端仍用 Connect。跨语言消息类型用 Go 类型
// 定义 + 校验；接入 Rust 端时按 JSON Schema 生成对等类型（schemas 随实现补充）。
package sidecar

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ProtocolVersion 是 IPC 协议主版本；与 Rust 侧不匹配时拒绝本地执行（V4.0 §11.5"主版本不匹配拒绝"）。
const ProtocolVersion = 1

// EngineVersion 是引擎实现版本。
const EngineVersion = "0.1.0"

// MaxMessageBytes 是单条消息大小上限（背压/限流，V4.0 §11.5"限制单消息大小"）。
const MaxMessageBytes = 1 << 20

// 协议错误码（结构化 error：code、retryable、trace_id）。
const (
	ErrCodeUnknownMethod  = "UNKNOWN_METHOD"
	ErrCodeInvalidRequest = "INVALID_REQUEST"
	ErrCodeInternal       = "INTERNAL"
	ErrCodeNotImplemented = "NOT_IMPLEMENTED"
)

// ErrOversize 表示单条消息超限。
var ErrOversize = errors.New("sidecar: message exceeds size limit")

// HandshakeRequest 是启动握手请求（client → sidecar）。
type HandshakeRequest struct {
	ProtocolVersion int `json:"protocolVersion"`
}

// HandshakeResponse 是握手响应。主版本不匹配时拒绝本地执行。
type HandshakeResponse struct {
	ProtocolVersion  int      `json:"protocolVersion"`
	EngineVersion    string   `json:"engineVersion"`
	SupportedMethods []string `json:"supportedMethods"`
	Capabilities     []string `json:"capabilities"`
}

// Capabilities 是运行时能力清单（V4.0 §11.8）：只有已安装并通过验证的能力才出现。
// 桌面首版：local_parse（go-pptx 本地解析）。
func Capabilities() []string {
	return []string{"local_parse", "local_engine"}
}

// SupportedMethods 引擎支持的 RPC 方法。
func SupportedMethods() []string {
	return []string{"handshake", "get_capabilities", "ping"}
}

// Request 是通用请求（V4.0 §11.5）。
type Request struct {
	RequestID  string          `json:"requestId"`
	Method     string          `json:"method"`
	DeadlineMS int64           `json:"deadlineMs,omitempty"`
	ProjectID  string          `json:"projectId,omitempty"`
	InputRev   string          `json:"inputRevision,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// Error 是结构化协议错误。
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
	TraceID   string `json:"traceId,omitempty"`
}

// Response 是通用响应（result 与 error 二选一）。
type Response struct {
	RequestID string          `json:"requestId"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *Error          `json:"error,omitempty"`
}

// Engine 是 sidecar 的请求分发器。方法通过 methodFn 表注册；未知方法返回 UNKNOWN_METHOD。
type Engine struct {
	mu      sync.Mutex
	methods map[string]func(ctx context.Context, req *Request) (any, error)
}

// NewEngine 返回带默认方法表的引擎。
func NewEngine() *Engine {
	e := &Engine{methods: map[string]func(context.Context, *Request) (any, error){}}
	e.methods["handshake"] = methodHandshake
	e.methods["get_capabilities"] = methodCapabilities
	e.methods["ping"] = methodPing
	return e
}

func methodHandshake(ctx context.Context, req *Request) (any, error) {
	var h HandshakeRequest
	if len(req.Payload) > 0 {
		if err := json.Unmarshal(req.Payload, &h); err != nil {
			return nil, invalidRequestf("handshake payload: %v", err)
		}
	}
	if h.ProtocolVersion != 0 && h.ProtocolVersion != ProtocolVersion {
		return nil, fmt.Errorf("protocol version mismatch: client %d, engine %d", h.ProtocolVersion, ProtocolVersion)
	}
	return HandshakeResponse{
		ProtocolVersion:  ProtocolVersion,
		EngineVersion:    EngineVersion,
		SupportedMethods: SupportedMethods(),
		Capabilities:     Capabilities(),
	}, nil
}

func methodCapabilities(ctx context.Context, req *Request) (any, error) {
	return map[string]any{"capabilities": Capabilities(), "methods": SupportedMethods()}, nil
}

func methodPing(ctx context.Context, req *Request) (any, error) {
	return map[string]string{"pong": ""}, nil
}

func invalidRequestf(format string, args ...any) error {
	return fmt.Errorf("%s: %s", ErrCodeInvalidRequest, fmt.Sprintf(format, args...))
}

// PeekMethod 判断方法是否已知（供 Rust 侧编码期判断，不实际执行）。
func (e *Engine) PeekMethod(name string) bool {
	_, ok := e.methods[name]
	return ok
}

// Serve 读取 NDJSON 请求流并写出响应流。消息超限按协议拒绝（ErrOversize）且不中断
// 后续请求；解析失败响应 INVALID_REQUEST 后继续下一行。
func (e *Engine) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	br := bufio.NewReader(r)
	bw := bufio.NewWriter(w)
	for {
		line, err := readLineCapped(br)
		if err == io.EOF {
			return nil
		}
		if err == ErrOversize {
			if err := writeResponse(bw, Response{Error: &Error{Code: ErrCodeInvalidRequest, Message: ErrOversize.Error()}}); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := writeResponse(bw, e.handleLine(ctx, line)); err != nil {
			return err
		}
	}
}

func writeResponse(w *bufio.Writer, resp Response) error {
	if _, err := w.Write(mustMarshal(resp)); err != nil {
		return err
	}
	if err := w.WriteByte('\n'); err != nil {
		return err
	}
	return w.Flush()
}

// readLineCapped 读取一行（以 \n 结尾），单行超过 MaxMessageBytes 时把整行读完并
// 丢弃、返回 ErrOversize，读取位置回到下一行起点，便于继续处理后续请求。
func readLineCapped(br *bufio.Reader) ([]byte, error) {
	line := make([]byte, 0, 256)
	overflow := false
	for {
		chunk, err := br.ReadString('\n')
		line = append(line, chunk...)
		hasNL := len(chunk) > 0 && chunk[len(chunk)-1] == '\n'
		switch {
		case overflow && (hasNL || err != nil):
			return nil, ErrOversize
		case err == io.EOF:
			if len(line) > 0 {
				return line, nil // 无换行结尾，由上层按无效 JSON 处理
			}
			return nil, io.EOF
		case hasNL:
			if overflow || len(line) > MaxMessageBytes {
				return nil, ErrOversize
			}
			return line, nil
		case len(line) > MaxMessageBytes:
			overflow = true
		}
	}
}

// HandleJSON 处理单条请求 JSON 并返回响应 JSON（便于直接单元测试与 Rust 转发）。
func (e *Engine) HandleJSON(ctx context.Context, data []byte) ([]byte, error) {
	resp := e.handleLine(ctx, data)
	return mustMarshal(resp), nil
}

func (e *Engine) handleLine(ctx context.Context, data []byte) Response {
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return Response{Error: &Error{Code: ErrCodeInvalidRequest, Message: "invalid json: " + err.Error()}}
	}
	if req.RequestID == "" || req.Method == "" {
		return Response{Error: &Error{Code: ErrCodeInvalidRequest, Message: "missing requestId or method"}}
	}
	fn, ok := e.methods[req.Method]
	if !ok {
		return Response{RequestID: req.RequestID, Error: &Error{Code: ErrCodeUnknownMethod, Message: "unknown method: " + req.Method}}
	}
	result, err := fn(ctx, &req)
	if err != nil {
		code, msg := ErrCodeInternal, err.Error()
		if hasPrefix(msg, ErrCodeInvalidRequest) {
			code = ErrCodeInvalidRequest
		} else if hasPrefix(msg, ErrCodeNotImplemented) {
			code = ErrCodeNotImplemented
		}
		return Response{RequestID: req.RequestID, Error: &Error{Code: code, Message: msg}}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return Response{RequestID: req.RequestID, Error: &Error{Code: ErrCodeInternal, Message: err.Error()}}
	}
	return Response{RequestID: req.RequestID, Result: raw}
}

func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"requestId":"","error":{"code":"INTERNAL","message":"marshal failed"}}`)
	}
	return b
}
