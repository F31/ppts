import { useEffect, useState } from 'react';
import { Link, useRoute } from './router';
import { useSession } from './session';
import { useI18n } from './i18n';
import { useTheme } from './ThemeContext';
import { roleKey, type Role } from './types';
import { can, type Capability } from './permissions';
import { useDialogA11y } from './a11y';
import { CommandPalette } from './components/CommandPalette';

export type NavItem = { key: string; to: string; labelKey: string; icon: string; need?: Capability; operatorOnly?: boolean };

export const primaryNav: NavItem[] = [
  { key: 'home', to: '/home', labelKey: 'nav.home', icon: '🏠' },
  { key: 'projects', to: '/projects', labelKey: 'nav.projects', icon: '📊' },
  { key: 'jobs', to: '/jobs', labelKey: 'nav.jobs', icon: '⚙️' },
  // B5-M2：跨项目成品库（owner 级），带 need 由侧栏按角色过滤，保持「菜单与后端一致」（A22）。
  { key: 'library', to: '/library', labelKey: 'nav.library', icon: '🗃️', need: 'library.view' },
  // 第二批：运营商后台，仅 identity.operator 可见（后端另有 403 兜底）。
  { key: 'admin', to: '/admin', labelKey: 'nav.admin', icon: '🛡️', operatorOnly: true },
  { key: 'settings', to: '/settings/models', labelKey: 'nav.settings', icon: '🔧' }
];

// SettingsNavItem 扩展 NavItem：hideForPersonal 标记"个人账号（单成员租户）不渲染"的入口。
export type SettingsNavItem = {
  key: string;
  to: string;
  labelKey: string;
  need: Capability;
  hideForPersonal?: boolean;
};

// 设置子菜单（作用域分组：个人不占本轮——C-7 已定不做并移除入口；租户项按方案 §12）。
// B4-M1：每项声明所需能力，菜单按角色过滤，做到「菜单与后端一致」（A22）。
// 个人账号（tenantType=personal）另按 hideForPersonal 过滤：成员管理对其无意义（恒 1 个成员）。
export const settingsMenus: SettingsNavItem[] = [
  { key: 'models', to: '/settings/models', labelKey: 'nav.settingsModels', need: 'gateway.manage' },
  { key: 'members', to: '/settings/members', labelKey: 'nav.settingsMembers', need: 'member.manage', hideForPersonal: true },
  { key: 'dictionary', to: '/settings/dictionary', labelKey: 'nav.settingsDictionary', need: 'project.read' },
  { key: 'usage', to: '/settings/usage', labelKey: 'nav.settingsUsage', need: 'project.read' },
  { key: 'tags', to: '/settings/tags', labelKey: 'nav.settingsTags', need: 'project.organize' },
  { key: 'audit', to: '/settings/audit', labelKey: 'nav.settingsAudit', need: 'audit.read' },
  { key: 'public', to: '/settings/public', labelKey: 'nav.settingsPublic', need: 'public.publish' }
];

function activeKey(parts: string[]): string {
  if (parts.length === 0) return 'home';
  const head = parts[0];
  if (head === 'settings') return 'settings';
  if (head === 'projects') {
    // /projects/:id/editor 属于讲解项目
    return 'projects';
  }
  return head;
}

export function AppShell({ children, role, roleReady = true }: { children: React.ReactNode; role?: Role; roleReady?: boolean }) {
  const route = useRoute();
  const { identity, logout } = useSession();
  const { t, lang, toggle } = useI18n();
  const { theme, toggle: toggleTheme } = useTheme();
  const [userMenuOpen, setUserMenuOpen] = useState(false);
  const [profileOpen, setProfileOpen] = useState(false);
  const profileDialogRef = useDialogA11y<HTMLDivElement>(() => setProfileOpen(false));
  // B5-M1：命令面板开关 + 全局 Cmd/Ctrl+K 唤起。
  const [paletteOpen, setPaletteOpen] = useState(false);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')) {
        e.preventDefault();
        setPaletteOpen((open) => !open);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);
  const active = activeKey(route.parts);
  const tenantShort = identity ? identity.tenantId.split('-').pop() ?? identity.tenantId : '-';

  // 个人账号（单成员租户）隐藏成员管理入口（hideForPersonal）。
  const personalTenant = identity?.tenantType === 'personal';
  // B4-M1：只渲染当前角色确实可用的设置项（菜单与后端一致，A22）。
  // 角色解析中先按"零权限"处理，避免向 Viewer 闪现管理菜单。
  const visibleSettings = settingsMenus.filter(
    (item) => roleReady && can(role, item.need) && !(item.hideForPersonal && personalTenant)
  );
  // B5-M2：侧栏主导航同样按 need 过滤（library 仅 owner 可见），保持「菜单与后端一致」（A22）。
  // 第二批：operatorOnly 项仅对 operator 渲染。
  const visiblePrimary = primaryNav.filter(
    (item) =>
      (!item.need || (roleReady && can(role, item.need))) && (!item.operatorOnly || identity?.operator === true)
  );
  // 「设置」入口落到第一个可见子页；解析中先落用量页（仅需已认证，对任何角色都安全）。
  const settingsHome = visibleSettings[0]?.to ?? '/settings/usage';

  return (
    <div className="app-shell">
      <aside className="console-sidebar">
        <Link to="/home" className="brand-block" title={t('shell.title')}>
          <span className="brand-mark">{t('shell.brand')}</span>
          <span className="brand-tenant" title={identity?.tenantId ?? ''}>
            {tenantShort}
          </span>
        </Link>
        <nav className="primary-nav" aria-label={t('shell.mainNav')}>
          {visiblePrimary.map((item) => (
            <Link
              key={item.key}
              to={item.key === 'settings' ? settingsHome : item.to}
              className={`nav-item ${active === item.key ? 'selected' : ''}`}
            >
              <span className="nav-icon">{item.icon}</span>
              <span>{t(item.labelKey)}</span>
            </Link>
          ))}
        </nav>
        {active === 'settings' && visibleSettings.length > 0 && (
          <nav className="secondary-nav" aria-label={t('shell.settingsNav')}>
            {visibleSettings.map((item) => (
              <Link key={item.key} to={item.to} className={`nav-sub ${route.parts[1] === item.key ? 'selected' : ''}`}>
                {t(item.labelKey)}
              </Link>
            ))}
          </nav>
        )}
      </aside>
      <div className="console-main">
        <header className="console-topbar">
          <div className="breadcrumb">
            <span className="eyebrow">{t('shell.brand')}</span>
            <span className="breadcrumb-sep">/</span>
            <span>{t(primaryNav.find((item) => item.key === active)?.labelKey ?? 'shell.console')}</span>
          </div>
          <div className="topbar-actions">
            <button
              type="button"
              className="palette-trigger"
              onClick={() => setPaletteOpen(true)}
              title={t('palette.trigger')}
              aria-label={t('palette.trigger')}
              aria-keyshortcuts="Meta+K Control+K"
            >
              ⌘K
            </button>
            <button type="button" className="theme-toggle" onClick={toggleTheme} title={t('shell.themeToggle')} aria-label={t('shell.themeToggle')}>
              {theme === 'dark' ? '☀️' : '🌙'}
            </button>
            <button type="button" className="lang-toggle" onClick={toggle} title={t('shell.languageToggle')}>
              {lang === 'zh' ? t('lang.en') : t('lang.zh')}
            </button>
            <div className="user-area">
              <button type="button" className="user-trigger" aria-haspopup="menu" aria-expanded={userMenuOpen} onClick={() => setUserMenuOpen((v) => !v)}>
                <span className="user-avatar">{(identity?.account ?? identity?.userId ?? '?').slice(0, 1).toUpperCase()}</span>
                <span className="user-meta">
                  <strong>{identity?.account ?? identity?.userId ?? '-'}</strong>
                  <small>{role ? t(roleKey[role]) : identity?.tenantId ?? ''}</small>
                </span>
              </button>
              {userMenuOpen && (
                <div className="user-menu" role="menu">
                  <button type="button" onClick={() => { setUserMenuOpen(false); setProfileOpen(true); }}>
                    {t('user.profile')}
                  </button>
                  {/* 用户菜单「设置」落到当前角色可见的第一个设置页（原硬编码 /settings/members 对非管理员是假入口）。 */}
                  <Link to={settingsHome} className="user-menu-link" onClick={() => setUserMenuOpen(false)}>
                    {t('user.settings')}
                  </Link>
                  <button type="button" className="danger" onClick={() => { setUserMenuOpen(false); logout(); }}>
                    {t('user.logout')}
                  </button>
                </div>
              )}
            </div>
          </div>
        </header>
        <main className="console-content">{children}</main>
      </div>
      {profileOpen && identity && (() => {
        const displayName = identity.account ?? identity.userId;
        const showUserSubId = !!identity.account && identity.account !== identity.userId;
        const tenantName = identity.tenantName;
        const showTenantSubId = !!tenantName && tenantName !== identity.tenantId;
        return (
          <div className="modal-backdrop" role="dialog" aria-modal="true" aria-label={t('user.title')} ref={profileDialogRef}>
            <section className="modal-card profile-modal">
              <header>
                <div>
                  <span className="eyebrow">{t('user.title')}</span>
                  <h2 className="profile-name">{displayName}</h2>
                  {showUserSubId && (
                    <small className="profile-subid">{t('user.userId')}: {identity.userId}</small>
                  )}
                </div>
                <button type="button" onClick={() => setProfileOpen(false)}>{t('common.close')}</button>
              </header>
              <dl className="profile-dl">
                <div>
                  <dt>{t('user.tenant')}</dt>
                  <dd>
                    <strong className="profile-name">{tenantName ?? identity.tenantId}</strong>
                    {showTenantSubId && (
                      <small className="profile-subid">{identity.tenantId}</small>
                    )}
                  </dd>
                </div>
                <div>
                  <dt>{t('user.role')}</dt>
                  <dd>{role ? t(roleKey[role]) : t('user.roleFallback')}</dd>
                </div>
                <div>
                  <dt>{t('user.loginMethod')}</dt>
                  <dd>{identity.accessToken ? t('user.oidc') : t('user.dev')}</dd>
                </div>
              </dl>
            </section>
          </div>
        );
      })()}
      {paletteOpen && (
        <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} role={role} roleReady={roleReady} />
      )}
    </div>
  );
}
