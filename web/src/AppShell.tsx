import { useState } from 'react';
import { Link, useRoute } from './router';
import { useSession } from './session';
import { useI18n } from './i18n';
import { roleKey, type Role } from './types';

export type NavItem = { key: string; to: string; labelKey: string; icon: string };

const primaryNav: NavItem[] = [
  { key: 'home', to: '/home', labelKey: 'nav.home', icon: '🏠' },
  { key: 'projects', to: '/projects', labelKey: 'nav.projects', icon: '📊' },
  { key: 'jobs', to: '/jobs', labelKey: 'nav.jobs', icon: '⚙️' },
  { key: 'settings', to: '/settings/models', labelKey: 'nav.settings', icon: '🔧' }
];

// 设置子菜单（作用域分组：个人不占本轮；租户项按方案 §12）
const settingsMenus: Array<{ key: string; to: string; labelKey: string }> = [
  { key: 'models', to: '/settings/models', labelKey: 'nav.settingsModels' },
  { key: 'members', to: '/settings/members', labelKey: 'nav.settingsMembers' },
  { key: 'dictionary', to: '/settings/dictionary', labelKey: 'nav.settingsDictionary' },
  { key: 'usage', to: '/settings/usage', labelKey: 'nav.settingsUsage' },
  { key: 'audit', to: '/settings/audit', labelKey: 'nav.settingsAudit' }
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

export function AppShell({ children, role }: { children: React.ReactNode; role?: Role }) {
  const route = useRoute();
  const { identity, logout } = useSession();
  const { t, lang, toggle } = useI18n();
  const [userMenuOpen, setUserMenuOpen] = useState(false);
  const [profileOpen, setProfileOpen] = useState(false);
  const active = activeKey(route.parts);
  const tenantShort = identity ? identity.tenantId.split('-').pop() ?? identity.tenantId : '-';

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
          {primaryNav.map((item) => (
            <Link key={item.key} to={item.to} className={`nav-item ${active === item.key ? 'selected' : ''}`}>
              <span className="nav-icon">{item.icon}</span>
              <span>{t(item.labelKey)}</span>
            </Link>
          ))}
        </nav>
        {active === 'settings' && (
          <nav className="secondary-nav" aria-label={t('shell.settingsNav')}>
            {settingsMenus.map((item) => (
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
            <button type="button" className="lang-toggle" onClick={toggle} title={t('shell.languageToggle')}>
              {lang === 'zh' ? t('lang.en') : t('lang.zh')}
            </button>
            <div className="user-area">
              <button type="button" className="user-trigger" aria-haspopup="menu" aria-expanded={userMenuOpen} onClick={() => setUserMenuOpen((v) => !v)}>
                <span className="user-avatar">{identity?.userId.slice(0, 1).toUpperCase() ?? '?'}</span>
                <span className="user-meta">
                  <strong>{identity?.userId ?? '-'}</strong>
                  <small>{role ? t(roleKey[role]) : identity?.tenantId ?? ''}</small>
                </span>
              </button>
              {userMenuOpen && (
                <div className="user-menu" role="menu">
                  <button type="button" onClick={() => { setUserMenuOpen(false); setProfileOpen(true); }}>
                    {t('user.profile')}
                  </button>
                  <Link to="/settings/members" className="user-menu-link" onClick={() => setUserMenuOpen(false)}>
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
      {profileOpen && identity && (
        <div className="modal-backdrop" role="dialog" aria-label={t('user.title')}>
          <section className="modal-card profile-modal">
            <header>
              <div>
                <span className="eyebrow">{t('user.title')}</span>
                <h2>{identity.userId}</h2>
              </div>
              <button type="button" onClick={() => setProfileOpen(false)}>{t('common.close')}</button>
            </header>
            <dl className="profile-dl">
              <div>
                <dt>{t('user.userId')}</dt>
                <dd>{identity.userId}</dd>
              </div>
              <div>
                <dt>{t('user.tenantId')}</dt>
                <dd>{identity.tenantId}</dd>
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
      )}
    </div>
  );
}
