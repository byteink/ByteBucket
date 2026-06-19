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

// BucketActivity is cumulative S3 object-operation activity (a total or per bucket).
export interface BucketActivity {
  uploads: number;
  downloads: number;
  deletes: number;
  bytesIn: number;
  bytesOut: number;
}

// RequestOutcomes is S3-surface request health: response counts by status
// class. Admin polling and SPA assets are excluded, so this reflects real
// object traffic only.
export interface RequestOutcomes {
  success: number;
  redirect: number;
  clientError: number;
  serverError: number;
}

// BucketRow is one bucket's on-disk size plus its operation counters.
export interface BucketRow {
  name: string;
  bytes: number;
  uploads: number;
  downloads: number;
  deletes: number;
}

// Stats is the admin dashboard summary returned by GET /api/stats.
export interface Stats {
  buckets: number;
  objects: number;
  bytes: number;
  multipartInProgress: number;
  activity: BucketActivity;
  requests: RequestOutcomes;
  perBucket: BucketRow[];
}

export async function getStats(s: Session): Promise<Stats> {
  const res = await fetch('/api/stats', { headers: authHeaders(s) });
  if (!res.ok) throw new Error(await parseError(res));
  return (await res.json()) as Stats;
}

// RequestBucket is one time bucket of S3 request outcomes by status class.
export interface RequestBucket {
  ts: number; // bucket start, unix seconds
  c2xx: number;
  c4xx: number;
  c5xx: number;
}

// RequestSeries is a navigable, bucketed window of request-outcome history.
export interface RequestSeries {
  range: string;
  offset: number;
  from: number;
  to: number;
  bucketSeconds: number;
  canBack: boolean;
  canForward: boolean;
  totals: { c2xx: number; c4xx: number; c5xx: number };
  buckets: RequestBucket[];
}

// The windows the dashboard chart can select between.
export type RequestRange = '1h' | '24h' | '7d' | '14d' | '30d';

export async function getRequestSeries(
  s: Session,
  range: RequestRange,
  offset: number,
): Promise<RequestSeries> {
  const res = await fetch(`/api/stats/requests?range=${range}&offset=${offset}`, {
    headers: authHeaders(s),
  });
  if (!res.ok) throw new Error(await parseError(res));
  return (await res.json()) as RequestSeries;
}

export type LogCategory = 'control' | 'data';

// LogEvent is one record in the unified event log. Control events carry
// op/target/detail; data (access) events carry op/bucket/key/status and the
// request envelope. The server omits the fields irrelevant to each category.
export interface LogEvent {
  ts: number; // unix nano; pagination cursor
  time: string; // RFC3339
  category: LogCategory;
  actor?: string;
  op: string;
  target?: string;
  bucket?: string;
  key?: string;
  status?: number;
  errorCode?: string;
  clientIp?: string;
  bytesIn?: number;
  bytesOut?: number;
  durationMs?: number;
  userAgent?: string;
  detail?: string;
}

// getLogs returns recent events of one category newest-first. Pass `before`
// (the ts of the oldest event already shown) to page into older entries.
export async function getLogs(
  s: Session,
  category: LogCategory,
  limit = 50,
  before?: number,
): Promise<LogEvent[]> {
  const params = new URLSearchParams({ category, limit: String(limit) });
  if (before) params.set('before', String(before));
  const res = await fetch(`/api/logs?${params.toString()}`, { headers: authHeaders(s) });
  if (!res.ok) throw new Error(await parseError(res));
  return ((await res.json()) as { events: LogEvent[] }).events ?? [];
}

// Admin API subpath for the data-plane access-log config (GET/PUT).
const ACCESS_LOG_PATH = '/api/config/accesslog';

// AccessLogConfig is the data-plane access-log master switch plus its retention
// caps (count and age). maxEvents/maxAgeDays of 0 disable that cap.
export interface AccessLogConfig {
  enabled: boolean;
  maxEvents: number;
  maxAgeDays: number;
}

// getAccessLog returns the effective access-log config.
export async function getAccessLog(s: Session): Promise<AccessLogConfig> {
  const res = await fetch(ACCESS_LOG_PATH, { headers: authHeaders(s) });
  if (!res.ok) throw new Error(await parseError(res));
  return (await res.json()) as AccessLogConfig;
}

// putAccessLog sets and persists the access-log config, returning the clamped value.
export async function putAccessLog(s: Session, cfg: AccessLogConfig): Promise<AccessLogConfig> {
  const res = await fetch(ACCESS_LOG_PATH, {
    method: 'PUT',
    headers: { ...authHeaders(s), 'Content-Type': 'application/json' },
    body: JSON.stringify(cfg),
  });
  if (!res.ok) throw new Error(await parseError(res));
  return (await res.json()) as AccessLogConfig;
}

// Admin API subpath for the trusted-proxy client-IP resolution config (GET/PUT).
const TRUSTED_PROXY_PATH = '/api/config/trustedproxy';

// TrustedProxyConfig is the ordered list of request headers trusted to carry the
// real client IP behind a reverse proxy, plus whether to read the leftmost
// (less safe) rather than the rightmost entry of a multi-value header. An empty
// headers list trusts no header — the socket peer is the client.
export interface TrustedProxyConfig {
  headers: string[];
  useLeftmostIP: boolean;
}

// getTrustedProxy returns the effective trusted-proxy config.
export async function getTrustedProxy(s: Session): Promise<TrustedProxyConfig> {
  const res = await fetch(TRUSTED_PROXY_PATH, { headers: authHeaders(s) });
  if (!res.ok) throw new Error(await parseError(res));
  return (await res.json()) as TrustedProxyConfig;
}

// putTrustedProxy persists the trusted-proxy config, returning the cleaned value.
export async function putTrustedProxy(s: Session, cfg: TrustedProxyConfig): Promise<TrustedProxyConfig> {
  const res = await fetch(TRUSTED_PROXY_PATH, {
    method: 'PUT',
    headers: { ...authHeaders(s), 'Content-Type': 'application/json' },
    body: JSON.stringify(cfg),
  });
  if (!res.ok) throw new Error(await parseError(res));
  return (await res.json()) as TrustedProxyConfig;
}

// WhoAmI reports how the server resolves the current request's client IP, plus
// the raw signals that fed the decision, so a trusted-proxy setup can be
// validated live from the Settings page.
export interface WhoAmI {
  ip: string;
  remoteAddr: string;
  forwardedFor: string;
  detectedHeader: string;
  trustedHeaders: string[];
  useLeftmostIP: boolean;
}

// getWhoAmI returns the resolved client IP for this very request.
export async function getWhoAmI(s: Session): Promise<WhoAmI> {
  const res = await fetch('/api/whoami', { headers: authHeaders(s) });
  if (!res.ok) throw new Error(await parseError(res));
  return (await res.json()) as WhoAmI;
}

// Admin API subpath for the request-sample retention setting (GET/PUT).
const RETENTION_PATH = '/api/config/retention';

// getRetention returns the request-sample retention window in days.
export async function getRetention(s: Session): Promise<number> {
  const res = await fetch(RETENTION_PATH, { headers: authHeaders(s) });
  if (!res.ok) throw new Error(await parseError(res));
  return ((await res.json()) as { days: number }).days;
}

// putRetention sets and persists the retention window, returning the clamped value.
export async function putRetention(s: Session, days: number): Promise<number> {
  const res = await fetch(RETENTION_PATH, {
    method: 'PUT',
    headers: { ...authHeaders(s), 'Content-Type': 'application/json' },
    body: JSON.stringify({ days }),
  });
  if (!res.ok) throw new Error(await parseError(res));
  return ((await res.json()) as { days: number }).days;
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
