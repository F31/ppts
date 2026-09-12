package objectstore

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func testKey() ObjectKey {
	return ObjectKey{
		TenantID: "t-0001", ProjectID: "p-0007",
		Revision: "src-03", AssetType: "audio", AssetID: "seg-07-02", Ext: "mp3",
	}
}

func TestObjectKeyStringParseRoundTrip(t *testing.T) {
	k := testKey()
	s := k.String()
	got, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got != k {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, k)
	}
	if want := "t-0001/p-0007/src-03/audio/seg-07-02.mp3"; s != want {
		t.Fatalf("String: got %q want %q", s, want)
	}
}

func TestObjectKeyValidateRejectsBadSegments(t *testing.T) {
	cases := []ObjectKey{
		{TenantID: "t/../../etc", ProjectID: "p", Revision: "r", AssetType: "a", AssetID: "x"},
		{TenantID: "t", ProjectID: "p", Revision: "r", AssetType: "a", AssetID: "x"}, // missing? no, valid
		{TenantID: "", ProjectID: "p", Revision: "r", AssetType: "a", AssetID: "x"},
		{TenantID: "t", ProjectID: "p", Revision: "r", AssetType: "a", AssetID: "x y"},
		{TenantID: "t", ProjectID: "p", Revision: "r", AssetType: "a", AssetID: "x", Ext: "../"},
	}
	for _, c := range cases {
		if c == (ObjectKey{TenantID: "t", ProjectID: "p", Revision: "r", AssetType: "a", AssetID: "x"}) {
			continue // 全合法样例，跳过
		}
		if err := c.Validate(); !errors.Is(err, ErrKeyInvalid) {
			t.Errorf("Validate(%+v) = %v, want ErrKeyInvalid", c, err)
		}
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", "a/b", "a/b/c/d/e/f", "/a/b/c/d", "a//b/c/d", "a/b/c/d/"} {
		if _, err := Parse(s); !errors.Is(err, ErrKeyInvalid) {
			t.Errorf("Parse(%q) = %v, want ErrKeyInvalid", s, err)
		}
	}
}

func TestEnsureTenant(t *testing.T) {
	k := testKey()
	if err := k.EnsureTenant("t-0001"); err != nil {
		t.Fatalf("same tenant should pass: %v", err)
	}
	if err := k.EnsureTenant("t-evil"); !errors.Is(err, ErrTenantMismatch) {
		t.Fatalf("cross tenant should fail: got %v", err)
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	s := NewSigner([]byte("test-secret"))
	k := testKey()
	tok, err := s.Sign("t-0001", k, OpRead, time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	gotKey, gotOp, err := s.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if gotKey != k || gotOp != OpRead {
		t.Fatalf("Verify mismatch: key=%+v op=%s", gotKey, gotOp)
	}
}

func TestSignRejectsCrossTenant(t *testing.T) {
	s := NewSigner([]byte("test-secret"))
	k := testKey()
	if _, err := s.Sign("t-evil", k, OpRead, time.Minute); !errors.Is(err, ErrTenantMismatch) {
		t.Fatalf("cross-tenant sign should be rejected: got %v", err)
	}
}

func TestVerifyRejectsTampered(t *testing.T) {
	s := NewSigner([]byte("test-secret"))
	k := testKey()
	tok, _ := s.Sign("t-0001", k, OpRead, time.Minute)

	cases := map[string]string{
		"bad version":      "v0:" + tok[strings.Index(tok, ":")+1:],
		"tampered payload": tamperPayload(tok),
	}
	for name, tk := range cases {
		if _, _, err := s.Verify(tk); !errors.Is(err, ErrSignatureInvalid) {
			t.Errorf("%s: got %v, want ErrSignatureInvalid", name, err)
		}
	}
	// 错误 secret 的校验器
	s2 := NewSigner([]byte("other-secret"))
	if _, _, err := s2.Verify(tok); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("wrong secret: got %v, want ErrSignatureInvalid", err)
	}
}

func TestVerifyExpired(t *testing.T) {
	s := NewSigner([]byte("test-secret"))
	k := testKey()
	tok, err := s.Sign("t-0001", k, OpRead, -time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, _, err := s.Verify(tok); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expired token: got %v, want ErrTokenExpired", err)
	}
}

func TestLocalPutGetDelete(t *testing.T) {
	ls := NewLocal(t.TempDir(), nil)
	ctx := context.Background()
	k := testKey()

	if err := ls.Put(ctx, k, strings.NewReader("hello audio"), ObjectMeta{ContentType: "audio/mpeg"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, meta, err := ls.Get(ctx, k)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "hello audio" || meta.Size != int64(len("hello audio")) {
		t.Fatalf("Get content mismatch: data=%q size=%d", data, meta.Size)
	}
	if err := ls.Delete(ctx, k); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := ls.Get(ctx, k); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("Get after delete: got %v, want ErrObjectNotFound", err)
	}
}

func TestLocalRejectsTraversal(t *testing.T) {
	ls := NewLocal(t.TempDir(), nil)
	ctx := context.Background()
	k := ObjectKey{TenantID: "t", ProjectID: "p", Revision: "r", AssetType: "a", AssetID: "..%2F.."}
	// 非法字符白名单直接拒绝
	if err := ls.Put(ctx, k, strings.NewReader("x"), ObjectMeta{}); !errors.Is(err, ErrKeyInvalid) {
		t.Fatalf("traversal key should be rejected: got %v", err)
	}
}

func TestLocalSignedURLFlow(t *testing.T) {
	ls := NewLocal(t.TempDir(), []byte("test-secret"))
	ctx := context.Background()
	k := testKey()
	if err := ls.Put(ctx, k, strings.NewReader("payload"), ObjectMeta{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	raw, err := ls.SignedURL(ctx, k, OpRead, time.Minute)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	gotKey, gotOp, err := ls.ParseSignedURL(raw)
	if err != nil {
		t.Fatalf("ParseSignedURL: %v", err)
	}
	if gotKey != k || gotOp != OpRead {
		t.Fatalf("ParseSignedURL mismatch: key=%+v op=%s", gotKey, gotOp)
	}
	if err := EnsureOp(gotOp, OpRead); err != nil {
		t.Fatalf("EnsureOp: %v", err)
	}
	if err := EnsureOp(gotOp, OpWrite); err == nil {
		t.Fatalf("EnsureOp should reject operation mismatch")
	}
}

func TestLocalSignedURLNeedsSecret(t *testing.T) {
	ls := NewLocal(t.TempDir(), nil)
	_, err := ls.SignedURL(context.Background(), testKey(), OpRead, time.Minute)
	if !errors.Is(err, ErrOperationNotSupported) {
		t.Fatalf("SignedURL without secret: got %v, want ErrOperationNotSupported", err)
	}
	if err := ls.ApplyLifecyclePolicy(context.Background(), "b", LifecyclePolicy{}); !errors.Is(err, ErrOperationNotSupported) {
		t.Fatalf("lifecycle on local: got %v, want ErrOperationNotSupported", err)
	}
}

func tamperPayload(token string) string {
	colon := strings.IndexByte(token, ':')
	dot := strings.IndexByte(token[colon+1:], '.') + colon + 1
	return token[:colon+1] + flipB64(token[colon+1:colon+2]) + token[colon+2:dot] + token[dot:]
}

// flipB64 翻转 base64url 有效 payload 字符，保证解码内容变化。
func flipB64(c string) string {
	if c == "A" {
		return "B"
	}
	return "A"
}
