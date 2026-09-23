package api

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// 账号形态：注册/登录时由输入**自动识别**，并分别落库到邮箱列或手机号列。
//   - 含 "@" → 邮箱；
//   - 其余 → 手机号（允许 + 前缀、空格/连字符/括号分隔，归一为 "+<digits>"）。
type accountKind int

const (
	accountKindEmail accountKind = iota
	accountKindPhone
)

type account struct {
	Kind  accountKind
	Email string
	Phone string
}

// String 返回归一化后的账号标识（用于日志/默认租户名；不用于数据库匹配——落库分列）。
func (a account) String() string {
	if a.Kind == accountKindPhone {
		return a.Phone
	}
	return a.Email
}

// classifyAccount 自动识别并归一化账号。无法识别返回错误。
func classifyAccount(raw string) (account, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return account{}, errors.New("account is required")
	}
	if strings.Contains(s, "@") {
		email := strings.ToLower(s)
		if !validEmail(email) {
			return account{}, errors.New("invalid email")
		}
		return account{Kind: accountKindEmail, Email: email}, nil
	}
	phone, err := normalizePhone(s)
	if err != nil {
		return account{}, err
	}
	return account{Kind: accountKindPhone, Phone: phone}, nil
}

// validAccount 保留为"是否可识别账号"的布尔判定（历史调用/测试）。
func validAccount(account string) bool {
	_, err := classifyAccount(account)
	return err == nil
}

// validEmail 基本邮箱校验：长度、单个 @、本地/域名非空、域名含点、无空格与连续点。
// 不含 RFC 5322 全量语法（不做正则回溯）；用途是拒绝明显非法输入，真实送达由验证邮件保证。
func validEmail(s string) bool {
	if len(s) > 254 || strings.Contains(s, " ") {
		return false
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	if strings.Contains(s, "..") || strings.Contains(s[at+1:], "@") {
		return false
	}
	local, domain := s[:at], s[at+1:]
	if local == "" || domain == "" {
		return false
	}
	// 域名必须含点且不以点结尾（拒绝 a@b、a@b. 这类）。
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	return true
}

// normalizePhone 归一化手机号：去空格/连字符/括号，仅允许一个前导 "+"，数字 5~15 位。
func normalizePhone(s string) (string, error) {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == '+' && i == 0:
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')':
			continue
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			return "", errors.New("invalid phone")
		}
	}
	out := b.String()
	digits := strings.TrimPrefix(out, "+")
	if len(digits) < 5 || len(digits) > 15 {
		return "", errors.New("invalid phone")
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 密码策略（WS6）
// ---------------------------------------------------------------------------

const (
	// 最短 10 位（比原 8 位更严）；最长 72 字节 - bcrypt 输入上限（超出部分被静默截断）。
	minPassword = 10
	maxPassword = 72
)

// commonPasswords 是一份极小的常见弱密码黑名单（全小写）。生产应替换为更大的字典
// 或接入 zxcvbn；此处覆盖最常被撞库的前若干口令，零依赖。
var commonPasswords = map[string]struct{}{
	"password":    {},
	"password1":   {},
	"password123": {},
	"12345678":    {},
	"123456789":   {},
	"1234567890":  {},
	"qwertyuiop":  {},
	"qwerty123":   {},
	"11111111":    {},
	"00000000":    {},
	"abc123456":   {},
	"iloveyou":    {},
	"admin123":    {},
	"letmein123":  {},
	"welcome123":  {},
	"passw0rd":    {},
	"p@ssw0rd":    {},
	"12345678910": {},
}

// validatePassword 校验密码强度：
//   - 长度 10~72（按字节，bcrypt 口径）；
//   - 至少包含 大写/小写/数字/符号 四类中的三类；
//   - 不命中常见弱密码黑名单；
//   - 不含账号本地部分（email @ 前 / 手机号），避免"账号即密码"。
func validatePassword(password, accountLocal string) error {
	if len(password) < minPassword {
		return errors.New("password must be at least 10 characters")
	}
	if len(password) > maxPassword {
		return errors.New("password is too long")
	}
	var upper, lower, digit, symbol bool
	for _, r := range password {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			symbol = true
		}
	}
	classes := 0
	for _, ok := range []bool{upper, lower, digit, symbol} {
		if ok {
			classes++
		}
	}
	if classes < 3 {
		return errors.New("password must include at least 3 of: uppercase, lowercase, digit, symbol")
	}
	if _, weak := commonPasswords[strings.ToLower(password)]; weak {
		return errors.New("password is too common")
	}
	if local := strings.ToLower(strings.TrimSpace(accountLocal)); local != "" {
		if utf8.RuneCountInString(local) >= 4 && strings.Contains(strings.ToLower(password), local) {
			return errors.New("password must not contain your account name")
		}
	}
	return nil
}

// accountLocalPart 返回账号的本地部分（email @ 前；手机号取去掉 + 的完整串），用于密码校验。
func accountLocalPart(acct account) string {
	if acct.Kind == accountKindEmail {
		if at := strings.IndexByte(acct.Email, '@'); at > 0 {
			return acct.Email[:at]
		}
		return acct.Email
	}
	return strings.TrimPrefix(acct.Phone, "+")
}
