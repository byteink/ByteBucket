import { useEffect, useState, type ReactNode } from 'react';
import { Navigate, useLocation } from 'react-router-dom';
import { UNAUTHORIZED, authEvents, fetchSession, type Session } from '../lib/session';
import { SessionContext } from '../lib/sessionContext';
import { ErrorBanner } from './ErrorBanner';

type GuardState =
  | { kind: 'checking' }
  | { kind: 'anonymous' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; session: Session };

// AuthGuard asks the server whether the session cookie is live before it
// renders the app, and drops back to login whenever any API call reports 401
// (idle or absolute expiry, logout elsewhere, or a revoked admin).
export default function AuthGuard({ children }: { children: ReactNode }) {
  const location = useLocation();
  const [state, setState] = useState<GuardState>({ kind: 'checking' });

  useEffect(() => {
    let live = true;
    fetchSession()
      .then((s) => live && setState(s ? { kind: 'ready', session: s } : { kind: 'anonymous' }))
      .catch((e: unknown) => live && setState({ kind: 'error', message: e instanceof Error ? e.message : String(e) }));
    return () => {
      live = false;
    };
  }, []);

  useEffect(() => {
    const onUnauthorized = () => setState({ kind: 'anonymous' });
    authEvents.addEventListener(UNAUTHORIZED, onUnauthorized);
    return () => authEvents.removeEventListener(UNAUTHORIZED, onUnauthorized);
  }, []);

  switch (state.kind) {
    case 'checking':
      return null;
    case 'anonymous':
      return <Navigate to="/login" replace state={{ from: location.pathname }} />;
    case 'error':
      return (
        <div className="min-h-full flex items-center justify-center px-6">
          <ErrorBanner message={`Cannot reach admin API: ${state.message}`} />
        </div>
      );
    case 'ready':
      return <SessionContext.Provider value={state.session}>{children}</SessionContext.Provider>;
  }
}
