import { createContext, useContext } from 'react';
import type { Session } from './session';

// SessionContext carries the verified session from AuthGuard to the pages, so
// no page re-checks auth or reads it from storage.
export const SessionContext = createContext<Session | null>(null);

export function useSession(): Session {
  const s = useContext(SessionContext);
  if (!s) throw new Error('useSession must be used inside AuthGuard');
  return s;
}
