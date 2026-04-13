// Session lives entirely in localStorage; there are no cookies or server sessions.
// Keep this file as the only place that knows the storage key or shape.

const STORAGE_KEY = 'bytebucket_session';

export interface Session {
  accessKey: string;
  secret: string;
  storageEndpoint: string;
}

export function loadSession(): Session | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<Session>;
    if (!parsed.accessKey || !parsed.secret || !parsed.storageEndpoint) {
      return null;
    }
    return parsed as Session;
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

export function defaultStorageEndpoint(): string {
  const { protocol, hostname } = window.location;
  return `${protocol}//${hostname}:9000`;
}
