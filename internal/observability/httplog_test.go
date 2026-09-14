package observability

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type recordCapture struct {
	records []slog.Record
}

func (c *recordCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *recordCapture) Handle(_ context.Context, r slog.Record) error {
	c.records = append(c.records, r)
	return nil
}
func (c *recordCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *recordCapture) WithGroup(string) slog.Handler      { return c }

func attrValue(r slog.Record, key string) string {
	var v string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			v = a.Value.String()
			return false
		}
		return true
	})
	return v
}

func TestRequestLoggerLogsAndSetsRequestID(t *testing.T) {
	capture := &recordCapture{}
	logger := slog.New(capture)
	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, _ := r.Context().Value(requestIDKey{}).(string); id == "" {
			t.Error("missing request id in context")
		}
		w.WriteHeader(http.StatusNoContent)
	}), logger)

	req := httptest.NewRequest("GET", "/ppts/foo", nil)
	req.Header.Set("X-PPTS-Tenant-ID", "tenant-1")
	req.Header.Set("X-PPTS-User-ID", "user-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	respID := rec.Header().Get("X-Request-ID")
	if respID == "" {
		t.Fatal("response missing X-Request-ID")
	}
	if len(capture.records) != 1 {
		t.Fatalf("records = %d want 1", len(capture.records))
	}
	r := capture.records[0]
	if attrValue(r, "method") != "GET" || attrValue(r, "status") != "204" ||
		attrValue(r, "tenant") != "tenant-1" || attrValue(r, "user") != "user-1" ||
		attrValue(r, "request_id") != respID {
		t.Fatalf("record attrs method=%q status=%q tenant=%q user=%q reqid=%q want %q",
			attrValue(r, "method"), attrValue(r, "status"), attrValue(r, "tenant"),
			attrValue(r, "user"), attrValue(r, "request_id"), respID)
	}
}
