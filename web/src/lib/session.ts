// Session lives entirely in localStorage; there are no cookies or server sessions.
// Keep this file as the only place that knows the storage key or shape.
//
// After the same-origin refactor the session only carries admin credentials.
// All storage traffic goes through /s3/* on the admin port using the same
// headers, so the browser no longer needs a separate storage endpoint.

const STORAGE_KEY = 'bytebucket_session';

export interface Session {
  accessKey: string;
  secret: string;
}

export function loadSession(): Session | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<Session>;
    if (!parsed.accessKey || !parsed.secret) {
      return null;
    }
    return { accessKey: parsed.accessKey, secret: parsed.secret };
  } catch {
    return null;
  }
}

export function saveSession(s: Session): void {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(s));
}

export function clearSession(): void {
  localStorage.removeItem(STORAGE_KEY);
}
