import { useState } from 'react';
import { Link, useRoute } from './router';
import { useSession } from './session';
import { roleLabel, type Role } from './types';

export type NavItem = { key: string; to: string; label: string; icon: string };

const primaryNav: NavItem[] = [
  { key: 'home', to: '/home', label: '首页', icon: '🏠' },
  { key: 'projects', to: '/projects', label: '讲解项目', icon: '📊' },
  { key: 'jobs', to: '/jobs', label: '任务中心', icon: '⚙️' },
  { key: 'settings', to: '/settings/models', label: '设置', icon: '🔧' }
];

// 设置子菜单（作用域分组：个人不占本轮；租户项按方案 §12）
const settingsMenus: Array<{ key: string; to: string; label: string }> = [
  { key: 'models', to: '/settings/models', label: '模型服务' },
  { key: 'members', to: '/settings/members', label: '成员管理' },
  { key: 'dictionary', to: '/settings/dictionary', label: '发音词典' },
  { key: 'usage', to: '/settings/usage', label: '用量与存储' },
  { key: 'audit', to: '/settings/audit', label: '审计日志' }
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
  const [userMenuOpen, setUserMenuOpen] = useState(false);
  const [profileOpen, setProfileOpen] = useState(false);
  const active = activeKey(route.parts);
  const tenantShort = identity ? identity.tenantId.split('-').pop() ?? identity.tenantId : '-';

  return (
    <div className="app-shell">
      <aside className="console-sidebar">
        <Link to="/home" className="brand-block" title="智讲 PPT">
          <span className="brand-mark">智讲 PPT</span>
          <span className="brand-tenant" title={identity?.tenantId ?? ''}>
            {tenantShort}
          </span>
        </Link>
        <nav className="primary-nav" aria-label="主导航">
          {primaryNav.map((item) => (
            <Link key={item.key} to={item.to} className={`nav-item ${active === item.key ? 'selected' : ''}`}>
              <span className="nav-icon">{item.icon}</span>
              <span>{item.label}</span>
            </Link>
          ))}
        </nav>
        {active === 'settings' && (
          <nav className="secondary-nav" aria-label="设置子导航">
            {settingsMenus.map((item) => (
              <Link key={item.key} to={item.to} className={`nav-sub ${route.parts[1] === item.key ? 'selected' : ''}`}>
                {item.label}
              </Link>
            ))}
          </nav>
        )}
      </aside>
      <div className="console-main">
        <header className="console-topbar">
          <div className="breadcrumb">
            <span className="eyebrow">智讲 PPT</span>
            <span className="breadcrumb-sep">/</span>
            <span>{primaryNav.find((item) => item.key === active)?.label ?? '控制台'}</span>
          </div>
          <div className="topbar-actions">
            <Link to="/projects" className="topbar-action-btn">
              新建讲解
            </Link>
            <Link to="/settings/models" className="topbar-action-btn" title="配置 TTS / LLM 模型服务">
              模型服务
            </Link>
            <div className="user-area">
              <button type="button" className="user-trigger" aria-haspopup="menu" aria-expanded={userMenuOpen} onClick={() => setUserMenuOpen((v) => !v)}>
                <span className="user-avatar">{identity?.userId.slice(0, 1).toUpperCase() ?? '?'}</span>
                <span className="user-meta">
                  <strong>{identity?.userId ?? '-'}</strong>
                  <small>{role ? roleLabel[role] : identity?.tenantId ?? ''}</small>
                </span>
              </button>
              {userMenuOpen && (
                <div className="user-menu" role="menu">
                  <button type="button" onClick={() => { setUserMenuOpen(false); setProfileOpen(true); }}>
                    个人信息
                  </button>
                  <Link to="/settings/members" className="user-menu-link" onClick={() => setUserMenuOpen(false)}>
                    系统设置
                  </Link>
                  <button type="button" className="danger" onClick={() => { setUserMenuOpen(false); logout(); }}>
                    退出登录
                  </button>
                </div>
              )}
            </div>
          </div>
        </header>
        <main className="console-content">{children}</main>
      </div>
      {profileOpen && identity && (
        <div className="modal-backdrop" role="dialog" aria-label="个人信息">
          <section className="modal-card profile-modal">
            <header>
              <div>
                <span className="eyebrow">个人信息</span>
                <h2>{identity.userId}</h2>
              </div>
              <button type="button" onClick={() => setProfileOpen(false)}>关闭</button>
            </header>
            <dl className="profile-dl">
              <div>
                <dt>用户 ID</dt>
                <dd>{identity.userId}</dd>
              </div>
              <div>
                <dt>租户 ID</dt>
                <dd>{identity.tenantId}</dd>
              </div>
              <div>
                <dt>角色</dt>
                <dd>{role ? roleLabel[role] : '（由服务端授权决定）'}</dd>
              </div>
              <div>
                <dt>登录方式</dt>
                <dd>{identity.accessToken ? '企业账号（OIDC）' : '开发身份'}</dd>
              </div>
            </dl>
          </section>
        </div>
      )}
    </div>
  );
}