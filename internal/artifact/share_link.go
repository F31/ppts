package artifact

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

// ShareLink 是**成品级**分享链接（M6-3）。
//
// 与 project.ShareLink 的关键差别只有一个，但它是整个 M6-3 的目的：
// 锚点是 ArtifactID 而不是 ProjectID。项目内容会变，成品不变——客户手里的链接
// 必须始终指向他当时拿到的那一版，否则 html_bundle 作为交付物就不成立。
//
// 字段语义与 project_share_links 逐条对齐，便于运维与前端复用同一套心智模型。
type ShareLink struct {
	ID         string
	TenantID   string
	ArtifactID string
	// ProjectID 是冗余列：只为「按项目列出分享」与审计可读，不参与内容定位。
	ProjectID string
	Token     string
	// AccessMode 沿用 project 包的枚举（view_only / view_and_comment）。
	// 成品播放器目前只读，两种取值的行为差别留给评论能力落地时兑现。
	AccessMode        string
	PasswordProtected bool
	ExpiresAt         *time.Time
	Revoked           bool
	CreatedBy         string
	CreatedAt         time.Time
	// LastAccessedAt 为 nil 表示尚未被匿名访问过。
	LastAccessedAt *time.Time
}

// NewShareLink 是创建链接的入参。
type NewShareLink struct {
	ArtifactID string
	ProjectID  string
	AccessMode string
	// PasswordHash 为空表示不设口令（此时 PasswordProtected 由实现置 false）。
	PasswordHash string
	CreatedBy    string
	ExpiresAt    *time.Time
}

// ErrShareLinkNotFound 覆盖「不存在 / 已撤销 / 已过期」三种情况。
//
// 三者归一成同一个错误是刻意的：匿名端点必须对它们返回同一个 404，
// 否则响应差异本身就是一把枚举有效链接的尺子。
var ErrShareLinkNotFound = errors.New("artifact: share link not found")

// ShareLinkStore 是成品分享链接的存储端口。
//
// 读路径分两类，权限模型完全不同，故接口上并列而不合并：
//   - 管理端（List / Revoke / PasswordHash / Touch）：必须带 tenantID，走租户上下文；
//   - 匿名端（GetByToken）：不带 tenantID，token 即授权，靠 anon_read 策略放行。
type ShareLinkStore interface {
	CreateShareLink(ctx context.Context, tenantID string, in NewShareLink) (*ShareLink, error)
	ListShareLinks(ctx context.Context, tenantID, artifactID string) ([]*ShareLink, error)
	RevokeShareLink(ctx context.Context, tenantID, linkID string) error
	// GetShareLinkByToken 只返回「未撤销且未过期」的行；其余一律 ErrShareLinkNotFound。
	GetShareLinkByToken(ctx context.Context, token string) (*ShareLink, error)
	// ShareLinkPasswordHash 单独取值，避免把哈希塞进匿名读路径的 SELECT 列。
	ShareLinkPasswordHash(ctx context.Context, tenantID, linkID string) (string, error)
	TouchShareLinkAccess(ctx context.Context, tenantID, linkID string) error
}

// shareLinkValidity 把「此刻可用」的定义收在一处，供两套 SQL 方言各自引用，
// 并由 share_link_test.go 用同一张表断言两边语义一致。
//
// 为什么不做成同一段 SQL：PG 有 now()，SQLite 的 datetime('now') 是
// 「2006-01-02 15:04:05」而本仓库存的是「...T...Z」，字符串直比会错序。
// 所以 SQLite 侧把当前时间作为参数传入。语义相同，写法必须不同——硬凑成
// 一段字符串只会让其中一边悄悄不成立。
const (
	// pgShareLinkValid 与迁移 0032 的 anon_read 策略同句式。
	pgShareLinkValid = `revoked = false AND (expires_at IS NULL OR expires_at > now())`
	// sqShareLinkValid 的 ? 绑定 db.Now()。
	sqShareLinkValid = `revoked = 0 AND (expires_at IS NULL OR expires_at > ?)`
)

// UsableAt 是 pgShareLinkValid / sqShareLinkValid 的 Go 版本，只用于测试与展示
// （例如管理端把「已过期」灰掉）。**判断是否放行必须走 SQL**，不能用它替代查询条件：
// 否则一旦两处漂移，漂移方向总是「库里的行已经撤了，代码还认为有效」。
func (l *ShareLink) UsableAt(t time.Time) bool {
	if l == nil || l.Revoked {
		return false
	}
	return l.ExpiresAt == nil || l.ExpiresAt.After(t)
}

// newShareToken 生成 32 位十六进制 token（128 位随机）。
//
// 用 crypto/rand 而不是 uuid：uuid v4 只有 122 位随机且格式带横线，
// 去横线后仍是 32 位但含固定版本位。这里直接取满 128 位。
func newShareToken() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// normalizeShareAccessMode 兜底空值为 view_only（与 project 包的默认一致）。
func normalizeShareAccessMode(mode string) string {
	if mode == "" {
		return "view_only"
	}
	return mode
}
