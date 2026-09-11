// Client-side routes for the object browser, kept in one place so the list,
// the detail page and the dashboard link to the same URLs.
import { encodeKeyPath } from './s3';

export function objectDetailPath(bucket: string, key: string): string {
  return `/buckets/${encodeURIComponent(bucket)}/objects/${encodeKeyPath(key)}`;
}

export function objectListPath(bucket: string, prefix: string): string {
  const base = `/buckets/${encodeURIComponent(bucket)}/objects`;
  return prefix ? `${base}?prefix=${encodeURIComponent(prefix)}` : base;
}

// buildCrumbs turns "a/b/c/" into [{label:"a", prefix:"a/"}, {label:"b", prefix:"a/b/"}, ...]
// so each segment is independently clickable.
export function buildCrumbs(prefix: string): { label: string; prefix: string }[] {
  if (!prefix) return [];
  const parts = prefix.split('/').filter((p) => p.length > 0);
  const out: { label: string; prefix: string }[] = [];
  let acc = '';
  for (const p of parts) {
    acc += p + '/';
    out.push({ label: p, prefix: acc });
  }
  return out;
}
