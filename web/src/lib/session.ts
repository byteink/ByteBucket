// The admin session lives in an HttpOnly cookie the server sets at login; the
// browser never stores the secret. This file is the only place that knows the
// session endpoints and the 401 contract.

// Key under which older builds persisted {accessKey, secret} in localStorage.
// Kept only so purgeLegacySession can delete it from browsers that still
// carry it.
export const LEGACY_SESSION_KEY = 'bytebucket_session';

export interface Session {
  accessKey: string;
}

// authEvents carries UNAUTHORIZED whenever an API call comes back 401, so the
// auth guard can route back to login from one place instead of every page.
export const authEvents = new EventTarget();
export const UNAUTHORIZED = 'unauthorized';

export function purgeLegacySession(): void {
  try {
    globalThis.localStorage.removeItem(LEGACY_SESSION_KEY);
  } catch {
    // Storage blocked (private mode): nothing persisted, nothing to purge.
  }
}

async function errorMessage(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as Record<string, unknown>;
    const m = body.error ?? body.message;
    if (typeof m === 'string' && m.length > 0) return m;
  } catch {
    /* keep status line */
  }
  return `${res.status} ${res.statusText}`;
}

// apiFetch is fetch for authenticated admin API calls: the cookie rides along
// and a 401 (expired or revoked session) is broadcast before the caller sees it.
export async function apiFetch(input: string, init: RequestInit = {}): Promise<Response> {
  const res = await fetch(input, { ...init, credentials: 'same-origin' });
  if (res.status === 401) authEvents.dispatchEvent(new Event(UNAUTHORIZED));
  return res;
}

// login sends the credentials exactly once; the server answers with the
// session cookie. Uses plain fetch because a 401 here is a form error, not an
// expired session.
export async function login(accessKey: string, secret: string): Promise<Session> {
  const res = await fetch('/api/login', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ accessKey, secret }),
  });
  if (!res.ok) throw new Error(await errorMessage(res));
  return (await res.json()) as Session;
}

// fetchSession reports the live session, or null when the cookie is missing,
// expired or revoked.
export async function fetchSession(): Promise<Session | null> {
  const res = await fetch('/api/session', { credentials: 'same-origin' });
  if (res.status === 401) return null;
  if (!res.ok) throw new Error(await errorMessage(res));
  return (await res.json()) as Session;
}

export async function logout(): Promise<void> {
  const res = await fetch('/api/logout', { method: 'POST', credentials: 'same-origin' });
  if (!res.ok) throw new Error(await errorMessage(res));
}
