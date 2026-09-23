package mail

import (
	"strings"
	"testing"
)

func TestNormalizeSMTPConfigSplitsFrom(t *testing.T) {
	s, err := newNormalizedSMTP(SMTPConfig{
		Host: "smtp.126.com", Port: 465, Username: "user@126.com", Password: "authcode",
		From: `PPTS 通知 <user@126.com>`, Mode: "tls",
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if s.cfg.From != "user@126.com" {
		t.Fatalf("envelope from = %q, want user@126.com", s.cfg.From)
	}
	if s.cfg.FromName != "PPTS 通知" {
		t.Fatalf("from name = %q", s.cfg.FromName)
	}
	if got := s.headerFrom(); got != "PPTS 通知 <user@126.com>" {
		t.Fatalf("header from = %q", got)
	}
}

func TestNormalizeSMTPConfigBareFrom(t *testing.T) {
	s, err := newNormalizedSMTP(SMTPConfig{Host: "h", From: "a@b.com", Mode: "starttls"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if s.cfg.From != "a@b.com" || s.cfg.FromName != "" {
		t.Fatalf("bare from parsed wrong: %+v", s.cfg)
	}
	if s.headerFrom() != "a@b.com" {
		t.Fatalf("header from = %q", s.headerFrom())
	}
}

func TestNormalizeSMTPConfigErrors(t *testing.T) {
	if _, err := NewSMTPSender(SMTPConfig{From: "a@b.com"}); err == nil {
		t.Fatal("missing host should fail")
	}
	if _, err := NewSMTPSender(SMTPConfig{Host: "h"}); err == nil {
		t.Fatal("missing from should fail")
	}
	if _, err := NewSMTPSender(SMTPConfig{Host: "h", From: "a@b.com", Mode: "ssl"}); err == nil {
		t.Fatal("invalid tls mode should fail")
	}
}

func TestBuildMessageEnvelopeVsHeader(t *testing.T) {
	raw := string(buildMessage("PPTS <no-reply@126.com>", Message{To: "x@y.com", Subject: "Hi", Text: "body"}))
	if !strings.HasPrefix(raw, "From: PPTS <no-reply@126.com>\r\n") {
		t.Fatalf("header From wrong:\n%s", raw)
	}
	if !strings.Contains(raw, "To: x@y.com\r\n") || !strings.Contains(raw, "\r\n\r\nbody\r\n") {
		t.Fatalf("message malformed:\n%s", raw)
	}
}

// newNormalizedSMTP 复用 normalizeSMTPConfig 并返回带配置的发送器（测试内部用）。
func newNormalizedSMTP(cfg SMTPConfig) (*smtpSender, error) {
	s, err := NewSMTPSender(cfg)
	if err != nil {
		return nil, err
	}
	return s.(*smtpSender), nil
}
