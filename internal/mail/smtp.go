package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// smtpSender 基于 net/smtp 的轻量发送器（STARTTLS 优先，可选隐式 TLS/无加密）。
// 不引入第三方依赖；如需 OAuth/高级重试可后续替换实现。
type smtpSender struct {
	host     string
	port     int
	username string
	password string
	from     string
	// mode: starttls（默认 587）| tls（隐式 TLS，465）| none（内网明文，如 MailHog）
	mode string
}

func newSMTPFromEnv() (Sender, error) {
	host := strings.TrimSpace(os.Getenv("PPTS_SMTP_HOST"))
	if host == "" {
		return nil, errors.New("mail: PPTS_SMTP_HOST is required for smtp backend")
	}
	port := 587
	if p := strings.TrimSpace(os.Getenv("PPTS_SMTP_PORT")); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return nil, fmt.Errorf("mail: invalid PPTS_SMTP_PORT %q", p)
		}
		port = n
	}
	from := strings.TrimSpace(os.Getenv("PPTS_SMTP_FROM"))
	if from == "" {
		from = strings.TrimSpace(os.Getenv("PPTS_SMTP_USER"))
	}
	if from == "" {
		return nil, errors.New("mail: PPTS_SMTP_FROM is required")
	}
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("PPTS_SMTP_TLS")))
	if mode == "" {
		mode = "starttls"
	}
	switch mode {
	case "starttls", "tls", "none":
	default:
		return nil, fmt.Errorf("mail: invalid PPTS_SMTP_TLS %q (want starttls|tls|none)", mode)
	}
	return &smtpSender{
		host:     host,
		port:     port,
		username: os.Getenv("PPTS_SMTP_USER"),
		password: os.Getenv("PPTS_SMTP_PASSWORD"),
		from:     from,
		mode:     mode,
	}, nil
}

func (s *smtpSender) Send(ctx context.Context, m Message) error {
	addr := net.JoinHostPort(s.host, strconv.Itoa(s.port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if s.mode == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: s.host})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mail: dial %s: %w", addr, err)
	}

	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mail: smtp client: %w", err)
	}
	defer c.Close()

	if s.mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: s.host}); err != nil {
				return fmt.Errorf("mail: starttls: %w", err)
			}
		}
	}
	if s.username != "" {
		auth := smtp.PlainAuth("", s.username, s.password, s.host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}
	if err := c.Mail(s.from); err != nil {
		return fmt.Errorf("mail: mail from: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return fmt.Errorf("mail: rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	if _, err := w.Write(buildMessage(s.from, m)); err != nil {
		w.Close()
		return fmt.Errorf("mail: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: close: %w", err)
	}
	return c.Quit()
}

// buildMessage 组装 RFC 5322 纯文本邮件（UTF-8）。
func buildMessage(from string, m Message) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + m.To + "\r\n")
	b.WriteString("Subject: " + m.Subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(m.Text)
	b.WriteString("\r\n")
	return []byte(b.String())
}
