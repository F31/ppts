// Package mail 提供可插拔的邮件发送能力（注册验证 / 密码重置通知）。
//
// 后端由 PPTS_MAIL_BACKEND 选择：
//   - smtp     ：真实 SMTP（PPTS_SMTP_* 配置），生产使用；
//   - log      ：把邮件内容打到日志（开发/联调，无需 SMTP）；
//   - disabled ：禁用（默认，或未配置 SMTP 时）。
//
// 未配置时返回 nil，调用方据此降级：需要发信的流程会明确失败或提示"邮件服务未配置"，
// 而不是静默假装已发送。
package mail

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
)

// Message 是一封纯文本（可选 HTML）邮件。
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Sender 发送邮件。
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// ErrNotConfigured 表示未配置任何邮件后端。
var ErrNotConfigured = errors.New("mail: no backend configured")

// FromEnv 按环境变量构建发送器；未配置返回 (nil, nil)。
func FromEnv(logger *log.Logger) (Sender, error) {
	backend := strings.ToLower(strings.TrimSpace(os.Getenv("PPTS_MAIL_BACKEND")))
	switch backend {
	case "smtp":
		return newSMTPFromEnv()
	case "log":
		if logger == nil {
			logger = log.Default()
		}
		return &LogSender{Logger: logger}, nil
	case "disabled":
		return nil, nil
	case "":
		// 未显式指定：配置了 SMTP host 就用 SMTP，否则禁用。
		if strings.TrimSpace(os.Getenv("PPTS_SMTP_HOST")) != "" {
			return newSMTPFromEnv()
		}
		return nil, nil
	default:
		return nil, errors.New("mail: unknown PPTS_MAIL_BACKEND " + backend)
	}
}

// LogSender 把邮件内容写入日志（仅供开发/联调；不要在生产使用会泄露验证链接）。
type LogSender struct {
	Logger *log.Logger
}

func (s *LogSender) Send(_ context.Context, m Message) error {
	s.Logger.Printf("mail(dev): to=%s subject=%q\n%s", m.To, m.Subject, m.Text)
	return nil
}
