import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode
} from 'react';

export type Lang = 'zh' | 'en';
type Messages = Record<string, string>;

// 语言资源按需加载：仅在切换到对应语言时才动态 import 该语言的 JSON。
const loaders: Record<Lang, () => Promise<{ default: Messages }>> = {
  zh: () => import('./zh.json'),
  en: () => import('./en.json')
};

const STORAGE_KEY = 'ppts.lang';

type I18nValue = {
  lang: Lang;
  ready: boolean;
  setLang: (lang: Lang) => void;
  toggle: () => void;
  t: (key: string, vars?: Record<string, string | number>) => string;
};

const I18nContext = createContext<I18nValue | null>(null);

function initialLang(): Lang {
  if (typeof localStorage !== 'undefined') {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored === 'zh' || stored === 'en') return stored;
  }
  const nav = typeof navigator !== 'undefined' ? navigator.language : '';
  return nav.toLowerCase().startsWith('zh') ? 'zh' : 'en';
}

export function I18nProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(initialLang);
  const [messages, setMessages] = useState<Messages | null>(null);

  useEffect(() => {
    let cancelled = false;
    loaders[lang]().then((mod) => {
      if (!cancelled) setMessages(mod.default);
    });
    return () => {
      cancelled = true;
    };
  }, [lang]);

  useEffect(() => {
    if (typeof document !== 'undefined') {
      document.documentElement.lang = lang === 'zh' ? 'zh-CN' : 'en';
    }
    if (typeof localStorage !== 'undefined') localStorage.setItem(STORAGE_KEY, lang);
  }, [lang]);

  const setLang = useCallback((next: Lang) => setLangState(next), []);

  const t = useCallback(
    (key: string, vars?: Record<string, string | number>) => {
      let text = messages?.[key] ?? key;
      if (vars) {
        for (const [name, value] of Object.entries(vars)) {
          text = text.split(`{${name}}`).join(String(value));
        }
      }
      return text;
    },
    [messages]
  );

  const value = useMemo<I18nValue>(
    () => ({
      lang,
      ready: messages !== null,
      setLang,
      toggle: () => setLangState((current) => (current === 'zh' ? 'en' : 'zh')),
      t
    }),
    [lang, messages, setLang, t]
  );

  // 资源加载完成前不渲染，避免闪现 key（JSON 很小，通常 <50ms）。
  if (messages === null) {
    return null;
  }
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nValue {
  const ctx = useContext(I18nContext);
  if (!ctx) throw new Error('useI18n must be used within I18nProvider');
  return ctx;
}
