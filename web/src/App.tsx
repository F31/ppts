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
import { PublicShell } from './PublicShell';
import { Explore } from './pages/Explore';
import { Watch } from './pages/Watch';

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

  // 角色从服务端授权读取（TenantService.Members）；失败不阻塞页面。
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
      });
    return () => {
      cancelled = true;
    };
  }, [identity]);

  const section = parts[0] ?? 'home';

  const content = (() => {
    switch (section) {
      case 'projects': {
        const projectId = parts[1];
        if (projectId && parts[2] === 'artifacts') return <ProjectArtifacts identity={identity} projectId={projectId} />;
        if (projectId && parts[2] === 'editor' || (projectId && !parts[2])) {
          return <ProjectEditor identity={identity} projectId={projectId} draftRequested={query.get('draft') === '1'} />;
        }
        return <Projects identity={identity} />;
      }
      case 'jobs':
        return <Jobs identity={identity} />;
      case 'settings':
        switch (parts[1]) {
          case 'members':
            return <SettingsMembers identity={identity} />;
          case 'dictionary':
            return <SettingsDictionary identity={identity} />;
          case 'usage':
            return <SettingsUsage identity={identity} />;
          case 'audit':
            return <SettingsAudit identity={identity} />;
          default:
            return <SettingsModels identity={identity} />;
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
    <AppShell role={role}>
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