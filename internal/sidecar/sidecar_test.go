package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func reqJSON(id, method, payload string) []byte {
	var p json.RawMessage
	if payload != "" {
		_ = json.Unmarshal([]byte(payload), &p)
	}
	b, _ := json.Marshal(Request{RequestID: id, Method: method, Payload: p})
	return b
}

func TestHandshakeRoundTrip(t *testing.T) {
	e := NewEngine()
	ctx := context.Background()
	in := strings.Join([]string{
		`{"requestId":"h1","method":"handshake","payload":{"protocolVersion":1}}`,
		`{"requestId":"cap1","method":"get_capabilities"}`,
		`{"requestId":"p1","method":"ping"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := e.Serve(ctx, strings.NewReader(in), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("responses: got %d", len(lines))
	}
	var hs struct {
		RequestID string `json:"requestId"`
		Result    struct {
			ProtocolVersion  int      `json:"protocolVersion"`
			EngineVersion    string   `json:"engineVersion"`
			SupportedMethods []string `json:"supportedMethods"`
			Capabilities     []string `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &hs); err != nil {
		t.Fatalf("handshake parse: %v", err)
	}
	if hs.Result.ProtocolVersion != ProtocolVersion || hs.RequestID != "h1" {
		t.Fatalf("handshake: %+v", hs)
	}
}

func TestUnknownMethodAndInvalidRequest(t *testing.T) {
	e := NewEngine()
	ctx := context.Background()
	cases := []struct{ line, code string }{
		{`{"requestId":"r1","method":"no_such"}`, ErrCodeUnknownMethod},
		{`this is not json`, ErrCodeInvalidRequest},
		{`{"requestId":"r3"}`, ErrCodeInvalidRequest},
	}
	for _, c := range cases {
		resp, err := e.HandleJSON(ctx, []byte(c.line))
		if err != nil {
			t.Fatalf("HandleJSON(%q): %v", c.line, err)
		}
		var r Response
		if err := json.Unmarshal(resp, &r); err != nil {
			t.Fatalf("response parse: %v", err)
		}
		if r.Error == nil || r.Error.Code != c.code {
			t.Fatalf("%q → error code %v, want %s", c.line, r.Error, c.code)
		}
	}
}

func TestProtocolVersionMismatchRejected(t *testing.T) {
	e := NewEngine()
	resp, _ := e.HandleJSON(context.Background(), reqJSON("r1", "handshake", `{"protocolVersion":99}`))
	var r Response
	_ = json.Unmarshal(resp, &r)
	if r.Error == nil {
		t.Fatalf("version mismatch should error, got %s", resp)
	}
}

func TestOversizeLineRejectedNotFatal(t *testing.T) {
	e := NewEngine()
	big := `{"requestId":"` + strings.Repeat("x", MaxMessageBytes+100) + `"}`
	in := big + "\n" + `{"requestId":"ok1","method":"ping"}` + "\n"
	var out bytes.Buffer
	if err := e.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	// 超限行返回 INVALID_REQUEST，后续行继续处理，不断流。
	firstOK := false
	for _, ln := range lines {
		var r Response
		if json.Unmarshal([]byte(ln), &r) == nil && r.RequestID == "ok1" && r.Error == nil {
			firstOK = true
		}
	}
	if !firstOK {
		t.Fatalf("oversize line blocked subsequent valid request: %s", out.String())
	}
}
