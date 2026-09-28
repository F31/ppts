// 登录身份与「请求头」偏好（语言 / 源版本）
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

export type ClientIdentity = {
  tenantId: string;
  userId: string;
  accessToken?: string;
  // cookieSession 为 true 时，会话令牌由后端 HttpOnly Cookie 承载（邮箱/手机注册登录路径），
  // 前端不持有 token，也不再发送 Authorization/X-PPTS 头（同源 Cookie 自动携带）。
  cookieSession?: boolean;
  // account 为登录账号（邮箱或手机号），仅邮箱注册/登录路径写入，用于界面展示；
  // 旧会话（localStorage 无此字段）回退显示 userId。
  account?: string;
  // accountKind 为账号形态（email/phone），用于界面提示。
  accountKind?: string;
  // emailVerified 表示邮箱是否已验证；未验证时界面提示"验证邮箱"。
  emailVerified?: boolean;
  // operator 表示该用户是运营商（PPTS_OPERATOR_USER_IDS），可访问 /admin 运营后台。
  operator?: boolean;
  // tenantName 为租户显示名，仅邮箱注册/登录路径从后端 tenants.name 带回；
  // 旧会话或开发/OIDC 登录无此字段时，界面回退显示 tenantId。
  tenantName?: string;
  // tenantType 为租户类型（personal/organization），由邮箱注册/登录与本地模式带回。
  // 个人账号（单成员）隐藏"成员管理/邀请协作者"入口；旧会话/开发/OIDC 无此字段时
  // 按组织处理（不隐藏），与后端不按类型分叉的宽松语义一致。
  tenantType?: string;
};

export function identityHeaders(identity: ClientIdentity): Record<string, string> {
  // Cookie 会话：令牌在 HttpOnly Cookie 中，同源请求自动携带，不额外加认证头。
  if (identity.cookieSession) {
    return {};
  }
  if (identity.accessToken) {
    return { Authorization: `Bearer ${identity.accessToken}` };
  }
  return {
    'X-PPTS-Tenant-ID': identity.tenantId,
    'X-PPTS-User-ID': identity.userId
  };
}

// 项目讲稿语言偏好：讲稿/配音等端点按请求头 Accept-Language 选择语言。
// 编辑器设置后，所有 API 请求携带该语言，避免"一键成稿生成英文但面板仍显示中文"。
let preferredScriptLanguage: string | undefined;

export function setScriptLanguagePreference(language?: string): void {
  preferredScriptLanguage = language && language.trim() ? language.trim() : undefined;
}

// 当前查看的源版本：讲稿/来源/配音按 (源版本, slide_id) 隔离。slide_id 只在单个 PPTX
// 内唯一，改版重传会重复；编辑器设置后所有 API 请求携带该版本，读写落在正确的版本上。
let preferredSourceRevision: number | undefined;

export function setSourceRevisionPreference(revision?: number): void {
  preferredSourceRevision = revision && revision > 0 ? revision : undefined;
}

export function languageHeader(): Record<string, string> {
  const headers: Record<string, string> = {};
  if (preferredScriptLanguage) {
    headers['X-PPTS-Language'] = preferredScriptLanguage;
    headers['Accept-Language'] = preferredScriptLanguage;
  }
  if (preferredSourceRevision) {
    headers['X-PPTS-Source-Revision'] = String(preferredSourceRevision);
  }
  return headers;
}
