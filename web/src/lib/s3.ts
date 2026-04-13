// Thin same-origin client for the /s3 admin surface.
//
// All requests go to the admin port under /s3/*, authenticated with the
// X-Admin-* header pair. The server negotiates JSON via the Accept header,
// so handlers return the browser-friendly shape rather than S3 XML. Errors
// carry either {code,message} (admin JSON) or {error} (user handlers); we
// normalise both into a thrown Error so call sites stay simple.

import type { Session } from './session';

export interface Bucket {
  name: string;
  creationDate?: string;
}

export interface S3Object {
  key: string;
  size: number;
  lastModified?: string;
  etag?: string;
  storageClass?: string;
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
// are preserved as literal path separators in /s3/:bucket/*key. encodeURIComponent
// would escape them, which the router would then refuse.
function encPath(parts: string[]): string {
  return parts.map((p) => encodeURIComponent(p)).join('/');
}

export async function listBuckets(s: Session): Promise<Bucket[]> {
  const res = await fetch('/s3/', { headers: authHeaders(s) });
  if (!res.ok) await throwHTTP(res);
  const body = (await res.json()) as { buckets?: Bucket[] | null };
  return body.buckets ?? [];
}

export async function createBucket(s: Session, name: string): Promise<void> {
  const res = await fetch(`/s3/${encodeURIComponent(name)}`, {
    method: 'PUT',
    headers: authHeaders(s),
  });
  if (!res.ok) await throwHTTP(res);
}

export async function deleteBucket(s: Session, name: string): Promise<void> {
  const res = await fetch(`/s3/${encodeURIComponent(name)}`, {
    method: 'DELETE',
    headers: authHeaders(s),
  });
  if (!res.ok) await throwHTTP(res);
}

export async function listObjects(s: Session, bucket: string): Promise<S3Object[]> {
  const res = await fetch(`/s3/${encodeURIComponent(bucket)}`, {
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
  const res = await fetch(`/s3/${encodeURIComponent(bucket)}/${encPath(key.split('/'))}`, {
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
  const res = await fetch(`/s3/${encodeURIComponent(bucket)}/${encPath(key.split('/'))}`, {
    headers: {
      'X-Admin-AccessKey': s.accessKey,
      'X-Admin-Secret': s.secret,
    },
  });
  if (!res.ok) await throwHTTP(res);
  return await res.blob();
}

export async function deleteObject(s: Session, bucket: string, key: string): Promise<void> {
  const res = await fetch(`/s3/${encodeURIComponent(bucket)}/${encPath(key.split('/'))}`, {
    method: 'DELETE',
    headers: authHeaders(s),
  });
  if (!res.ok) await throwHTTP(res);
}

export async function headObject(s: Session, bucket: string, key: string): Promise<boolean> {
  const res = await fetch(`/s3/${encodeURIComponent(bucket)}/${encPath(key.split('/'))}`, {
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
  const res = await fetch(`/s3/${encodeURIComponent(bucket)}?cors`, {
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
  const res = await fetch(`/s3/${encodeURIComponent(bucket)}?cors`, {
    method: 'PUT',
    headers: { ...authHeaders(s), 'Content-Type': 'application/json' },
    body: JSON.stringify(cfg),
  });
  if (!res.ok) await throwHTTP(res);
}

export async function deleteBucketCORS(s: Session, bucket: string): Promise<void> {
  const res = await fetch(`/s3/${encodeURIComponent(bucket)}?cors`, {
    method: 'DELETE',
    headers: authHeaders(s),
  });
  if (res.status === 404) throw new NoSuchCORSConfiguration();
  if (!res.ok) await throwHTTP(res);
}
