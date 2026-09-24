package app

import (
	"testing"
	"time"

	"github.com/F31/ppts/internal/integrations/llm"
	"github.com/F31/ppts/internal/pipeline"
)

type llmHTTPStatusErr struct {
	status int
}

func (e llmHTTPStatusErr) Error() string   { return "llm: provider status " + intToStr(e.status) }
func (e llmHTTPStatusErr) HTTPStatus() int { return e.status }

func intToStr(v int) string {
	if v == 429 {
		return "429"
	}
	return "500"
}

func TestClassifyLLMError429IsRetryable(t *testing.T) {
	// 429（无 Retry-After）→ 退避可重试，不再一击失败整单。
	result := classifyLLMError(&llm.RetryableError{Err: llmHTTPStatusErr{status: 429}})
	r, ok := result.(*pipeline.RetryError)
	if !ok {
		t.Fatalf("429 must be retryable, got %T: %v", result, result)
	}
	if r.At.Before(time.Now().Add(10 * time.Second)) {
		t.Fatalf("429 backoff too short: At=%v", r.At)
	}
	// Retry-After 显式生效时优先尊重。
	ra := classifyLLMError(&llm.RetryableError{Err: llmHTTPStatusErr{status: 429}, RetryAfter: 90 * time.Second})
	raRetry, ok := ra.(*pipeline.RetryError)
	if !ok || raRetry.At.Before(time.Now().Add(80*time.Second)) {
		t.Fatalf("Retry-After must be respected: %v", ra)
	}
	// 非可重试错误原样透传，不伪装成 RetryError。
	plain := llmHTTPStatusErr{status: 501}
	if got := classifyLLMError(plain); got == nil {
		t.Fatal("plain error must pass through")
	} else if _, ok := got.(*pipeline.RetryError); ok {
		t.Fatal("plain error must not become retryable")
	}
	// 5xx 仍可重试（一直如此）。
	five := classifyLLMError(&llm.RetryableError{Err: llmHTTPStatusErr{status: 503}})
	if _, ok := five.(*pipeline.RetryError); !ok {
		t.Fatalf("5xx must stay retryable, got %T", five)
	}
}
