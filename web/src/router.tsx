import { useEffect, useState, type ReactNode } from 'react';

export type Route = {
  path: string;
  parts: string[];
  query: URLSearchParams;
};

// 解析当前浏览器地址（History 模式：以 pathname + search 为准，不再依赖 location.hash）。
function parseRoute(): Route {
  let raw = window.location.pathname;
  if (!raw) raw = '/';
  const [pathPart, queryPart] = raw.split('?');
  const path = pathPart.startsWith('/') ? pathPart : `/${pathPart}`;
  const parts = path.split('/').filter(Boolean);
  return { path, parts, query: new URLSearchParams(queryPart ?? window.location.search) };
}

// 将遗留的 hash 深链接（#/home、#/projects/123/editor）改写为 history 路径，
// 避免切换路由模式后旧书签/外链失效。仅在应用挂载前调用一次。
export function migrateLegacyHash() {
  const h = window.location.hash;
  if (h.startsWith('#/')) {
    const newPath = h.slice(1) + window.location.search;
    window.history.replaceState(null, '', newPath);
  }
}

// 编程式跳转：使用 History API 推入新路径，并派发 popstate 让 useRoute 重新解析。
export function navigate(to: string) {
  const target = to.startsWith('/') ? to : `/${to}`;
  if (window.location.pathname + window.location.search === target) return;
  window.history.pushState({}, '', target);
  // pushState 不会触发 popstate，手动派发以确保路由状态同步。
  window.dispatchEvent(new PopStateEvent('popstate'));
}

export function useRoute(): Route {
  const [route, setRoute] = useState<Route>(parseRoute);
  useEffect(() => {
    const onChange = () => setRoute(parseRoute());
    window.addEventListener('popstate', onChange);
    return () => window.removeEventListener('popstate', onChange);
  }, []);
  return route;
}

// History 模式下的链接：href 写入真实路径，支持新标签打开与深链分享；
// 点击时阻止默认跳转并走 navigate（避免整页刷新），随后执行可选的 onClick。
export function Link({
  to,
  className,
  children,
  title,
  onClick,
}: {
  to: string;
  className?: string;
  children: ReactNode;
  title?: string;
  onClick?: () => void;
}) {
  const target = to.startsWith('/') ? to : `/${to}`;
  return (
    <a
      href={target}
      className={className}
      title={title}
      onClick={(e) => {
        if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
        e.preventDefault();
        onClick?.();
        navigate(to);
      }}
    >
      {children}
    </a>
  );
}
