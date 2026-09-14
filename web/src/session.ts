import { createContext, useContext } from 'react';
import type { ClientIdentity } from './api';

export type Session = {
  identity: ClientIdentity | null;
  loginDev: (identity: ClientIdentity) => void;
  loginOIDC: () => void;
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