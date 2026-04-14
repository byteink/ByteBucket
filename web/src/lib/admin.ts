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
  const res = await fetch('/users', { headers: authHeaders(s) });
  if (!res.ok) throw new Error(await parseError(res));
  const data = (await res.json()) as User[] | null;
  return data ?? [];
}

export async function createUser(s: Session, acl: ACLRule[]): Promise<CreatedUser> {
  const res = await fetch('/users', {
    method: 'POST',
    headers: { ...authHeaders(s), 'Content-Type': 'application/json' },
    body: JSON.stringify({ acl }),
  });
  if (!res.ok) throw new Error(await parseError(res));
  return (await res.json()) as CreatedUser;
}

export async function updateUserACL(s: Session, accessKeyID: string, acl: ACLRule[]): Promise<void> {
  const res = await fetch(`/users/${encodeURIComponent(accessKeyID)}`, {
    method: 'PUT',
    headers: { ...authHeaders(s), 'Content-Type': 'application/json' },
    body: JSON.stringify({ acl }),
  });
  if (!res.ok) throw new Error(await parseError(res));
}

export async function deleteUser(s: Session, accessKeyID: string): Promise<void> {
  const res = await fetch(`/users/${encodeURIComponent(accessKeyID)}`, {
    method: 'DELETE',
    headers: authHeaders(s),
  });
  if (!res.ok) throw new Error(await parseError(res));
}

// checkAdminAuth returns null when the current session is accepted by the admin
// API, or a string describing the rejection.
export async function checkAdminAuth(s: Session): Promise<string | null> {
  try {
    const res = await fetch('/users', { headers: authHeaders(s) });
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
