// 单租户本地模式（SQLite profile）的会话规则。
//
// 本地模式没有账号体系：/auth/config 返回 local=true 时，前端以固定的 tenant_id/user_id
// 自动进入，服务端按开发头部（PPTS_AUTH_DEV_HEADERS）认定身份。
// 因此「退出登录」在本地模式下不是一个可以完成的操作——身份是每个请求都带的头部，
// 清掉本地存储并不改变服务端认定；而登录页也没有任何可用的登录方式
// （email_password=false、无 OIDC），只会显示「登录服务尚未配置」。
//
// 这里把判定收敛成纯函数，App 与 Login 共用，避免两处各写一套条件后再次漂移。

export type LocalIdentity = {
  tenantId: string;
  userId: string;
  tenantType?: string;
  tenantName?: string;
};

type LocalConfigLike = {
  local?: boolean;
  tenant_id?: string;
  user_id?: string;
  tenant_type?: string;
  tenant_name?: string;
};

// localIdentityFromConfig：只有 local 为真**且带齐 tenant/user** 才算本地身份。
// 缺字段时返回 null——凭 local:true 造一个没有 tenant 的会话，服务端同样不可用，
// 前端却会以为"已登录"，把 401 变成一堆看不懂的页面错误。
export function localIdentityFromConfig(cfg: LocalConfigLike | null | undefined): LocalIdentity | null {
  if (!cfg || cfg.local !== true) return null;
  const tenantId = (cfg.tenant_id ?? '').trim();
  const userId = (cfg.user_id ?? '').trim();
  if (!tenantId || !userId) return null;
  return { tenantId, userId, tenantType: cfg.tenant_type, tenantName: cfg.tenant_name };
}

// logoutOutcome：本地模式没有登出。
// 'local-reenter' 表示必须保持/恢复本地身份（并说明原因），而不是清空身份跳登录页；
// 'sign-out' 才是多租户模式下的正常登出。
export function logoutOutcome(local: LocalIdentity | null): 'local-reenter' | 'sign-out' {
  return local ? 'local-reenter' : 'sign-out';
}

// canEnterLocalFromLogin：登录页是否应给出「进入本机实例」入口。
// 仅在拿到完整本地身份时给出——否则仍然是"未配置"的死路，不能凭空造一个登录方式。
export function canEnterLocalFromLogin(local: LocalIdentity | null): boolean {
  return local !== null;
}
