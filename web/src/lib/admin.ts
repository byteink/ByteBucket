// Thin wrapper around fetch for admin API calls.
// Every call is same-origin and relative; auth is carried in headers only.

import type { Session } from './session';

export interface ACLRule {
  effect: string;
  buckets: string[];
  actions: string[];
}

export interface User {
  accessKeyID: string;
  acl: ACLRule[] | null;
  // RFC3339 timestamp set when the user was first persisted. Users created
  // before this field existed return the Go zero value (year 0001), which
  // the UI renders as unknown.
  createdAt?: string;
}

export interface CreatedUser extends User {
  secretAccessKey: string;
}

// Admin API subpath for the runtime rate-limit override (GET/PUT/DELETE).
const RATE_LIMIT_PATH = '/api/config/ratelimit';

// RateLimitConfig mirrors the server's wire shape (internal/handlers/ratelimit).
export interface RateLimitConfig {
  enabled: boolean;
  rps: number;
  burst: number;
  trustedProxies: number;
}

// RateLimitState carries the environment baseline, the persisted override
// (null when none), and the effective config currently enforced.
export interface RateLimitState {
  env: RateLimitConfig;
  override: RateLimitConfig | null;
  effective: RateLimitConfig;
}

function authHeaders(s: Session): HeadersInit {
  return {
    'X-Admin-AccessKey': s.accessKey,
    'X-Admin-Secret': s.secret,
  };
}

async function parseError(res: Response): Promise<string> {
  try {
    const body = await res.json();
    if (body && typeof body === 'object' && 'error' in body) {
      return String((body as Record<string, unknown>).error);
    }
  } catch {
    /* ignore */
  }
  return `${res.status} ${res.statusText}`;
}

export async function listUsers(s: Session): Promise<User[]> {
  const res = await fetch('/api/users', { headers: authHeaders(s) });
  if (!res.ok) throw new Error(await parseError(res));
  const data = (await res.json()) as User[] | null;
  return data ?? [];
}

export async function createUser(s: Session, acl: ACLRule[]): Promise<CreatedUser> {
  const res = await fetch('/api/users', {
    method: 'POST',
    headers: { ...authHeaders(s), 'Content-Type': 'application/json' },
    body: JSON.stringify({ acl }),
  });
  if (!res.ok) throw new Error(await parseError(res));
  return (await res.json()) as CreatedUser;
}

export async function updateUserACL(s: Session, accessKeyID: string, acl: ACLRule[]): Promise<void> {
  const res = await fetch(`/api/users/${encodeURIComponent(accessKeyID)}`, {
    method: 'PUT',
    headers: { ...authHeaders(s), 'Content-Type': 'application/json' },
    body: JSON.stringify({ acl }),
  });
  if (!res.ok) throw new Error(await parseError(res));
}

export async function deleteUser(s: Session, accessKeyID: string): Promise<void> {
  const res = await fetch(`/api/users/${encodeURIComponent(accessKeyID)}`, {
    method: 'DELETE',
    headers: authHeaders(s),
  });
  if (!res.ok) throw new Error(await parseError(res));
}

export async function getRateLimit(s: Session): Promise<RateLimitState> {
  const res = await fetch(RATE_LIMIT_PATH, { headers: authHeaders(s) });
  if (!res.ok) throw new Error(await parseError(res));
  return (await res.json()) as RateLimitState;
}

// putRateLimit persists a runtime override and returns the now-effective config.
export async function putRateLimit(s: Session, cfg: RateLimitConfig): Promise<RateLimitConfig> {
  const res = await fetch(RATE_LIMIT_PATH, {
    method: 'PUT',
    headers: { ...authHeaders(s), 'Content-Type': 'application/json' },
    body: JSON.stringify(cfg),
  });
  if (!res.ok) throw new Error(await parseError(res));
  return ((await res.json()) as { effective: RateLimitConfig }).effective;
}

// deleteRateLimit clears the override, reverting to the environment baseline.
export async function deleteRateLimit(s: Session): Promise<RateLimitConfig> {
  const res = await fetch(RATE_LIMIT_PATH, { method: 'DELETE', headers: authHeaders(s) });
  if (!res.ok) throw new Error(await parseError(res));
  return ((await res.json()) as { effective: RateLimitConfig }).effective;
}

// Admin API subpath for the object-write durability (fsync) toggle (GET/PUT).
const SYNC_WRITES_PATH = '/api/config/sync';

// getSyncWrites returns the effective object-write durability setting.
export async function getSyncWrites(s: Session): Promise<boolean> {
  const res = await fetch(SYNC_WRITES_PATH, { headers: authHeaders(s) });
  if (!res.ok) throw new Error(await parseError(res));
  return ((await res.json()) as { enabled: boolean }).enabled;
}

// putSyncWrites sets and persists the durability setting, returning the new value.
export async function putSyncWrites(s: Session, enabled: boolean): Promise<boolean> {
  const res = await fetch(SYNC_WRITES_PATH, {
    method: 'PUT',
    headers: { ...authHeaders(s), 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled }),
  });
  if (!res.ok) throw new Error(await parseError(res));
  return ((await res.json()) as { enabled: boolean }).enabled;
}

// checkAdminAuth returns null when the current session is accepted by the admin
// API, or a string describing the rejection.
export async function checkAdminAuth(s: Session): Promise<string | null> {
  try {
    const res = await fetch('/api/users', { headers: authHeaders(s) });
    if (res.status === 401 || res.status === 403) {
      return 'Invalid admin credentials';
    }
    if (!res.ok) return await parseError(res);
    return null;
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    return `Cannot reach admin API: ${msg}`;
  }
}
