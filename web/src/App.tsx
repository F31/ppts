import { useCallback, useEffect, useMemo, useState } from 'react';
import { listMembers, type ClientIdentity } from './api';
import { AppShell } from './AppShell';
import { clearAllIdentity, completeOIDCCallback, storedAccessToken, storedDevIdentity, saveDevIdentity } from './auth';
import { navigate, useRoute } from './router';
import { Home } from './pages/Home';
import { Jobs } from './pages/Jobs';
import { Login } from './pages/Login';
import { ProjectArtifacts } from './pages/ProjectArtifacts';
import { ProjectEditor } from './pages/ProjectEditor';
import { Projects } from './pages/Projects';
import { SettingsAudit } from './pages/SettingsAudit';
import { SettingsDictionary } from './pages/SettingsDictionary';
import { SettingsMembers } from './pages/SettingsMembers';
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
import { PublicAdmin } from './pages/PublicAdmin';

function AppContent() {
  const route = useRoute();
  const { t } = useI18n();
  const [identity, setIdentity] = useState<ClientIdentity | null>(() => {
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

  const loginOIDC = useCallback(() => {
    // 由 Login 页触发 startOIDCLogin 跳转，授权码回调后 setIdentity。
  }, []);

  const logout = useCallback(() => {
    clearAllIdentity();
    setIdentity(null);
    navigate('/login');
  }, []);

  const session: Session = useMemo(
    () => ({ identity, loginDev, loginOIDC, logout }),
    [identity, loginDev, loginOIDC, logout]
  );

  // 未完成 OIDC 回调判定前不闪登录页（深链接登录 A05）。
  if (!oidcDone) {
    return <div className="splash-screen">{t('app.restoring')}</div>;
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
  const [role, setRole] = useState<Role | undefined>(undefined);
  // B4-M1：角色是否已解析完成。未完成时不渲染越权路由的"无权"占位（避免闪现），
  // 也不提前暴露管理菜单，保证菜单与后端一致（A22）。
  const [roleReady, setRoleReady] = useState(false);

  // 角色从服务端授权读取（TenantService.Members）；失败不阻塞页面。
  // members 未配置时服务端返回 Unimplemented → role 保持 undefined，此时服务端 requireRole 放行，
  // 前端同样不隐藏（见 permissions.ts can()）。
  useEffect(() => {
    let cancelled = false;
    listMembers(identity)
      .then((members) => {
        if (cancelled) return;
        const mine = members.find((member) => member.userId === identity.userId);
        setRole(mine?.role ?? 'ROLE_VIEWER');
      })
      .catch(() => {
        if (!cancelled) setRole(undefined);
      })
      .finally(() => {
        if (!cancelled) setRoleReady(true);
      });
    return () => {
      cancelled = true;
    };
  }, [identity]);

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
        return <Projects identity={identity} />;
      }
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
          // 模型服务：读写与测试均要求 ADMIN（gateway.go:85）。
          default:
            return guard('gateway.manage', <SettingsModels identity={identity} />);
        }
      case 'home':
      default:
        if (section === 'home') return <Home identity={identity} />;
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
      {content}
    </AppShell>
  );
}

function isPublicRoute(parts: string[]): boolean {
  return parts[0] === 'explore' || (parts[0] === 'watch' && parts.length >= 2);
}

function PublicApp({ parts, query }: { parts: string[]; query: URLSearchParams }) {
  const section = parts[0] ?? 'explore';
  let content: React.ReactNode;
  if (section === 'watch' && parts[1]) {
    content = <Watch id={parts[1]} />;
  } else {
    content = <Explore />;
  }
  return <PublicShell>{content}</PublicShell>;
}

export function App() {
  return <AppContent />;
}