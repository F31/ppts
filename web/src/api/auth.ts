// 邮箱/手机注册登录、密码重置、邮箱验证
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import { ConnectError, requestJSON, requestVoid } from '../core';
// ---- 邮箱/手机自助注册与登录（B5-M4 + 第一批认证闭环）----
export type EmailAuthResult = {
  access_token: string;
  tenant_id: string;
  tenant_name: string;
  tenant_type: string;
  user_id: string;
  account: string;
  account_kind?: string;
  email_verified?: boolean;
  operator?: boolean;
};

// AuthConfig 是后端认证能力探测结果。
// local=true 表示单租户本地模式（SQLite profile）：无需登录，前端以 tenant_id/user_id
// 固定身份自动进入。email_password 控制邮箱登录入口显隐。
export type AuthConfig = {
  email_password: boolean;
  // mail_configured 表示后端已配置邮件发送（未配置时验证/重置链接会打到服务端日志）。
  mail_configured?: boolean;
  // require_email_verified 表示后端强制邮箱验证后才能登录。
  require_email_verified?: boolean;
  // registration_enabled=false 时隐藏"创建账户"入口（封闭注册）。
  registration_enabled?: boolean;
  // invite_required=true 时注册需填写邀请码。
  invite_required?: boolean;
  local?: boolean;
  tenant_id?: string;
  user_id?: string;
  tenant_type?: string;
  // tenant_name 仅本地模式回传（种子租户显示名），供个人信息弹窗/欢迎语展示。
  tenant_name?: string;
};

// getAuthConfig 探测后端认证能力（无认证端点）；登录页据此显隐邮箱入口，
// App 启动时据此判断是否本地模式自动进入。
export async function getAuthConfig(): Promise<AuthConfig> {
  return requestJSON<AuthConfig>({ method: 'GET', url: '/auth/config', label: 'auth config' });
}

// registerEmail 自助注册：账号支持邮箱或手机号（后端自动识别并分列落库），返回会话。
// 请求体字段名与后端 internal/api/authhandlers.go 的 json tag 一一对应。
export async function registerEmail(params: {
  account: string;
  password: string;
  accountType: 'personal' | 'organization';
  orgName?: string;
  inviteCode?: string;
  username?: string;
  fullName?: string;
  gender?: string;
  birthDate?: string;
  phone?: string;
}): Promise<EmailAuthResult> {
  return (await postAuth('/auth/register', {
    account: params.account,
    email: params.account,
    password: params.password,
    account_type: params.accountType,
    org_name: params.orgName ?? '',
    invite_code: params.inviteCode ?? '',
    username: params.username ?? '',
    full_name: params.fullName ?? '',
    gender: params.gender ?? '',
    birth_date: params.birthDate ?? '',
    phone: params.phone ?? ''
  })) as EmailAuthResult;
}

// loginEmail 账号登录（邮箱或手机号）：后端校验凭证并建立 Cookie 会话。
export async function loginEmail(params: { account: string; password: string }): Promise<EmailAuthResult> {
  return (await postAuth('/auth/email-login', { account: params.account, email: params.account, password: params.password })) as EmailAuthResult;
}

// logoutSession 清除后端 Cookie 会话。失败必须上抛（ConnectError）：
// 放行会让界面显示"已登出"而服务端会话仍有效——把失败读成成功。
export async function logoutSession(): Promise<void> {
  await requestVoid({ method: 'POST', url: '/auth/logout', label: 'logout' });
}

// verifyEmail 消费邮箱验证令牌。
export async function verifyEmail(token: string): Promise<void> {
  await postAuth('/auth/verify-email', { token });
}

// resendVerification 重新发送验证邮件（统一响应，不泄露账号是否存在）。
export async function resendVerification(account: string): Promise<void> {
  await postAuth('/auth/resend-verification', { account, email: account });
}

// forgotPassword 请求密码重置邮件（统一响应）。
export async function forgotPassword(account: string): Promise<void> {
  await postAuth('/auth/forgot-password', { account, email: account });
}

// resetPassword 用令牌设置新密码。
export async function resetPassword(token: string, password: string): Promise<void> {
  await postAuth('/auth/reset-password', { token, password });
}

// postAuth 通用无认证 POST（注册/登录/验证/重置），解析后端 {code,message} 错误体。
async function postAuth(path: string, body: Record<string, unknown>): Promise<unknown> {
  const data = await requestJSON<unknown>({
    method: 'POST',
    url: path,
    headers: { 'Content-Type': 'application/json' },
    body,
    label: `POST ${path}`
  });
  return data ?? {};
}
