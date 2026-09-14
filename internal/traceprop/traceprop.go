// Package traceprop 提供 W3C traceparent 的读写助手，供 API 与 worker 跨进程关联异步任务 span。
package traceprop

import (
	"context"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

var traceContext = propagation.TraceContext{}

// FromContext 返回 ctx 中当前 span 的 W3C traceparent；无有效 span 时返回空串。
func FromContext(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-" + sc.TraceFlags().String()
}

// SpanContextFromTraceParent 从 W3C traceparent 解析出远程 SpanContext；空串/非法返回无效值。
func SpanContextFromTraceParent(traceparent string) trace.SpanContext {
	if traceparent == "" {
		return trace.SpanContext{}
	}
	carrier := propagation.HeaderCarrier{}
	carrier.Set("traceparent", traceparent)
	ctx := traceContext.Extract(context.Background(), carrier)
	return trace.SpanContextFromContext(ctx)
}
