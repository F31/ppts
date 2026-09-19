import React from 'react';

type ErrorBoundaryProps = {
  children: React.ReactNode;
};

type ErrorBoundaryState = {
  error: Error | null;
  info: React.ErrorInfo | null;
};

// 自包含文案：崩溃兜底不依赖 i18n/主题上下文（它们自身也可能已崩溃）。
const COPY: Record<'zh' | 'en', {
  title: string;
  desc: string;
  retry: string;
  reload: string;
  details: string;
}> = {
  zh: {
    title: '页面渲染出错',
    desc: '页面遇到未预期的错误，无法继续显示。你可以尝试重试恢复，或重新加载页面。',
    retry: '重试',
    reload: '重新加载页面',
    details: '错误详情（供排查）',
  },
  en: {
    title: 'Something went wrong',
    desc: 'The page hit an unexpected error and cannot continue. You can retry to recover, or reload the page.',
    retry: 'Retry',
    reload: 'Reload page',
    details: 'Error details (for debugging)',
  },
};

function isDark(): boolean {
  if (typeof window !== 'undefined' && window.matchMedia) {
    return window.matchMedia('(prefers-color-scheme: dark)').matches;
  }
  return false;
}

// 顶层错误边界：捕获任意子树渲染期异常，呈现「原因 + 重试」而非空白页（A26）。
export class ErrorBoundary extends React.Component<ErrorBoundaryProps, ErrorBoundaryState> {
  constructor(props: ErrorBoundaryProps) {
    super(props);
    this.state = { error: null, info: null };
  }

  static getDerivedStateFromError(error: Error): Partial<ErrorBoundaryState> {
    return { error };
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    // 仅记录到控制台供排查，不向用户暴露敏感信息。
    // eslint-disable-next-line no-console
    console.error('[ErrorBoundary]', error, info);
    this.setState({ info });
  }

  handleRetry = () => {
    this.setState({ error: null, info: null });
  };

  handleReload = () => {
    window.location.reload();
  };

  render() {
    const { error, info } = this.state;
    if (!error) return this.props.children;

    const lang: 'zh' | 'en' =
      typeof navigator !== 'undefined' && navigator.language?.toLowerCase().startsWith('zh') ? 'zh' : 'en';
    const c = COPY[lang];
    const dark = isDark();
    const bg = dark ? '#15171c' : '#f5f6f8';
    const surface = dark ? '#1e2128' : '#ffffff';
    const text = dark ? '#e6e8ec' : '#1f2329';
    const sub = dark ? '#9aa3af' : '#5b6573';
    const danger = dark ? '#ff6b6b' : '#d93025';
    const border = dark ? '#2c303a' : '#e3e6ea';
    const btnBg = dark ? '#2d6cdf' : '#2563eb';

    const stack = [error.message, info?.componentStack].filter(Boolean).join('\n\n');

    return (
      <div
        role="alert"
        style={{
          minHeight: '100vh',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          background: bg,
          color: text,
          padding: '24px',
          boxSizing: 'border-box',
          fontFamily:
            'system-ui, -apple-system, "Segoe UI", Roboto, "PingFang SC", "Microsoft YaHei", sans-serif',
        }}
      >
        <div
          style={{
            maxWidth: '560px',
            width: '100%',
            background: surface,
            border: `1px solid ${border}`,
            borderRadius: '12px',
            padding: '28px',
            boxShadow: '0 8px 28px rgba(0,0,0,0.12)',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginBottom: '12px' }}>
            <span style={{ color: danger, fontSize: '22px', fontWeight: 700 }}>!</span>
            <h1 style={{ fontSize: '18px', margin: 0, fontWeight: 600 }}>{c.title}</h1>
          </div>
          <p style={{ color: sub, fontSize: '14px', lineHeight: 1.6, margin: '0 0 20px' }}>{c.desc}</p>
          <div style={{ display: 'flex', gap: '10px', flexWrap: 'wrap' }}>
            <button
              type="button"
              onClick={this.handleRetry}
              style={{
                background: btnBg,
                color: '#ffffff',
                border: 'none',
                borderRadius: '8px',
                padding: '10px 18px',
                fontSize: '14px',
                fontWeight: 600,
                cursor: 'pointer',
              }}
            >
              {c.retry}
            </button>
            <button
              type="button"
              onClick={this.handleReload}
              style={{
                background: 'transparent',
                color: text,
                border: `1px solid ${border}`,
                borderRadius: '8px',
                padding: '10px 18px',
                fontSize: '14px',
                cursor: 'pointer',
              }}
            >
              {c.reload}
            </button>
          </div>
          {stack && (
            <details style={{ marginTop: '18px' }}>
              <summary style={{ cursor: 'pointer', color: sub, fontSize: '13px' }}>{c.details}</summary>
              <pre
                style={{
                  marginTop: '10px',
                  background: dark ? '#0f1115' : '#f0f2f5',
                  color: sub,
                  fontSize: '12px',
                  lineHeight: 1.5,
                  padding: '12px',
                  borderRadius: '8px',
                  overflow: 'auto',
                  whiteSpace: 'pre-wrap',
                  wordBreak: 'break-word',
                }}
              >
                {stack}
              </pre>
            </details>
          )}
        </div>
      </div>
    );
  }
}
