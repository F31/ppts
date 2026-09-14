import { useEffect, useState, type ReactNode } from 'react';

export type Route = {
  path: string;
  parts: string[];
  query: URLSearchParams;
};

function parseHash(): Route {
  let raw = window.location.hash.replace(/^#/, '');
  if (!raw) raw = '/';
  const [pathPart, queryPart] = raw.split('?');
  const path = pathPart.startsWith('/') ? pathPart : `/${pathPart}`;
  const parts = path.split('/').filter(Boolean);
  return { path, parts, query: new URLSearchParams(queryPart ?? '') };
}

export function navigate(to: string) {
  const target = to.startsWith('/') ? to : `/${to}`;
  if (window.location.hash === `#${target}`) return;
  window.location.hash = target;
}

export function useRoute(): Route {
  const [route, setRoute] = useState<Route>(parseHash);
  useEffect(() => {
    const onChange = () => setRoute(parseHash());
    window.addEventListener('hashchange', onChange);
    return () => window.removeEventListener('hashchange', onChange);
  }, []);
  return route;
}

export function Link({ to, className, children, title, onClick }: { to: string; className?: string; children: ReactNode; title?: string; onClick?: () => void }) {
  return (
    <a href={`#${to}`} className={className} title={title} onClick={onClick}>
      {children}
    </a>
  );
}
