import { useCallback, useEffect, useMemo, useState } from 'react';
import { getAuthConfig, listMembers, type ClientIdentity } from './api';
import { AppShell } from './AppShell';
import { clearAllIdentity, completeOIDCCallback, storedAccessToken, storedDevIdentity, storedLocalIdentity, saveLocalIdentity, saveDevIdentity, storedIdentity, saveIdentity } from './auth';
import { navigate, useRoute } from './router';
import { Home } from './pages/Home';
import { Jobs } from './pages/Jobs';
import { Login } from './pages/Login';
import { ProjectArtifacts } from './pages/ProjectArtifacts';
import { Library } from './pages/Library';
import { ProjectEditor } from './pages/ProjectEditor';
import { Projects } from './pages/Projects';
import { SettingsAudit } from './pages/SettingsAudit';
import { SettingsDictionary } from './pages/SettingsDictionary';
import { SettingsMembers } from './pages/SettingsMembers';
import { SettingsTags } from './pages/SettingsTags';
import { SettingsModels } from './pages/SettingsModels';
import { SettingsUsage } from './pages/SettingsUsage';
import { SessionContext, type Session } from './session';
import { useI18n } from './i18n';
import type { Role } from './types';
import { NoPermissionNotice, PermissionBoundary, RolePendingNotice } from './components/PermissionBoundary';
import type { Capability } from './permissions';
import { PublicShell } from './PublicShell';
import { Explore } from './pages/Explore';
import { Watch } from './pages/Watch';
import { SharedWatch } from './pages/SharedWatch';
import { PublicAdmin } from './pages/PublicAdmin';
import { describeApiError } from './apiError';

function AppContent() {
  const route = useRoute();
  const { t } = useI18n();
  const [identity, setIdentity] = useState<ClientIdentity | null>(() => {
    // 邮箱登录身份优先：含真实 tenantId/userId，刷新后直接恢复。
    const email = storedIdentity();
    if (email) return email;
    // 单租户本地模式（SQLite）：后端无登录，刷新后直接以固定本地身份进入。
    const local = storedLocalIdentity();
    if (local) return { tenantId: local.tenantId, userId: local.userId };
    const dev = storedDevIdentity();
    const token = storedAccessToken();
    if (dev) {
      return { tenantId: dev.tenantId, userId: dev.userId, accessToken: token || undefined };
    }
    if (token) {
      return { tenantId: '00000000-0000-0000-0000-000000000000', userId: 'oidc-user', accessToken: token };
    }
    return null;
  });
  const [oidcDone, setOidcDone] = useState(false);
  // localAuthDone：本地模式探测是否完成（避免探测期间闪现登录页）。
  const [localAuthDone, setLocalAuthDone] = useState(false);

  // 单租户本地模式自动进入：探测 /auth/config，local=true 时以固定本地身份进入，无需登录。
  // PostgreSQL 多租户模式返回 email_password（无 local 字段），不触发自动进入。
  useEffect(() => {
    if (identity) {
      setLocalAuthDone(true);
      return;
    }
    let cancelled = false;
    getAuthConfig()
      .then((cfg) => {
        if (cancelled) return;
        if (cfg.local && cfg.tenant_id && cfg.user_id) {
          saveLocalIdentity({ tenantId: cfg.tenant_id, userId: cfg.user_id });
          setIdentity({ tenantId: cfg.tenant_id, userId: cfg.user_id });
        }
      })
      .catch(() => {
        // 探测失败（后端不可达/非本地模式）：退回登录页逻辑。
      })
      .finally(() => {
        if (!cancelled) setLocalAuthDone(true);
      });
    return () => {
      cancelled = true;
    };
  }, [identity]);

  // OIDC 回调：登录成功后优先按 OIDC 身份进入。
  useEffect(() => {
    let cancelled = false;
    completeOIDCCallback()
      .then((token) => {
        if (cancelled || !token) return;
        const dev = storedDevIdentity();
        if (dev) {
          setIdentity({ tenantId: dev.tenantId, userId: dev.userId, accessToken: token });
        } else {
          setIdentity({ tenantId: '00000000-0000-0000-0000-000000000000', userId: 'oidc-user', accessToken: token });
        }
      })
      .catch(() => {
        // 回调状态不符等失败：保留现有会话。
      })
      .finally(() => {
        if (!cancelled) setOidcDone(true);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const loginDev = useCallback((next: ClientIdentity) => {
    saveDevIdentity({ tenantId: next.tenantId, userId: next.userId });
    setIdentity({ tenantId: next.tenantId, userId: next.userId, accessToken: undefined });
  }, []);

  const loginEmail = useCallback((next: ClientIdentity) => {
    saveIdentity(next);
    setIdentity(next);
  }, []);

  const loginOIDC = useCallback(() => {
    // 由 Login 页触发 startOIDCLogin 跳转，授权码回调后 setIdentity。
  }, []);

  const logout = useCallback(() => {
    clearAllIdentity();
    setIdentity(null);
    navigate('/login');
  }, []);

  const session: Session = useMemo(
    () => ({ identity, loginDev, loginOIDC, loginEmail, logout }),
    [identity, loginDev, loginOIDC, loginEmail, logout]
  );

  // 未完成 OIDC 回调 / 本地模式探测判定前不闪登录页（深链接登录 A05）。
  if (!oidcDone || !localAuthDone) {
    return <div className="splash-screen">{t('app.restoring')}</div>;
  }
  // 公开区（作品广场 / 广场作品播放 / 私密分享播放）无需登录：必须在身份判定之前返回，
  // 否则匿名访客会被强制跳登录页。此前 isPublicRoute/PublicApp 从未被调用，导致
  // /explore 与 /watch/{id} 实际不可达（死代码），本轮随 #95 一并接回。
  if (isPublicRoute(route.parts)) {
    return (
      <SessionContext.Provider value={session}>
        <PublicApp parts={route.parts} query={route.query} />
      </SessionContext.Provider>
    );
  }

  if (!identity) {
    return (
      <SessionContext.Provider value={session}>
        <Login />
      </SessionContext.Provider>
    );
  }
  return (
    <SessionContext.Provider value={session}>
      <AuthenticatedApp identity={identity} parts={route.parts} query={route.query} />
    </SessionContext.Provider>
  );
}

function AuthenticatedApp({ identity, parts, query }: { identity: ClientIdentity; parts: string[]; query: URLSearchParams }) {
  const { t } = useI18n();
  const [role, setRole] = useState<Role | undefined>(undefined);
  // B4-M1：角色是否已解析完成。未完成时不渲染越权路由的"无权"占位（避免闪现），
  // 也不提前暴露管理菜单，保证菜单与后端一致（A22）。
  const [roleReady, setRoleReady] = useState(false);
  // A26：角色读取失败时必须显式告知（并给重试），不能静默把权限判定放宽——
  // 否则会向低权限用户闪现管理入口，随后每个请求都 403。
  const [roleError, setRoleError] = useState('');
  const [roleReloadKey, setRoleReloadKey] = useState(0);

  // 角色从服务端授权读取（TenantService.Members）；失败不阻塞页面。
  // members 未配置时服务端返回 Unimplemented → role 保持 undefined，此时服务端 requireRole 放行，
  // 前端同样不隐藏（见 permissions.ts can()）。
  useEffect(() => {
    let cancelled = false;
    setRoleError('');
    setRoleReady(false);
    listMembers(identity)
      .then((members) => {
        if (cancelled) return;
        const mine = members.find((member) => member.userId === identity.userId);
        setRole(mine?.role ?? 'ROLE_VIEWER');
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        // 保留 role=undefined（对齐服务端 nil-reader 放行语义），但把原因暴露到界面上。
        setRole(undefined);
        setRoleError(describeApiError(err, t('app.roleLoadFailed'), t));
      })
      .finally(() => {
        if (!cancelled) setRoleReady(true);
      });
    return () => {
      cancelled = true;
    };
  }, [identity, t, roleReloadKey]);

  const section = parts[0] ?? 'home';

  // B4-M1 路由级权限边界（A22）：直接输入 URL 也不能进入越权界面。
  // 角色解析中渲染轻量占位（不闪"无权"），确认无权则渲染统一占位，绝不渲染可点击的越权入口。
  const guard = (need: Capability, node: React.ReactNode) => (
    <PermissionBoundary
      role={role}
      need={need}
      ready={roleReady}
      pendingFallback={<RolePendingNotice />}
      fallback={<NoPermissionNotice />}
    >
      {node}
    </PermissionBoundary>
  );

  const content = (() => {
    switch (section) {
      case 'projects': {
        const projectId = parts[1];
        // 成品列表端点要求 EDITOR（artifact.go:27），故整页按 artifact.list 门控。
        if (projectId && parts[2] === 'artifacts') return guard('artifact.list', <ProjectArtifacts identity={identity} projectId={projectId} />);
        if (projectId && parts[2] === 'editor' || (projectId && !parts[2])) {
          return <ProjectEditor identity={identity} projectId={projectId} draftRequested={query.get('draft') === '1'} role={role} roleReady={roleReady} />;
        }
        return <Projects identity={identity} role={role} roleReady={roleReady} />;
      }
      case 'library':
        // B5-M2 跨项目成品库：owner 级（与后端 GET /artifacts requireRole RoleOwner 一致）。
        return guard('library.view', <Library identity={identity} />);
      case 'jobs':
        return <Jobs identity={identity} />;
      case 'settings':
        switch (parts[1]) {
          // 成员管理：变更角色/移除要求 ADMIN（tenant.go:114,142），整页门控。
          case 'members':
            return guard('member.manage', <SettingsMembers identity={identity} />);
          // 词典：仅要求已认证（pronunciation.go:23-26），不做角色门控。
          case 'dictionary':
            return <SettingsDictionary identity={identity} />;
          // 用量：仅要求已认证（tenant.go:231），不做角色门控。
          case 'usage':
            return <SettingsUsage identity={identity} />;
          // 审计：ADMIN（tenant.go:326,370）。
          case 'audit':
            return guard('audit.read', <SettingsAudit identity={identity} />);
          // 公开区：含"我的发布"（成员均可用）与"审核队列"（ADMIN，组件内再门控）。
          case 'public':
            return <PublicAdmin identity={identity} role={role} />;
          // 标签与分组：editor 及以上（project.organize，对应后端 requireRole RoleEditor）。
          case 'tags':
            return guard('project.organize', <SettingsTags identity={identity} />);
          // 模型服务：读写与测试均要求 ADMIN（gateway.go:85）。
          default:
            return guard('gateway.manage', <SettingsModels identity={identity} />);
        }
      case 'home':
      default:
        if (section === 'home') return <Home identity={identity} role={role} roleReady={roleReady} />;
        // 未知路由回首页。
        navigate('/home');
        return null;
    }
  })();

  if (content === null) {
    return null;
  }
  return (
    <AppShell role={role} roleReady={roleReady}>
      {/* A26：角色未解析成功时明确告知，并提供重试；不静默放宽权限判定。 */}
      {roleError && (
        <div className="load-failure" role="alert">
          <p className="form-error">{roleError}</p>
          <button type="button" onClick={() => setRoleReloadKey((key) => key + 1)}>{t('common.retry')}</button>
        </div>
      )}
      {content}
    </AppShell>
  );
}

// isPublicRoute 判定无需登录即可访问的公开区路径：
//   - /explore                 公开作品广场
//   - /watch/{publicId}        广场作品匿名播放（B5-M3 改用不可反推的 public_id）
//   - /shared/{token}          私密分享匿名播放（#95），与广场互不干扰
function isPublicRoute(parts: string[]): boolean {
  return (
    parts[0] === 'explore' ||
    (parts[0] === 'watch' && parts.length >= 2) ||
    (parts[0] === 'shared' && parts.length >= 2)
  );
}

function PublicApp({ parts, query }: { parts: string[]; query: URLSearchParams }) {
  const section = parts[0] ?? 'explore';
  let content: React.ReactNode;
  if (section === 'watch' && parts[1]) {
    content = <Watch publicId={parts[1]} />;
  } else if (section === 'shared' && parts[1]) {
    content = <SharedWatch token={parts[1]} />;
  } else {
    content = <Explore />;
  }
  return <PublicShell>{content}</PublicShell>;
}

export function App() {
  return <AppContent />;
}