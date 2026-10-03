import { afterEach, describe, expect, it, vi } from 'vitest';
import { deleteIPBan, getIPBan, putIPBan, type IPBanConfig } from './admin';
import { IP_BAN_LIMITS, ipBanError } from './ipban';

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

const valid: IPBanConfig = { enabled: true, maxFailures: 20, windowSeconds: 60, banSeconds: 900 };

describe('ipBanError', () => {
  it('accepts the defaults and the bounds', () => {
    expect(ipBanError(valid)).toBeNull();
    expect(ipBanError({ enabled: false, maxFailures: 1, windowSeconds: 1, banSeconds: 1 })).toBeNull();
    expect(
      ipBanError({
        enabled: true,
        maxFailures: IP_BAN_LIMITS.maxFailures,
        windowSeconds: IP_BAN_LIMITS.windowSeconds,
        banSeconds: IP_BAN_LIMITS.banSeconds,
      }),
    ).toBeNull();
  });

  it('matches the server bounds', () => {
    expect(IP_BAN_LIMITS).toEqual({ maxFailures: 10000, windowSeconds: 3600, banSeconds: 604800 });
  });

  it.each([
    [{ maxFailures: 0 }, 'Max failures must be a whole number from 1 to 10000.'],
    [{ maxFailures: 10001 }, 'Max failures must be a whole number from 1 to 10000.'],
    [{ maxFailures: 2.5 }, 'Max failures must be a whole number from 1 to 10000.'],
    [{ windowSeconds: 0 }, 'Window must be a whole number of seconds from 1 to 3600.'],
    [{ windowSeconds: 3601 }, 'Window must be a whole number of seconds from 1 to 3600.'],
    [{ banSeconds: 0 }, 'Ban duration must be a whole number of seconds from 1 to 604800.'],
    [{ banSeconds: 604801 }, 'Ban duration must be a whole number of seconds from 1 to 604800.'],
    [{ banSeconds: Number.NaN }, 'Ban duration must be a whole number of seconds from 1 to 604800.'],
  ])('rejects %o', (patch, msg) => {
    expect(ipBanError({ ...valid, ...patch })).toBe(msg);
  });

  it('validates even when disabled, as the server does', () => {
    expect(ipBanError({ ...valid, enabled: false, maxFailures: 0 })).not.toBeNull();
  });
});

describe('ip ban client', () => {
  it('reads env, override and effective', async () => {
    const state = { env: valid, override: null, effective: valid };
    const fn = stubFetch(jsonResponse(200, state));
    await expect(getIPBan()).resolves.toEqual(state);
    expect(fn.mock.calls[0][0]).toBe('/api/config/ipban');
  });

  it('puts the full config as JSON and returns the effective one', async () => {
    const fn = stubFetch(jsonResponse(200, { effective: valid }));
    await expect(putIPBan(valid)).resolves.toEqual(valid);
    const [url, init] = fn.mock.calls[0];
    expect(url).toBe('/api/config/ipban');
    expect(init?.method).toBe('PUT');
    expect(new Headers(init?.headers).get('Content-Type')).toBe('application/json');
    expect(JSON.parse(String(init?.body))).toEqual(valid);
  });

  it('deletes the override and returns the effective config', async () => {
    const fn = stubFetch(jsonResponse(200, { effective: valid }));
    await expect(deleteIPBan()).resolves.toEqual(valid);
    expect(fn.mock.calls[0][1]?.method).toBe('DELETE');
  });

  it.each([
    ['get', () => getIPBan()],
    ['put', () => putIPBan(valid)],
    ['delete', () => deleteIPBan()],
  ])('surfaces the server error on %s', async (_name, call) => {
    stubFetch(jsonResponse(400, { error: 'maxFailures must be between 1 and 10000' }));
    await expect(call()).rejects.toThrow('maxFailures must be between 1 and 10000');
  });
});
