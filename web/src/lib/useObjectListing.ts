import { useEffect, useRef, useState } from 'react';
import { getBucketACL, listObjects, type CannedACL, type S3Object } from './s3';
import type { Session } from './session';

export interface ObjectRow {
  key: string;
  size: number;
  modified?: string;
  acl: CannedACL;
  aclSource: 'object' | 'bucket' | 'default';
}

export interface ObjectListing {
  rows: ObjectRow[] | null;
  folders: string[];
  bucketACL: CannedACL;
  error: string | null;
  loadingMore: boolean;
  hasMore: boolean;
  refresh: () => Promise<void>;
  loadMore: () => Promise<void>;
  setError: (msg: string | null) => void;
}

function toRow(o: S3Object): ObjectRow {
  return {
    key: o.key,
    size: o.size,
    modified: o.lastModified ? new Date(o.lastModified).toISOString() : undefined,
    acl: o.acl ?? 'private',
    aclSource: o.aclSource ?? 'default',
  };
}

// useObjectListing owns the paginated, delimiter-scoped browse of one prefix.
// It re-fetches whenever bucket or prefix changes, exposes a manual loadMore
// that appends the next page, and guards every async write behind a sequence
// id so navigating away mid-fetch can never splice one folder's results into
// another's view.
export function useObjectListing(
  session: Session | null,
  bucket: string,
  prefix: string,
): ObjectListing {
  const [rows, setRows] = useState<ObjectRow[] | null>(null);
  const [folders, setFolders] = useState<string[]>([]);
  const [bucketACL, setBucketACL] = useState<CannedACL>('private');
  const [error, setError] = useState<string | null>(null);
  const [nextToken, setNextToken] = useState<string | undefined>(undefined);
  const [loadingMore, setLoadingMore] = useState(false);
  const listSeq = useRef(0);

  async function refresh() {
    if (!session || !bucket) return;
    const seq = ++listSeq.current;
    setError(null);
    setRows(null);
    setFolders([]);
    setNextToken(undefined);
    try {
      const [list, bAcl] = await Promise.all([
        listObjects(session, bucket, prefix, '/'),
        getBucketACL(session, bucket),
      ]);
      if (seq !== listSeq.current) return;
      setBucketACL(bAcl);
      setFolders(list.commonPrefixes);
      setRows(list.contents.map((o) => toRow(o)));
      setNextToken(list.isTruncated ? list.nextContinuationToken : undefined);
    } catch (e) {
      if (seq !== listSeq.current) return;
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function loadMore() {
    if (!session || !bucket || !nextToken || loadingMore) return;
    const seq = listSeq.current;
    setLoadingMore(true);
    setError(null);
    try {
      const list = await listObjects(session, bucket, prefix, '/', nextToken);
      if (seq !== listSeq.current) return;
      setFolders((prev) => {
        const seen = new Set(prev);
        return [...prev, ...list.commonPrefixes.filter((p) => !seen.has(p))];
      });
      setRows((prev) => {
        const base = prev ?? [];
        const seen = new Set(base.map((r) => r.key));
        return [...base, ...list.contents.filter((o) => !seen.has(o.key)).map((o) => toRow(o))];
      });
      setNextToken(list.isTruncated ? list.nextContinuationToken : undefined);
    } catch (e) {
      if (seq !== listSeq.current) return;
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      if (seq === listSeq.current) setLoadingMore(false);
    }
  }

  useEffect(() => {
    void refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [bucket, prefix]);

  return {
    rows,
    folders,
    bucketACL,
    error,
    loadingMore,
    hasMore: nextToken !== undefined,
    refresh,
    loadMore,
    setError,
  };
}
