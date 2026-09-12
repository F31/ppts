package objectstore

import (
	"strings"
)

// ObjectKey 是对象键的强类型表示，规范化形式为：
//
//	{tenant_id}/{project_id}/{revision|artifact_id}/{asset_type}/{asset_id}.{ext}
//
// 各段只允许 [a-zA-Z0-9._-]，不允许 "/" 嵌套；Parse 与构造器都做严格校验，
// 防止 Zip Slip 式路径穿越与跨租户前缀伪造。
type ObjectKey struct {
	TenantID  string // 服务端已授权租户 UUID（不是客户端传入值）
	ProjectID string // 项目 ID
	Revision  string // 源文件版本（src-03）或成品快照 ID
	AssetType string // source | render | audio | subtitle | artifact | work
	AssetID   string // 资产 ID，可含内部点号
	Ext       string // 扩展名（不含点），可为空
}

// Segment 合法性：允许小写/大写字母、数字、下划线、点、连字符。
func validSegment(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// Validate 校验键各段均合法且非空。
func (k ObjectKey) Validate() error {
	for _, v := range []string{
		k.TenantID, k.ProjectID, k.Revision, k.AssetType, k.AssetID,
	} {
		if !validSegment(v) {
			return ErrKeyInvalid
		}
	}
	if k.Ext != "" && !validSegment(k.Ext) {
		return ErrKeyInvalid
	}
	return nil
}

// String 输出规范化键路径。
func (k ObjectKey) String() string {
	if k.Ext == "" {
		return k.TenantID + "/" + k.ProjectID + "/" + k.Revision + "/" + k.AssetType + "/" + k.AssetID
	}
	return k.TenantID + "/" + k.ProjectID + "/" + k.Revision + "/" + k.AssetType + "/" + k.AssetID + "." + k.Ext
}

// Prefix 返回不含 asset_id 的前缀，用于"临时资产前缀清理"等批量操作。
func (k ObjectKey) Prefix() string {
	return k.TenantID + "/" + k.ProjectID + "/" + k.Revision + "/" + k.AssetType + "/"
}

// Parse 从规范化字符串解析 ObjectKey；任何格式不符均返回 ErrKeyInvalid。
func Parse(s string) (ObjectKey, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 5 {
		return ObjectKey{}, ErrKeyInvalid
	}
	// 末段可含 .ext，但只允许一个扩展名后缀。
	id, ext := parts[4], ""
	if dot := strings.LastIndexByte(id, '.'); dot >= 0 {
		ext = id[dot+1:]
		id = id[:dot]
	}
	if id == "" {
		return ObjectKey{}, ErrKeyInvalid
	}
	k := ObjectKey{
		TenantID: parts[0], ProjectID: parts[1],
		Revision: parts[2], AssetType: parts[3],
		AssetID: id, Ext: ext,
	}
	if err := k.Validate(); err != nil {
		return ObjectKey{}, err
	}
	return k, nil
}

// EnsureTenant 断言键的租户前缀与 authorizedTenant 一致；
// 不一致返回 ErrTenantMismatch。预签名签发与业务写路径都必须先过这一道。
func (k ObjectKey) EnsureTenant(authorizedTenant string) error {
	if k.TenantID != authorizedTenant {
		return ErrTenantMismatch
	}
	return nil
}
