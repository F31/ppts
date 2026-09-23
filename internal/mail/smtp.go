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

// SMTPConfig 是 SMTP 发送配置（可由环境变量或数据库中的消息服务配置构建）。
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	// From 是信封发件地址（MAIL FROM），必须是纯地址（如 no-reply@126.com）。
	// 若传入 "显示名 <地址>" 形式，normalizeSMTPConfig 会拆分出地址与显示名。
	From string
	// FromName 是邮件头 From 的显示名（可选）。
	FromName string
	// Mode: starttls（默认，587）| tls（隐式 TLS，465）| none（内网明文，如 MailHog）
	Mode string
}

// smtpSender 基于 net/smtp 的轻量发送器（STARTTLS 优先，可选隐式 TLS/无加密）。
type smtpSender struct {
	cfg SMTPConfig
}

// NewSMTPSender 校验并构建 SMTP 发送器。config 中的 Host/From 必填。
func NewSMTPSender(cfg SMTPConfig) (Sender, error) {
	normalized, err := normalizeSMTPConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &smtpSender{cfg: normalized}, nil
}

func normalizeSMTPConfig(cfg SMTPConfig) (SMTPConfig, error) {
	cfg.Host = strings.TrimSpace(cfg.Host)
	if cfg.Host == "" {
		return cfg, errors.New("mail: smtp host is required")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		cfg.Port = 587
	}
	// 拆分 "显示名 <地址>"：信封只能用纯地址；显示名进邮件头。
	if from := strings.TrimSpace(cfg.From); from != "" {
		if i := strings.IndexByte(from, '<'); i >= 0 {
			if j := strings.IndexByte(from[i:], '>'); j > 0 {
				addr := strings.TrimSpace(from[i+1 : i+j])
				name := strings.Trim(strings.TrimSpace(from[:i]), `"`)
				if addr != "" {
					from = addr
					if cfg.FromName == "" {
						cfg.FromName = name
					}
				}
			}
		}
		cfg.From = from
	}
	cfg.FromName = strings.TrimSpace(cfg.FromName)
	cfg.From = strings.TrimSpace(cfg.From)
	if cfg.From == "" {
		cfg.From = strings.TrimSpace(cfg.Username)
	}
	if cfg.From == "" {
		return cfg, errors.New("mail: smtp from address is required")
	}
	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	if cfg.Mode == "" {
		cfg.Mode = "starttls"
	}
	switch cfg.Mode {
	case "starttls", "tls", "none":
	default:
		return cfg, fmt.Errorf("mail: invalid smtp tls mode %q (want starttls|tls|none)", cfg.Mode)
	}
	return cfg, nil
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
	return NewSMTPSender(SMTPConfig{
		Host:     host,
		Port:     port,
		Username: os.Getenv("PPTS_SMTP_USER"),
		Password: os.Getenv("PPTS_SMTP_PASSWORD"),
		From:     os.Getenv("PPTS_SMTP_FROM"),
		Mode:     os.Getenv("PPTS_SMTP_TLS"),
	})
}

func (s *smtpSender) Send(ctx context.Context, m Message) error {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if s.cfg.Mode == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: s.cfg.Host})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mail: dial %s: %w", addr, err)
	}

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mail: SMTP handshake failed (mode=%s port=%d): %w — check that port and encryption match (126/163 use 465 with implicit TLS)", s.cfg.Mode, s.cfg.Port, err)
	}
	defer c.Close()

	if s.cfg.Mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: s.cfg.Host}); err != nil {
				return fmt.Errorf("mail: starttls: %w", err)
			}
		}
	}
	if s.cfg.Username != "" {
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}
	if err := c.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("mail: mail from: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return fmt.Errorf("mail: rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	if _, err := w.Write(buildMessage(s.headerFrom(), m)); err != nil {
		w.Close()
		return fmt.Errorf("mail: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: close: %w", err)
	}
	return c.Quit()
}

// headerFrom 返回邮件头 From 值（含显示名时用 "Name <addr>"）。
func (s *smtpSender) headerFrom() string {
	if s.cfg.FromName == "" {
		return s.cfg.From
	}
	return s.cfg.FromName + " <" + s.cfg.From + ">"
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
