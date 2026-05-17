// Thin same-origin client for the /api/s3 admin surface.
//
// All requests go to the admin port under /api/s3/*, authenticated with the
// X-Admin-* header pair. The server negotiates JSON via the Accept header,
// so handlers return the browser-friendly shape rather than S3 XML. Errors
// carry either {code,message} (admin JSON) or {error} (user handlers); we
// normalise both into a thrown Error so call sites stay simple.

import type { Session } from './session';

export type CannedACL = 'private' | 'public-read';

export interface Bucket {
  name: string;
  creationDate?: string;
  acl?: CannedACL;
}

export interface S3Object {
  key: string;
  size: number;
  lastModified?: string;
  etag?: string;
  storageClass?: string;
  acl?: CannedACL;
  aclSource?: 'object' | 'bucket' | 'default';
}

export interface BucketCORSRule {
  ID?: string;
  AllowedMethods: string[];
  AllowedOrigins: string[];
  AllowedHeaders?: string[];
  ExposeHeaders?: string[];
  MaxAgeSeconds?: number;
}

export interface BucketCORSConfig {
  CORSRules: BucketCORSRule[];
}

// Thrown when the server returns 404 for a bucket's CORS subresource so the
// UI can distinguish "no config" from a real failure without parsing strings.
export class NoSuchCORSConfiguration extends Error {
  constructor() {
    super('No CORS configuration for this bucket');
    this.name = 'NoSuchCORSConfiguration';
  }
}

function authHeaders(s: Session): HeadersInit {
  return {
    'X-Admin-AccessKey': s.accessKey,
    'X-Admin-Secret': s.secret,
    Accept: 'application/json',
  };
}

async function throwHTTP(res: Response): Promise<never> {
  let msg = `${res.status} ${res.statusText}`;
  try {
    const ct = res.headers.get('Content-Type') ?? '';
    if (ct.includes('application/json')) {
      const body = (await res.json()) as Record<string, unknown>;
      const m = body.message ?? body.error ?? body.Message;
      if (typeof m === 'string' && m.length > 0) msg = m;
    }
  } catch {
    /* keep status line */
  }
  throw new Error(msg);
}

// encPath encodes every path segment separately so slashes inside object keys
// are preserved as literal path separators in /api/s3/:bucket/*key. encodeURIComponent
// would escape them, which the router would then refuse.
function encPath(parts: string[]): string {
  return parts.map((p) => encodeURIComponent(p)).join('/');
}

export interface ServerConfig {
  publicBaseURL: string;
}

// getConfig surfaces operator-supplied runtime knobs (today: PUBLIC_BASE_URL).
// Cached on the first successful call so every page that needs the public
// origin does not refetch.
let configCache: ServerConfig | null = null;
export async function getConfig(s: Session): Promise<ServerConfig> {
  if (configCache) return configCache;
  const res = await fetch('/api/config', { headers: authHeaders(s) });
  if (!res.ok) await throwHTTP(res);
  configCache = (await res.json()) as ServerConfig;
  return configCache;
}

// ObjectMetadata is the flattened HEAD response: keys are lowercased header
// names (etag, content-type, content-length, acl, x-amz-meta-*). HEAD on the
// admin surface goes through GetObjectMetadataHandler, which surfaces every
// persisted sidecar field as a response header.
export type ObjectMetadata = Record<string, string>;
export async function getObjectMetadata(
  s: Session,
  bucket: string,
  key: string,
): Promise<ObjectMetadata> {
  const res = await fetch(
    `/api/s3/${encodeURIComponent(bucket)}/${encPath(key.split('/'))}`,
    { method: 'HEAD', headers: authHeaders(s) },
  );
  if (!res.ok) await throwHTTP(res);
  const out: ObjectMetadata = {};
  res.headers.forEach((value, name) => {
    out[name.toLowerCase()] = value;
  });
  return out;
}

export async function listBuckets(s: Session): Promise<Bucket[]> {
  const res = await fetch('/api/s3/', { headers: authHeaders(s) });
  if (!res.ok) await throwHTTP(res);
  const body = (await res.json()) as { buckets?: Bucket[] | null };
  return body.buckets ?? [];
}

export async function createBucket(s: Session, name: string): Promise<void> {
  const res = await fetch(`/api/s3/${encodeURIComponent(name)}`, {
    method: 'PUT',
    headers: authHeaders(s),
  });
  if (!res.ok) await throwHTTP(res);
}

export async function deleteBucket(s: Session, name: string): Promise<void> {
  const res = await fetch(`/api/s3/${encodeURIComponent(name)}`, {
    method: 'DELETE',
    headers: authHeaders(s),
  });
  if (!res.ok) await throwHTTP(res);
}

export async function listObjects(s: Session, bucket: string): Promise<S3Object[]> {
  const res = await fetch(`/api/s3/${encodeURIComponent(bucket)}`, {
    headers: authHeaders(s),
  });
  if (!res.ok) await throwHTTP(res);
  const body = (await res.json()) as { contents?: S3Object[] | null };
  return body.contents ?? [];
}

export async function putObject(
  s: Session,
  bucket: string,
  key: string,
  body: File | Blob,
): Promise<void> {
  // Upload the raw bytes; server persists them verbatim and records only the
  // CRC32 checksum plus Content-Type. Intentionally not streaming via
  // ReadableStream — Safari still lacks half-duplex fetch upload support.
  const res = await fetch(`/api/s3/${encodeURIComponent(bucket)}/${encPath(key.split('/'))}`, {
    method: 'PUT',
    headers: {
      ...authHeaders(s),
      'Content-Type': body.type || 'application/octet-stream',
    },
    body,
  });
  if (!res.ok) await throwHTTP(res);
}

export async function getObject(s: Session, bucket: string, key: string): Promise<Blob> {
  const res = await fetch(`/api/s3/${encodeURIComponent(bucket)}/${encPath(key.split('/'))}`, {
    headers: {
      'X-Admin-AccessKey': s.accessKey,
      'X-Admin-Secret': s.secret,
    },
  });
  if (!res.ok) await throwHTTP(res);
  return await res.blob();
}

export async function deleteObject(s: Session, bucket: string, key: string): Promise<void> {
  const res = await fetch(`/api/s3/${encodeURIComponent(bucket)}/${encPath(key.split('/'))}`, {
    method: 'DELETE',
    headers: authHeaders(s),
  });
  if (!res.ok) await throwHTTP(res);
}

export async function headObject(s: Session, bucket: string, key: string): Promise<boolean> {
  const res = await fetch(`/api/s3/${encodeURIComponent(bucket)}/${encPath(key.split('/'))}`, {
    method: 'HEAD',
    headers: {
      'X-Admin-AccessKey': s.accessKey,
      'X-Admin-Secret': s.secret,
    },
  });
  if (res.status === 404) return false;
  if (!res.ok) await throwHTTP(res);
  return true;
}

export async function getBucketCORS(s: Session, bucket: string): Promise<BucketCORSConfig> {
  const res = await fetch(`/api/s3/${encodeURIComponent(bucket)}?cors`, {
    headers: authHeaders(s),
  });
  if (res.status === 404) throw new NoSuchCORSConfiguration();
  if (!res.ok) await throwHTTP(res);
  return (await res.json()) as BucketCORSConfig;
}

export async function putBucketCORS(
  s: Session,
  bucket: string,
  cfg: BucketCORSConfig,
): Promise<void> {
  const res = await fetch(`/api/s3/${encodeURIComponent(bucket)}?cors`, {
    method: 'PUT',
    headers: { ...authHeaders(s), 'Content-Type': 'application/json' },
    body: JSON.stringify(cfg),
  });
  if (!res.ok) await throwHTTP(res);
}

export async function putBucketACL(
  s: Session,
  bucket: string,
  canned: CannedACL,
): Promise<void> {
  const res = await fetch(`/api/s3/${encodeURIComponent(bucket)}?acl`, {
    method: 'PUT',
    headers: { ...authHeaders(s), 'Content-Type': 'application/json' },
    body: JSON.stringify({ canned }),
  });
  if (!res.ok) await throwHTTP(res);
}

export async function getBucketACL(s: Session, bucket: string): Promise<CannedACL> {
  const res = await fetch(`/api/s3/${encodeURIComponent(bucket)}?acl`, {
    headers: authHeaders(s),
  });
  if (!res.ok) await throwHTTP(res);
  const body = (await res.json()) as { canned?: CannedACL };
  return body.canned ?? 'private';
}

export interface PresignedURL {
  url: string;
  expiresIn: number;
  expiresAt: string;
}

// presignObject asks the server to mint a SigV4 GetObject URL valid for the
// requested number of seconds. Server-side signing keeps the user's secret
// out of the browser's signing path — the admin login already trusted us
// with it, so this just centralises the canonical-request bookkeeping.
export async function presignObject(
  s: Session,
  bucket: string,
  key: string,
  expiresSeconds: number,
): Promise<PresignedURL> {
  const res = await fetch(
    `/api/s3/${encodeURIComponent(bucket)}/${encPath(key.split('/'))}?presign&expires=${expiresSeconds}`,
    { headers: authHeaders(s) },
  );
  if (!res.ok) await throwHTTP(res);
  return (await res.json()) as PresignedURL;
}

export async function putObjectACL(
  s: Session,
  bucket: string,
  key: string,
  canned: CannedACL,
): Promise<void> {
  const res = await fetch(
    `/api/s3/${encodeURIComponent(bucket)}/${encPath(key.split('/'))}?acl`,
    {
      method: 'PUT',
      headers: { ...authHeaders(s), 'Content-Type': 'application/json' },
      body: JSON.stringify({ canned }),
    },
  );
  if (!res.ok) await throwHTTP(res);
}

export async function deleteBucketCORS(s: Session, bucket: string): Promise<void> {
  const res = await fetch(`/api/s3/${encodeURIComponent(bucket)}?cors`, {
    method: 'DELETE',
    headers: authHeaders(s),
  });
  if (res.status === 404) throw new NoSuchCORSConfiguration();
  if (!res.ok) await throwHTTP(res);
}
