package traceprop

import (
	"context"
	"encoding/hex"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestFromContextEmptyWithoutSpan(t *testing.T) {
	if tp := FromContext(context.Background()); tp != "" {
		t.Fatalf("FromContext = %q want empty", tp)
	}
}

func validSpanContext() trace.SpanContext {
	var tid trace.TraceID
	var sid trace.SpanID
	hex.Decode(tid[:], []byte("0102030405060708090a0b0c0d0e0f10"))
	hex.Decode(sid[:], []byte("1112131415161718"))
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: tid, SpanID: sid,
		TraceFlags: trace.FlagsSampled,
	})
}

func TestRoundTripTraceParent(t *testing.T) {
	sc := validSpanContext()
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	raw := FromContext(ctx)
	if raw == "" {
		t.Fatalf("FromContext returned empty")
	}
	got := SpanContextFromTraceParent(raw)
	if !got.IsValid() {
		t.Fatalf("parsed span context invalid from %q", raw)
	}
	if got.TraceID() != sc.TraceID() || got.SpanID() != sc.SpanID() {
		t.Fatalf("mismatch: got %s/%s want %s/%s", got.TraceID(), got.SpanID(), sc.TraceID(), sc.SpanID())
	}
}

func TestSpanContextFromTraceParentEmpty(t *testing.T) {
	if sc := SpanContextFromTraceParent(""); sc.IsValid() {
		t.Fatalf("empty traceparent should be invalid")
	}
}
