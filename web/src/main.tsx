import React from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import { I18nProvider } from './i18n';
import { ThemeProvider } from './ThemeContext';
import { ErrorBoundary } from './components/ErrorBoundary';
import { migrateLegacyHash } from './router';
import './styles.css';
import './theme.css';
import './public.css';

// 路由模式迁移：将遗留 #/path 深链接改写为 /path，须在 React 挂载前执行。
migrateLegacyHash();

createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ErrorBoundary>
      <ThemeProvider>
        <I18nProvider>
          <App />
        </I18nProvider>
      </ThemeProvider>
    </ErrorBoundary>
  </React.StrictMode>
);
