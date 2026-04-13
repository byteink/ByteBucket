import { Navigate, useLocation } from 'react-router-dom';
import { loadSession } from '../lib/session';
import type { ReactNode } from 'react';

export default function AuthGuard({ children }: { children: ReactNode }) {
  const location = useLocation();
  const session = loadSession();
  if (!session) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }
  return <>{children}</>;
}
