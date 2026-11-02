import { createContext, useContext } from 'react';
import type { ClientIdentity } from './api';
import type { LocalIdentity } from './localAuth';

export type Session = {
  identity: ClientIdentity | null;
  // local：本实例是单租户本地模式（无账号体系）。此时没有可完成的登出，
  // 界面不应提供「退出登录」，登录页则提供「进入本机实例」。
  local: boolean;
  loginDev: (identity: ClientIdentity) => void;
  loginLocal: (identity: LocalIdentity) => void;
  loginOIDC: () => void;
  loginEmail: (identity: ClientIdentity) => void;
  logout: () => void;
};

export const SessionContext = createContext<Session | null>(null);

export function useSession(): Session {
  const session = useContext(SessionContext);
  if (!session) {
    throw new Error('useSession must be used within SessionContext');
  }
  return session;
}
