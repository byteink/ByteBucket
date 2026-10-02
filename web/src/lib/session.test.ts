import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  LEGACY_SESSION_KEY,
  UNAUTHORIZED,
  apiFetch,
  authEvents,
  fetchSession,
  login,
  logout,
  purgeLegacySession,
} from './session';

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function stubFetch(res: Response) {
  const fn = vi.fn<typeof fetch>().mockResolvedValue(res);
  vi.stubGlobal('fetch', fn);
  return fn;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('purgeLegacySession', () => {
  it('removes the old localStorage credential blob', () => {
    const removeItem = vi.fn();
    vi.stubGlobal('localStorage', { removeItem });
    purgeLegacySession();
    expect(removeItem).toHaveBeenCalledWith(LEGACY_SESSION_KEY);
    expect(LEGACY_SESSION_KEY).toBe('bytebucket_session');
  });

  it('tolerates blocked storage', () => {
    vi.stubGlobal('localStorage', {
      removeItem: () => {
        throw new Error('SecurityError');
      },
    });
    expect(() => purgeLegacySession()).not.toThrow();
  });
});

describe('login', () => {
  it('posts the credentials once as JSON and returns the session', async () => {
    const fn = stubFetch(jsonResponse(200, { accessKey: 'AK' }));
    await expect(login('AK', 'SK')).resolves.toEqual({ accessKey: 'AK' });
    const [url, init] = fn.mock.calls[0];
    expect(url).toBe('/api/login');
    expect(init?.method).toBe('POST');
    expect(init?.credentials).toBe('same-origin');
    expect(JSON.parse(String(init?.body))).toEqual({ accessKey: 'AK', secret: 'SK' });
    const headers = new Headers(init?.headers);
    expect(headers.has('X-Admin-Secret')).toBe(false);
  });

  it('surfaces the server error message on rejection', async () => {
    stubFetch(jsonResponse(401, { error: 'Invalid admin credentials' }));
    await expect(login('AK', 'bad')).rejects.toThrow('Invalid admin credentials');
  });

  it('falls back to the status line when the body is not JSON', async () => {
    stubFetch(new Response('nope', { status: 502, statusText: 'Bad Gateway' }));
    await expect(login('AK', 'SK')).rejects.toThrow('502 Bad Gateway');
  });

  it('does not broadcast unauthorized for a failed login', async () => {
    stubFetch(jsonResponse(401, { error: 'Invalid admin credentials' }));
    const seen = vi.fn();
    authEvents.addEventListener(UNAUTHORIZED, seen);
    await expect(login('AK', 'bad')).rejects.toThrow();
    authEvents.removeEventListener(UNAUTHORIZED, seen);
    expect(seen).not.toHaveBeenCalled();
  });
});

describe('fetchSession', () => {
  it('returns the session when the cookie is live', async () => {
    const fn = stubFetch(jsonResponse(200, { accessKey: 'AK' }));
    await expect(fetchSession()).resolves.toEqual({ accessKey: 'AK' });
    expect(fn.mock.calls[0][0]).toBe('/api/session');
    expect(fn.mock.calls[0][1]?.credentials).toBe('same-origin');
  });

  it('returns null when there is no session', async () => {
    stubFetch(jsonResponse(401, { error: 'Missing admin credentials' }));
    await expect(fetchSession()).resolves.toBeNull();
  });

  it('throws on other failures so the guard can show an error', async () => {
    stubFetch(jsonResponse(500, { error: 'boom' }));
    await expect(fetchSession()).rejects.toThrow('boom');
  });
});

describe('logout', () => {
  it('posts to the logout endpoint', async () => {
    const fn = stubFetch(new Response(null, { status: 204 }));
    await logout();
    expect(fn.mock.calls[0][0]).toBe('/api/logout');
    expect(fn.mock.calls[0][1]?.method).toBe('POST');
  });

  it('throws when the server refuses', async () => {
    stubFetch(jsonResponse(403, { error: 'Cross-origin request rejected' }));
    await expect(logout()).rejects.toThrow('Cross-origin request rejected');
  });
});

describe('apiFetch', () => {
  it('sends the cookie and no credential headers', async () => {
    const fn = stubFetch(jsonResponse(200, {}));
    await apiFetch('/api/users', { headers: { Accept: 'application/json' } });
    const init = fn.mock.calls[0][1];
    expect(init?.credentials).toBe('same-origin');
    const headers = new Headers(init?.headers);
    expect(headers.get('Accept')).toBe('application/json');
    expect(headers.has('X-Admin-AccessKey')).toBe(false);
    expect(headers.has('X-Admin-Secret')).toBe(false);
  });

  it('broadcasts unauthorized on 401 so the app returns to login', async () => {
    stubFetch(jsonResponse(401, { error: 'Session expired' }));
    const seen = vi.fn();
    authEvents.addEventListener(UNAUTHORIZED, seen);
    const res = await apiFetch('/api/users');
    authEvents.removeEventListener(UNAUTHORIZED, seen);
    expect(res.status).toBe(401);
    expect(seen).toHaveBeenCalledTimes(1);
  });

  it('stays quiet on other statuses', async () => {
    stubFetch(jsonResponse(403, {}));
    const seen = vi.fn();
    authEvents.addEventListener(UNAUTHORIZED, seen);
    await apiFetch('/api/users');
    authEvents.removeEventListener(UNAUTHORIZED, seen);
    expect(seen).not.toHaveBeenCalled();
  });
});
