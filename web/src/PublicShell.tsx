import { Link, useRoute } from './router';
import { useSession } from './session';
import { useI18n } from './i18n';
import { useTheme } from './ThemeContext';

// PublicShell 是公开区外壳：未登录可见，仅含顶栏（品牌/主题/语言/登录入口）与内容区，
// 不渲染控制台侧边栏。已登录时"进入控制台"跳转 /home，未登录时"登录"跳转 /login。
export function PublicShell({ children }: { children: React.ReactNode }) {
  const { identity } = useSession();
  const { t, lang, toggle } = useI18n();
  const { theme, toggle: toggleTheme } = useTheme();
  const route = useRoute();
  const onExplore = route.parts[0] === 'explore' || route.parts[0] === '' || route.parts[0] === 'watch';

  return (
    <div className="public-shell">
      <header className="public-topbar">
        <Link to="/explore" className="public-brand">
          {t('public.brand')}
        </Link>
        <nav className="public-nav" aria-label={t('public.explore')}>
          <Link to="/explore" className={`public-nav-link ${onExplore ? 'selected' : ''}`}>
            {t('public.explore')}
          </Link>
        </nav>
        <div className="topbar-actions">
          <button
            type="button"
            className="theme-toggle"
            onClick={toggleTheme}
            title={t('shell.themeToggle')}
            aria-label={t('shell.themeToggle')}
          >
            {theme === 'dark' ? '☀️' : '🌙'}
          </button>
          <button type="button" className="lang-toggle" onClick={toggle} title={t('shell.languageToggle')}>
            {lang === 'zh' ? t('lang.en') : t('lang.zh')}
          </button>
          {identity ? (
            <Link to="/home" className="btn btn-primary">
              {t('public.console')}
            </Link>
          ) : (
            <Link to="/login" className="btn btn-primary">
              {t('public.login')}
            </Link>
          )}
        </div>
      </header>
      <main className="public-content">{children}</main>
      <footer className="public-footer">{t('public.footer')}</footer>
    </div>
  );
}
